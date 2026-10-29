package tgbot

import (
	"context"
	"log/slog"
	"time"

	"igcrm/internal/store"
)

// defaultPushInterval is used when PushWorker.Interval is zero.
const defaultPushInterval = 20 * time.Second

// PushWorker polls the store for review/shipment items that still need a
// push notification and fans each one out to every registered Telegram
// user, mirroring extract.Worker's Run-calls-Tick-on-a-ticker shape.
type PushWorker struct {
	Store       Store
	Client      telegramClient
	DirectusURL string
	Interval    time.Duration
	Log         *slog.Logger
	// Allowed restricts recipients: a registered user who is no longer on
	// TG_ALLOWED_USER_IDS must not keep receiving customer data.
	Allowed Allowlist
}

// log returns w.Log, falling back to a discard logger if unset.
func (w *PushWorker) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Run ticks until ctx is cancelled, defaulting Interval to 20s if unset.
func (w *PushWorker) Run(ctx context.Context) {
	interval := w.Interval
	if interval == 0 {
		interval = defaultPushInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		w.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick fans out every pending review and TTN-shipment push to every
// registered bot user, then marks each item pushed exactly once. A
// per-send error is logged and skipped so one user's failure never stops
// the fan-out to the rest, nor skips marking the item pushed.
func (w *PushWorker) Tick(ctx context.Context) {
	users, err := w.Store.BotUsers(ctx)
	if err != nil {
		w.log().Error("push: load bot users", "err", err)
		return
	}

	var recipients []store.BotUser
	for _, u := range users {
		if w.Allowed.Allowed(u.TGUserID) {
			recipients = append(recipients, u)
		}
	}
	if len(recipients) == 0 {
		// Nobody to send to: leave everything unpushed for when someone is.
		return
	}
	users = recipients

	w.tickReviews(ctx, users)
	w.tickTTNs(ctx, users)
}

func (w *PushWorker) tickReviews(ctx context.Context, users []store.BotUser) {
	ids, err := w.Store.ContactsPendingReviewPush(ctx, 20)
	if err != nil {
		w.log().Error("push: contacts pending review push", "err", err)
		return
	}
	for _, id := range ids {
		c, err := w.Store.Contact(ctx, id)
		if err != nil {
			w.log().Error("push: load contact", "contact", id, "err", err)
			continue
		}
		ext, err := w.Store.LastExtraction(ctx, id)
		if err != nil {
			w.log().Error("push: load last extraction", "contact", id, "err", err)
			continue
		}
		text := RenderContactCard(c, ext.Changes)
		kb := ReviewKeyboard(id, w.DirectusURL)
		for _, u := range users {
			if _, err := w.Client.SendMessage(ctx, u.ChatID, text, kb); err != nil {
				w.log().Error("push: send review card", "contact", id, "chat_id", u.ChatID, "err", err)
			}
		}
		if err := w.Store.MarkReviewPushed(ctx, id); err != nil {
			w.log().Error("push: mark review pushed", "contact", id, "err", err)
		}
	}
}

func (w *PushWorker) tickTTNs(ctx context.Context, users []store.BotUser) {
	ids, err := w.Store.OrdersPendingTTNPush(ctx, 20)
	if err != nil {
		w.log().Error("push: orders pending ttn push", "err", err)
		return
	}
	for _, id := range ids {
		o, err := w.Store.BotOrder(ctx, id)
		if err != nil {
			w.log().Error("push: load order", "order", id, "err", err)
			continue
		}
		text := RenderOrderSummary(o)
		kb := OrderActionsKeyboard(o, w.DirectusURL)
		for _, u := range users {
			if _, err := w.Client.SendMessage(ctx, u.ChatID, text, kb); err != nil {
				w.log().Error("push: send order card", "order", id, "chat_id", u.ChatID, "err", err)
			}
		}
		if err := w.Store.MarkTTNPushed(ctx, id); err != nil {
			w.log().Error("push: mark ttn pushed", "order", id, "err", err)
		}
	}
}
