package tgbot

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// parseSetStatusCallback parses the remainder of a "set_status:<id>:<status>"
// callback's data after handleCallback's default 2-way split has already
// peeled off the "set_status" action prefix (that split only separates the
// prefix from everything after the first ":"; the remainder here still needs
// a second split into order id and status). It returns an error for a
// missing separator or a non-numeric id; any status string, recognized or
// not, is returned as-is — SetOrderStatus itself enforces the allowed-status
// allowlist at the store layer, so there's no need to duplicate that check
// here (an unrecognized status still flows through actionSetStatusStart,
// which answers the callback exactly once regardless of what the status
// string is).
func parseSetStatusCallback(rest string) (orderID int64, status string, err error) {
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 {
		return 0, "", fmt.Errorf("malformed set_status callback data %q", rest)
	}
	orderID, err = strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, "", err
	}
	return orderID, parts[1], nil
}

// setStatusConfirmKeyboard builds the confirm/cancel keyboard shown after a
// status button is tapped.
func setStatusConfirmKeyboard(orderID int64) *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{
			{Text: "✅ Підтвердити", CallbackData: fmt.Sprintf("set_status_confirm:%d", orderID)},
			{Text: "❌ Скасувати", CallbackData: fmt.Sprintf("set_status_cancel:%d", orderID)},
		},
	}}
}

// actionSetStatusStart begins the set-status dialog: it stashes the chosen
// status as a pending "confirm"-step action and prompts for confirmation. It
// answers the callback exactly once on every path.
func (b *Bot) actionSetStatusStart(ctx context.Context, cq *CallbackQuery, orderID int64, status string) {
	chatID, ok := b.callbackChatID(cq)
	if !ok {
		b.answerCallback(ctx, cq, "")
		return
	}

	b.Pending.Set(cq.From.ID, PendingAction{Kind: "set_status", OrderID: orderID, Status: status, Step: "confirm", CreatedAt: time.Now()})
	text := fmt.Sprintf("Позначити замовлення #%d як %q?", orderID, status)
	if _, err := b.Client.SendMessage(ctx, chatID, text, setStatusConfirmKeyboard(orderID)); err != nil {
		b.log().Error("send message", "error", err, "chat_id", chatID)
	}
	b.answerCallback(ctx, cq, "")
}

// actionSetStatusCancel clears a set-status dialog (matching this orderID, if
// one is still pending) and acknowledges the button tap. It is a no-op if
// the pending action no longer matches, so a stale/duplicate tap is safe.
func (b *Bot) actionSetStatusCancel(ctx context.Context, cq *CallbackQuery, orderID int64) {
	if p, ok := b.Pending.Get(cq.From.ID); ok && p.Kind == "set_status" && p.OrderID == orderID {
		b.Pending.Clear(cq.From.ID)
	}
	b.editCallbackMessage(ctx, cq, "Скасовано.") // no-op (logged) if cq.Message is nil
	b.answerCallback(ctx, cq, "Скасовано")
}

// actionSetStatusConfirm executes a set-status dialog that has reached the
// "confirm" step. It rejects (answering the callback, taking no other
// action) when there is no matching pending action — covering both a stale
// button from an unrelated/expired dialog and a redelivered double-tap of
// this same button. Pending.Take removes the entry atomically before
// Store.SetOrderStatus is ever called, mirroring actionCreateTTNConfirm's and
// actionDeleteTTNConfirm's safety ordering (see their doc comments): even
// though a wrong status write is a smaller harm than a duplicate shipment,
// there's no reason to fall back to the weaker Get+Clear pattern now that
// Take is the established idiom in this package.
func (b *Bot) actionSetStatusConfirm(ctx context.Context, cq *CallbackQuery, orderID int64) {
	p, ok := b.Pending.Take(cq.From.ID)
	if !ok || p.Kind != "set_status" || p.OrderID != orderID {
		b.answerCallback(ctx, cq, staleActionMessage)
		return
	}

	if err := b.Store.SetOrderStatus(ctx, orderID, p.Status); err != nil {
		b.editCallbackMessage(ctx, cq, "Помилка: "+err.Error())
		b.answerCallback(ctx, cq, "")
		return
	}

	b.editCallbackMessage(ctx, cq, fmt.Sprintf("Статус замовлення #%d змінено на %q.", orderID, p.Status))
	detail, _ := json.Marshal(struct {
		Status string `json:"status"`
	}{p.Status})
	if err := b.Store.LogBotAction(ctx, cq.From.ID, "set_status", nil, &orderID, detail); err != nil {
		b.log().Error("log bot action", "error", err, "tg_user_id", cq.From.ID)
	}
	b.answerCallback(ctx, cq, "Статус оновлено")
}
