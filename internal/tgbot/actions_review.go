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
		if err := b.Client.AnswerCallbackQuery(ctx, cq.ID, "Вже підтверджено або не знайдено"); err != nil {
			b.log().Error("answer callback query", "error", err, "callback_id", cq.ID)
		}
		return
	}

	if err := b.Client.EditMessageText(ctx, cq.Message.Chat.ID, cq.Message.MessageID, "✅ Підтверджено", nil); err != nil {
		b.log().Error("edit message", "error", err, "chat_id", cq.Message.Chat.ID)
	}
	if err := b.Store.LogBotAction(ctx, cq.From.ID, "confirm_review", &contactID, nil, nil); err != nil {
		b.log().Error("log bot action", "error", err, "tg_user_id", cq.From.ID)
	}
	if err := b.Client.AnswerCallbackQuery(ctx, cq.ID, "Підтверджено"); err != nil {
		b.log().Error("answer callback query", "error", err, "callback_id", cq.ID)
	}
}
