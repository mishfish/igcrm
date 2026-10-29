package tgbot

import "context"

// actionConfirmReview marks a contact's review as confirmed. On success it
// removes the inline keyboard from the message, writes an audit log entry, and
// acknowledges the callback. If the confirm fails (for example, the review was
// already confirmed), it only acknowledges the callback: no edit and no audit
// entry, since nothing was executed.
func (b *Bot) actionConfirmReview(ctx context.Context, cq *CallbackQuery, contactID int64) {
	if err := b.Store.ConfirmReview(ctx, contactID); err != nil {
		b.log().Info("confirm review skipped", "error", err, "contact_id", contactID)
		b.answerCallback(ctx, cq, "Вже підтверджено або не знайдено")
		return
	}

	b.editCallbackMessage(ctx, cq, "✅ Підтверджено")
	b.logAction(ctx, cq.From.ID, "confirm_review", &contactID, nil, nil)
	b.answerCallback(ctx, cq, "Підтверджено")
}
