package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"igcrm/internal/ig"
	"igcrm/internal/notify"
	"igcrm/internal/store"
)

// TokenRefresher подовжує токен, коли до кінця менше Threshold, і кричить, якщо не вийшло.
type TokenRefresher struct {
	Store     *store.Store
	IG        *ig.Client
	Notify    *notify.Notifier
	Threshold time.Duration
	Log       *slog.Logger
}

func (r *TokenRefresher) Run(ctx context.Context) {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		r.Check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (r *TokenRefresher) Check(ctx context.Context) {
	tok, err := r.Store.Token(ctx)
	if errors.Is(err, store.ErrNoToken) {
		r.Notify.Alert(ctx, "немає токена Instagram: задай IG_ACCESS_TOKEN")
		return
	}
	if err != nil {
		r.Log.Error("token load", "err", err)
		return
	}
	if tok.ExpiresAt != nil && time.Until(*tok.ExpiresAt) > r.Threshold {
		return
	}
	newTok, exp, err := r.IG.Refresh(ctx, tok.AccessToken)
	if err != nil && tok.ExpiresAt == nil {
		// Термін невідомий (щойно вставлений токен). Свіжий токен (<24 год) оновити не можна —
		// це не аварія, якщо він працює. Спробуємо ще раз за 6 годин.
		if _, meErr := r.IG.Me(ctx); meErr == nil {
			r.Log.Warn("token refresh postponed (токен робочий, термін невідомий)", "err", err)
			return
		}
	}
	if err != nil {
		left := "невідомо"
		if tok.ExpiresAt != nil {
			left = time.Until(*tok.ExpiresAt).Round(time.Hour).String()
		}
		r.Notify.Alert(ctx, "не вдалося оновити токен Instagram (до кінця: "+left+"): "+err.Error())
		return
	}
	if err := r.Store.SaveToken(ctx, newTok, exp); err != nil {
		r.Notify.Alert(ctx, "токен оновлено, але не збережено в БД: "+err.Error())
		return
	}
	r.Log.Info("instagram token refreshed", "expires_at", exp.Format(time.RFC3339))
}

// ProfileFetcher підтягує @username та ім'я для нових контактів.
type ProfileFetcher struct {
	Store *store.Store
	IG    *ig.Client
	Log   *slog.Logger
}

func (p *ProfileFetcher) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		p.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (p *ProfileFetcher) Tick(ctx context.Context) {
	jobs, err := p.Store.ContactsWithoutProfile(ctx, 20)
	if err != nil {
		p.Log.Error("profiles list", "err", err)
		return
	}
	for _, j := range jobs {
		prof, err := p.IG.Profile(ctx, j.IGSID)
		if err != nil {
			p.Log.Warn("profile fetch", "contact", j.ID, "err", err)
			_ = p.Store.ProfileFailed(ctx, j.ID)
			continue
		}
		if err := p.Store.SetProfile(ctx, j.ID, prof.Username, prof.Name); err != nil {
			p.Log.Error("profile save", "contact", j.ID, "err", err)
		}
	}
}

// Backfill разово заливає старі переписки через Conversations API.
func Backfill(ctx context.Context, st *store.Store, c *ig.Client, debounce time.Duration, log *slog.Logger) error {
	me, err := c.Me(ctx)
	if err != nil {
		return err
	}
	if me.UserID == "" {
		// Graph API інколи повертає 200 з порожніми полями; без UserID неможливо
		// відрізнити бізнес-акаунт від клієнта серед учасників розмови
		return fmt.Errorf("ig: Me() returned empty user id")
	}
	log.Info("backfill start", "account", me.Username)
	var convs, saved int
	err = c.Conversations(ctx, func(conv ig.Conversation) error {
		convs++
		var client ig.Participant
		for _, p := range conv.Participants.Data {
			if p.ID != me.UserID && p.Username != me.Username {
				client = p
			}
		}
		if client.ID == "" {
			return nil
		}
		for _, m := range conv.Messages.Data {
			if m.ID == "" || m.Message == "" {
				continue
			}
			dir := "in"
			if m.From.ID == me.UserID || (me.Username != "" && m.From.Username == me.Username) {
				dir = "out"
			}
			ok, err := st.SaveMessage(ctx, store.Message{
				MID: m.ID, IGSID: client.ID, Direction: dir, Text: m.Message,
				SentAt: ig.ParseGraphTime(m.CreatedTime), Source: "backfill",
			}, debounce)
			if err != nil {
				return err
			}
			if ok {
				saved++
			}
		}
		return nil
	})
	log.Info("backfill done", "conversations", convs, "new_messages", saved)
	return err
}
