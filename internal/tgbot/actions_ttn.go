package tgbot

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// invalidRequestMessage answers a callback whose data cannot be parsed.
const invalidRequestMessage = "Некоректний запит"

// openActionHint is sent when the user writes something while a dialog is
// waiting on a button tap.
const openActionHint = "Є незавершена дія — підтвердь або скасуй кнопкою, або надішли /cancel."

// sideEffectTimeout bounds a confirm's real side effects once started.
const sideEffectTimeout = 60 * time.Second

// sideEffectCtx detaches a confirm's side effects (Nova Poshta call + audit
// row) from ctx cancellation so a SIGTERM mid-call cannot abort a shipment
// halfway and lose the record, while still bounding them with a timeout.
func sideEffectCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), sideEffectTimeout)
}

// logAction writes an audit row; on failure the fallback log line carries the
// whole record so it can be reconstructed from logs.
func (b *Bot) logAction(ctx context.Context, tgUserID int64, action string, contactID, orderID *int64, detail json.RawMessage) {
	if err := b.Store.LogBotAction(ctx, tgUserID, action, contactID, orderID, detail); err != nil {
		attrs := []any{"error", err, "tg_user_id", tgUserID, "action", action, "detail", string(detail)}
		if contactID != nil {
			attrs = append(attrs, "contact_id", *contactID)
		}
		if orderID != nil {
			attrs = append(attrs, "order_id", *orderID)
		}
		b.log().Error("log bot action", attrs...)
	}
}

// takeConfirm atomically consumes the user's pending action iff it is the
// confirm-step action of the given kind/order AND the tap comes from the very
// prompt message that action was issued with. Otherwise the current pending
// action is left intact, the stale prompt (if the user has another dialog
// open) loses its keyboard, and the callback is answered as stale.
func (b *Bot) takeConfirm(ctx context.Context, cq *CallbackQuery, kind string, orderID int64) (PendingAction, bool) {
	p, ok := b.Pending.TakeIf(cq.From.ID, func(p PendingAction) bool {
		return p.Kind == kind && p.OrderID == orderID && p.Step == "confirm" &&
			cq.Message != nil && cq.Message.MessageID == p.PromptMsgID
	})
	if ok {
		return p, true
	}
	// Only touch the message when another dialog is open: after a completed
	// confirm the message already shows the result and must not be overwritten.
	if _, open := b.Pending.Get(cq.From.ID); open && cq.Message != nil {
		text := "Застаріло."
		if cq.Message.Text != "" {
			text = cq.Message.Text + "\n\n(застаріло)"
		}
		b.editCallbackMessage(ctx, cq, text)
	}
	b.answerCallback(ctx, cq, staleActionMessage)
	return PendingAction{}, false
}

// ttnConfigErrorMessage is sent whenever an action needs b.Shipping but it is
// nil (NP_API_KEY not configured on this deployment).
const ttnConfigErrorMessage = "ТТН не налаштована на сервері (NP_API_KEY)."

// weightPromptMessage is sent after "Створити ТТН" is tapped, before the
// confirm step.
const weightPromptMessage = "Введіть вагу посилки в кг (наприклад: 1.2). /cancel — скасувати."

// invalidWeightMessage is sent when the free-text reply to weightPromptMessage
// does not parse as a sane positive weight. The pending action is left
// untouched so the user can simply try again.
const invalidWeightMessage = "Не розпізнав вагу, спробуй ще раз (наприклад 1.2)."

// maxParcelWeightKg bounds what parseWeight accepts. A shipment over a tonne
// is never legitimate for this business, and this also rejects +Inf (which
// is > maxParcelWeightKg, since Go's < on NaN is always false but a plain
// numeric comparison against +Inf correctly reports it as larger).
const maxParcelWeightKg = 1000.0

// parseWeight parses a free-text weight reply such as "1,2" or "1.2" into a
// sane positive kilogram value. strconv.ParseFloat alone would also accept
// "nan", "inf"/"+inf"/"infinity" (case-insensitive) and absurd magnitudes
// like "1e9", any of which would otherwise flow straight into
// PendingAction.Weight, the confirm summary text, and ultimately the Nova
// Poshta API call — so every one of those is rejected here explicitly.
func parseWeight(text string) (float64, bool) {
	w, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(text), ",", "."), 64)
	if err != nil || math.IsNaN(w) || math.IsInf(w, 0) || w <= 0 || w > maxParcelWeightKg {
		return 0, false
	}
	return w, true
}

// maxDeclaredValueUAH bounds what parseDeclaredValue accepts.
const maxDeclaredValueUAH = 1000000.0

// parseDeclaredValue parses a free-text declared-value reply such as "1500" or
// "1500,50" into a non-negative UAH amount. 0 is syntactically valid (it means
// "use the order amount"; the caller decides whether that is allowed).
// NaN, Inf, negatives and values above maxDeclaredValueUAH are rejected.
func parseDeclaredValue(text string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(text), ",", "."), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > maxDeclaredValueUAH {
		return 0, false
	}
	return v, true
}

// orderAmount returns the order's known positive amount; false if the order
// has no amount or cannot be loaded.
func (b *Bot) orderAmount(ctx context.Context, orderID int64) (float64, bool) {
	order, err := b.Store.BotOrder(ctx, orderID)
	if err != nil {
		b.log().Error("load order for declared value", "error", err, "order_id", orderID)
		return 0, false
	}
	if order.Amount == nil || *order.Amount <= 0 {
		return 0, false
	}
	return *order.Amount, true
}

// declaredPrompt builds the declared-value question, with a hint about the
// order amount when it is known.
func declaredPrompt(amount float64, hasAmount bool) string {
	text := "Оголошена вартість у грн (наприклад 1500)."
	if hasAmount {
		text += fmt.Sprintf(" Надішліть 0, щоб взяти суму замовлення (%s грн).", formatUAH(amount))
	}
	return text
}

func formatUAH(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// invalidDeclaredMessage re-prompts after an unparsable declared value.
const invalidDeclaredMessage = "Не розпізнав вартість, спробуй ще раз (наприклад 1500)."

// noAmountMessage re-prompts when 0 was sent but the order has no amount.
const noAmountMessage = "У замовлення немає суми — вкажи реальну оголошену вартість у грн (наприклад 1500)."

// staleActionMessage answers a confirm/cancel tap that no longer has a
// matching pending action: either it was already handled (a redelivered
// double-tap) or it belongs to a different, expired dialog.
const staleActionMessage = "Дія застаріла, почни заново"

// parseCallbackOrderID extracts the order id from the last ":"-separated
// segment of callback data such as "create_ttn:10" or "create_ttn_confirm:10".
func parseCallbackOrderID(data string) (int64, error) {
	parts := strings.SplitN(data, ":", 2)
	return strconv.ParseInt(parts[len(parts)-1], 10, 64)
}

// answerCallback answers a callback query and logs (without propagating) any
// error, so every call site can be a single line.
func (b *Bot) answerCallback(ctx context.Context, cq *CallbackQuery, text string) {
	if err := b.Client.AnswerCallbackQuery(ctx, cq.ID, text); err != nil {
		b.log().Error("answer callback query", "error", err, "callback_id", cq.ID)
	}
}

// editCallbackMessage edits the message a callback was attached to and logs
// (without propagating) any error. cq.Message should always be present for a
// button this bot itself generated (this bot never uses Telegram inline
// mode), but a missing Message is logged rather than panicking — a panic on
// the success path of a confirm would crash Run and lose the LogBotAction
// audit entry for a shipment/cancellation that already genuinely happened.
func (b *Bot) editCallbackMessage(ctx context.Context, cq *CallbackQuery, text string) {
	b.editCallbackMessageKB(ctx, cq, text, nil)
}

// editCallbackMessageKB is editCallbackMessage with an optional keyboard.
func (b *Bot) editCallbackMessageKB(ctx context.Context, cq *CallbackQuery, text string, kb *InlineKeyboardMarkup) {
	if cq.Message == nil {
		b.log().Error("edit message: callback has no attached message", "callback_id", cq.ID)
		return
	}
	if err := b.Client.EditMessageText(ctx, cq.Message.Chat.ID, cq.Message.MessageID, text, kb); err != nil {
		b.log().Error("edit message", "error", err, "chat_id", cq.Message.Chat.ID)
	}
}

// orderActionsKeyboard fetches the order and builds its action keyboard for a
// result message. The action it follows has already completed, so a lookup
// failure is only logged and yields nil (message edited without a keyboard).
func (b *Bot) orderActionsKeyboard(ctx context.Context, orderID int64) *InlineKeyboardMarkup {
	order, err := b.Store.BotOrder(ctx, orderID)
	if err != nil {
		b.log().Error("load order for result keyboard", "error", err, "order_id", orderID)
		return nil
	}
	return OrderActionsKeyboard(order, b.DirectusURL)
}

// callbackChatID returns the chat id of the message a callback is attached
// to, and false if there is none (see editCallbackMessage's doc comment).
func (b *Bot) callbackChatID(cq *CallbackQuery) (int64, bool) {
	if cq.Message == nil {
		b.log().Error("callback has no attached message", "callback_id", cq.ID)
		return 0, false
	}
	return cq.Message.Chat.ID, true
}

// createTTNConfirmKeyboard builds the confirm/cancel keyboard shown once a
// weight has been entered.
func createTTNConfirmKeyboard(orderID int64) *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{
			{Text: "✅ Підтвердити", CallbackData: fmt.Sprintf("create_ttn_confirm:%d", orderID)},
			{Text: "❌ Скасувати", CallbackData: fmt.Sprintf("create_ttn_cancel:%d", orderID)},
		},
	}}
}

// actionCreateTTNStart begins the create-TTN dialog: if shipping isn't
// configured it replies with a configuration error; otherwise it opens a
// pending "await_weight" dialog and prompts for the parcel's weight. Either
// way it answers the callback exactly once.
func (b *Bot) actionCreateTTNStart(ctx context.Context, cq *CallbackQuery, orderID int64) {
	chatID, ok := b.callbackChatID(cq)
	if !ok {
		b.answerCallback(ctx, cq, "")
		return
	}

	if b.Shipping == nil {
		if _, err := b.Client.SendMessage(ctx, chatID, ttnConfigErrorMessage, nil); err != nil {
			b.log().Error("send message", "error", err, "chat_id", chatID)
		}
		b.answerCallback(ctx, cq, "")
		return
	}

	b.Pending.Set(cq.From.ID, PendingAction{Kind: "create_ttn", OrderID: orderID, Step: "await_weight", CreatedAt: time.Now()})
	if _, err := b.Client.SendMessage(ctx, chatID, weightPromptMessage, nil); err != nil {
		b.log().Error("send message", "error", err, "chat_id", chatID)
	}
	b.answerCallback(ctx, cq, "")
}

// actionCreateTTNCancel clears a create-TTN dialog (matching this orderID,
// if one is still pending) and acknowledges the button tap. It is a no-op if
// the pending action no longer matches, so a stale/duplicate tap is safe.
func (b *Bot) actionCreateTTNCancel(ctx context.Context, cq *CallbackQuery, orderID int64) {
	if p, ok := b.Pending.Get(cq.From.ID); ok && p.Kind == "create_ttn" && p.OrderID == orderID {
		b.Pending.Clear(cq.From.ID)
	}
	b.editCallbackMessage(ctx, cq, "Скасовано.") // no-op (logged) if cq.Message is nil
	b.answerCallback(ctx, cq, "Скасовано")
}

// actionCreateTTNConfirm executes a create-TTN dialog that has reached the
// "confirm" step. It rejects (answering the callback, taking no other
// action) when there is no matching pending action — covering both a stale
// button from an unrelated/expired dialog and a redelivered double-tap of
// this same button. Pending.Take removes the entry atomically (under a
// single lock acquisition, not a separate Get then Clear) before
// Shipping.CreateTTN is ever called, so even a double-tap racing this call
// on another goroutine cannot both observe the pending action as present —
// the second one always finds it already gone and is rejected above.
func (b *Bot) actionCreateTTNConfirm(ctx context.Context, cq *CallbackQuery, orderID int64) {
	p, ok := b.takeConfirm(ctx, cq, "create_ttn", orderID)
	if !ok {
		return
	}

	weight := p.Weight
	execCtx, cancel := sideEffectCtx(ctx)
	defer cancel()
	ttn, err := b.Shipping.CreateTTN(execCtx, orderID, weight, p.DeclaredValue)
	if err != nil {
		b.editCallbackMessage(ctx, cq, "Помилка: "+err.Error())
		b.answerCallback(ctx, cq, "")
		return
	}

	b.editCallbackMessageKB(ctx, cq, "ТТН створено: "+ttn, b.orderActionsKeyboard(execCtx, orderID))
	detail, _ := json.Marshal(struct {
		TTN           string  `json:"ttn"`
		Weight        float64 `json:"weight"`
		DeclaredValue float64 `json:"declared_value"`
	}{ttn, weight, p.DeclaredValue})
	b.logAction(execCtx, cq.From.ID, "create_ttn", nil, &orderID, detail)
	b.answerCallback(ctx, cq, "ТТН створено")
}

// actionDeleteTTNStart begins the cancel-TTN dialog. Unlike create, it has no
// weight step: it goes straight to "confirm".
func (b *Bot) actionDeleteTTNStart(ctx context.Context, cq *CallbackQuery, orderID int64) {
	chatID, ok := b.callbackChatID(cq)
	if !ok {
		b.answerCallback(ctx, cq, "")
		return
	}

	if b.Shipping == nil {
		if _, err := b.Client.SendMessage(ctx, chatID, ttnConfigErrorMessage, nil); err != nil {
			b.log().Error("send message", "error", err, "chat_id", chatID)
		}
		b.answerCallback(ctx, cq, "")
		return
	}

	kb := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{{Text: "✅ Підтвердити", CallbackData: fmt.Sprintf("delete_ttn_confirm:%d", orderID)}},
	}}
	text := fmt.Sprintf("Скасувати ТТН замовлення #%d? /cancel — залишити.", orderID)
	msgID, err := b.Client.SendMessage(ctx, chatID, text, kb)
	if err != nil {
		b.log().Error("send message", "error", err, "chat_id", chatID)
	}
	b.Pending.Set(cq.From.ID, PendingAction{Kind: "delete_ttn", OrderID: orderID, Step: "confirm", PromptMsgID: msgID, CreatedAt: time.Now()})
	b.answerCallback(ctx, cq, "")
}

// actionDeleteTTNConfirm mirrors actionCreateTTNConfirm's safety ordering
// (Pending.Take before calling Shipping), without a weight.
func (b *Bot) actionDeleteTTNConfirm(ctx context.Context, cq *CallbackQuery, orderID int64) {
	if _, ok := b.takeConfirm(ctx, cq, "delete_ttn", orderID); !ok {
		return
	}

	execCtx, cancel := sideEffectCtx(ctx)
	defer cancel()
	if err := b.Shipping.DeleteTTN(execCtx, orderID); err != nil {
		b.editCallbackMessage(ctx, cq, "Помилка: "+err.Error())
		b.answerCallback(ctx, cq, "")
		return
	}

	b.editCallbackMessage(ctx, cq, "ТТН скасовано.")
	b.logAction(execCtx, cq.From.ID, "delete_ttn", nil, &orderID, nil)
	b.answerCallback(ctx, cq, "ТТН скасовано")
}

// handlePendingReply handles a free-text message while the sender has a
// pending dialog (create_ttn's weight prompt, or /cancel for any dialog). It
// reports whether the message was consumed, so handleMessage can fall back to
// its normal command switch when it wasn't (currently: whenever the pending
// action isn't waiting on free text, e.g. it's already at a confirm step
// that's meant to be resolved by tapping a button).
func (b *Bot) handlePendingReply(ctx context.Context, m *Message, p PendingAction) bool {
	userID := m.From.ID

	if m.Text == "/cancel" || strings.HasPrefix(m.Text, "/cancel@") {
		b.Pending.Clear(userID)
		b.send(ctx, m.Chat.ID, "Скасовано.")
		return true
	}

	if p.Kind == "create_ttn" && p.Step == "await_weight" {
		weight, ok := parseWeight(m.Text)
		if !ok {
			b.send(ctx, m.Chat.ID, invalidWeightMessage)
			return true
		}

		p.Weight = weight
		p.Step = "await_declared"
		b.Pending.Set(userID, p)
		amount, has := b.orderAmount(ctx, p.OrderID)
		b.send(ctx, m.Chat.ID, declaredPrompt(amount, has))
		return true
	}

	if p.Kind == "create_ttn" && p.Step == "await_declared" {
		value, ok := parseDeclaredValue(m.Text)
		if !ok {
			b.send(ctx, m.Chat.ID, invalidDeclaredMessage)
			return true
		}
		valueText := formatUAH(value) + " грн"
		if value == 0 {
			amount, has := b.orderAmount(ctx, p.OrderID)
			if !has {
				b.send(ctx, m.Chat.ID, noAmountMessage)
				return true
			}
			valueText = fmt.Sprintf("сума замовлення (%s грн)", formatUAH(amount))
		}

		p.DeclaredValue = value
		p.Step = "confirm"

		text := fmt.Sprintf("Замовлення #%d: створити ТТН, вага %.2f кг, оголошена вартість: %s?", p.OrderID, p.Weight, valueText)
		msgID, err := b.Client.SendMessage(ctx, m.Chat.ID, text, createTTNConfirmKeyboard(p.OrderID))
		if err != nil {
			b.log().Error("send message", "error", err, "chat_id", m.Chat.ID)
		}
		p.PromptMsgID = msgID
		b.Pending.Set(userID, p)
		return true
	}

	// A pending action exists but isn't waiting on free text (e.g. it's
	// sitting at a "confirm" step meant to be resolved by tapping a button).
	// Consume the message anyway rather than letting it fall through to an
	// unrelated command while a money-critical dialog is still open, but tell
	// the user why nothing happened.
	b.send(ctx, m.Chat.ID, openActionHint)
	return true
}
