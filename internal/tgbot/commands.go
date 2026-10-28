package tgbot

import (
	"context"
	"fmt"
)

const (
	// pullFetchLimit is how many pending IDs /review and /orders fetch, separate
	// from the 5-card display cap, so the overflow note's count is accurate. It
	// is not meant as a pagination limit.
	pullFetchLimit = 200
	// pullCardLimit caps how many cards one reply sends; the rest are noted.
	pullCardLimit = 5
)

// cmdReview sends a card with a confirm button for each contact awaiting review.
func (b *Bot) cmdReview(ctx context.Context, chatID int64) {
	ids, err := b.Store.ContactsNeedingReview(ctx, pullFetchLimit)
	if err != nil {
		b.log().Error("list contacts needing review", "error", err)
		b.send(ctx, chatID, "Не вдалося отримати чергу перевірки.")
		return
	}
	if len(ids) == 0 {
		b.send(ctx, chatID, "Немає карток на перевірку.")
		return
	}
	for _, id := range firstN(ids, pullCardLimit) {
		c, err := b.Store.Contact(ctx, id)
		if err != nil {
			b.log().Error("load contact", "error", err, "contact_id", id)
			continue
		}
		ext, err := b.Store.LastExtraction(ctx, id)
		if err != nil {
			b.log().Error("load last extraction", "error", err, "contact_id", id)
			continue
		}
		b.sendCard(ctx, chatID, RenderContactCard(c, ext.Changes), ReviewKeyboard(id, b.DirectusURL))
	}
	b.sendOverflow(ctx, chatID, len(ids))
}

// cmdOrders sends a summary with action buttons for each order pending shipment.
func (b *Bot) cmdOrders(ctx context.Context, chatID int64) {
	ids, err := b.Store.OrdersPendingShipment(ctx, pullFetchLimit)
	if err != nil {
		b.log().Error("list orders pending shipment", "error", err)
		b.send(ctx, chatID, "Не вдалося отримати список замовлень.")
		return
	}
	if len(ids) == 0 {
		b.send(ctx, chatID, "Немає замовлень, що очікують відправки.")
		return
	}
	for _, id := range firstN(ids, pullCardLimit) {
		o, err := b.Store.BotOrder(ctx, id)
		if err != nil {
			b.log().Error("load order", "error", err, "order_id", id)
			continue
		}
		b.sendCard(ctx, chatID, RenderOrderSummary(o), OrderActionsKeyboard(o, b.DirectusURL))
	}
	b.sendOverflow(ctx, chatID, len(ids))
}

// firstN returns at most n leading elements of ids.
func firstN(ids []int64, n int) []int64 {
	if len(ids) > n {
		return ids[:n]
	}
	return ids
}

// sendCard sends one card with its keyboard, logging (not propagating) errors.
func (b *Bot) sendCard(ctx context.Context, chatID int64, text string, kb *InlineKeyboardMarkup) {
	if _, err := b.Client.SendMessage(ctx, chatID, text, kb); err != nil {
		b.log().Error("send card", "error", err, "chat_id", chatID)
	}
}

// sendOverflow sends the "and N more" note when total exceeds pullCardLimit.
func (b *Bot) sendOverflow(ctx context.Context, chatID int64, total int) {
	if total <= pullCardLimit {
		return
	}
	b.send(ctx, chatID, fmt.Sprintf("...і ще %d — дивись у Directus.", total-pullCardLimit))
}
