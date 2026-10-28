package tgbot

// bot_test.go — shared fakes, reused by Tasks 8–12's tests in the same package.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"igcrm/internal/store"
)

// fakeStore implements Store, recording calls into the slices/maps below.
// Every method returns zero values / nil error unless a field like
// confirmErr/setStatusErr says otherwise. Do not rename/add/remove fields:
// Tasks 8–12's tests construct this type by field name.
type fakeStore struct {
	offset     int64
	users      []store.BotUser
	registered []struct {
		tgUserID, chatID int64
		username         string
	}
	actions []struct {
		tgUserID int64
		action   string
	}
	needingReview     []int64
	contacts          map[int64]store.Contact
	lastExtraction    map[int64]store.Extraction
	confirmErr        map[int64]error
	reviewPushPending []int64
	reviewPushedIDs   []int64
	pendingShipment   []int64
	botOrders         map[int64]store.BotOrder
	ttnPushPending    []int64
	ttnPushedIDs      []int64
	setStatusCalls    []struct {
		id     int64
		status string
	}
	setStatusErr error
}

func (s *fakeStore) BotUpdateOffset(ctx context.Context) (int64, error) {
	return s.offset, nil
}

func (s *fakeStore) SetBotUpdateOffset(ctx context.Context, updateID int64) error {
	s.offset = updateID
	return nil
}

func (s *fakeStore) RegisterBotUser(ctx context.Context, tgUserID, chatID int64, username string) error {
	s.registered = append(s.registered, struct {
		tgUserID, chatID int64
		username         string
	}{tgUserID, chatID, username})
	return nil
}

func (s *fakeStore) BotUsers(ctx context.Context) ([]store.BotUser, error) {
	return s.users, nil
}

func (s *fakeStore) LogBotAction(ctx context.Context, tgUserID int64, action string, contactID, orderID *int64, detail json.RawMessage) error {
	s.actions = append(s.actions, struct {
		tgUserID int64
		action   string
	}{tgUserID, action})
	return nil
}

func (s *fakeStore) Contact(ctx context.Context, id int64) (store.Contact, error) {
	return s.contacts[id], nil
}

func (s *fakeStore) ContactsNeedingReview(ctx context.Context, limit int) ([]int64, error) {
	return s.needingReview, nil
}

func (s *fakeStore) ConfirmReview(ctx context.Context, id int64) error {
	if err, ok := s.confirmErr[id]; ok {
		return err
	}
	return nil
}

func (s *fakeStore) ContactsPendingReviewPush(ctx context.Context, limit int) ([]int64, error) {
	return s.reviewPushPending, nil
}

func (s *fakeStore) MarkReviewPushed(ctx context.Context, id int64) error {
	s.reviewPushedIDs = append(s.reviewPushedIDs, id)
	return nil
}

func (s *fakeStore) LastExtraction(ctx context.Context, contactID int64) (store.Extraction, error) {
	return s.lastExtraction[contactID], nil
}

func (s *fakeStore) BotOrder(ctx context.Context, id int64) (store.BotOrder, error) {
	return s.botOrders[id], nil
}

func (s *fakeStore) OrdersPendingShipment(ctx context.Context, limit int) ([]int64, error) {
	return s.pendingShipment, nil
}

func (s *fakeStore) OrdersPendingTTNPush(ctx context.Context, limit int) ([]int64, error) {
	return s.ttnPushPending, nil
}

func (s *fakeStore) MarkTTNPushed(ctx context.Context, id int64) error {
	s.ttnPushedIDs = append(s.ttnPushedIDs, id)
	return nil
}

func (s *fakeStore) SetOrderStatus(ctx context.Context, id int64, status string) error {
	s.setStatusCalls = append(s.setStatusCalls, struct {
		id     int64
		status string
	}{id, status})
	return s.setStatusErr
}

// fakeClient implements telegramClient with no network calls.
type fakeClient struct {
	nextMessageID int64
	sent          []struct {
		chatID int64
		text   string
		kb     *InlineKeyboardMarkup
	}
	edited []struct {
		chatID, messageID int64
		text              string
		kb                *InlineKeyboardMarkup
	}
	answered []struct {
		id, text string
	}
	updates [][]Update // one slice per call to GetUpdates; empty/exhausted → blocks until ctx.Done()
}

func (c *fakeClient) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error) {
	if len(c.updates) > 0 {
		batch := c.updates[0]
		c.updates = c.updates[1:]
		return batch, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c *fakeClient) SendMessage(ctx context.Context, chatID int64, text string, kb *InlineKeyboardMarkup) (int64, error) {
	c.nextMessageID++
	c.sent = append(c.sent, struct {
		chatID int64
		text   string
		kb     *InlineKeyboardMarkup
	}{chatID, text, kb})
	return c.nextMessageID, nil
}

func (c *fakeClient) EditMessageText(ctx context.Context, chatID, messageID int64, text string, kb *InlineKeyboardMarkup) error {
	c.edited = append(c.edited, struct {
		chatID, messageID int64
		text              string
		kb                *InlineKeyboardMarkup
	}{chatID, messageID, text, kb})
	return nil
}

func (c *fakeClient) AnswerCallbackQuery(ctx context.Context, callbackID, text string) error {
	c.answered = append(c.answered, struct{ id, text string }{callbackID, text})
	return nil
}

// discardLog is the one place this helper is defined — every later task's tests (8–12) call
// discardLog() from this file by same-package visibility; do NOT redefine it in another _test.go
// file in this package (Go will fail to compile on the duplicate declaration).
func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestHandleUpdate_UnauthorizedIsIgnored(t *testing.T) {
	st := &fakeStore{}
	cl := &fakeClient{}
	b := &Bot{Store: st, Client: cl, Allowed: Allowlist{111: true}, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	b.handleUpdate(context.Background(), Update{Message: &Message{Chat: Chat{ID: 1}, From: &User{ID: 999}, Text: "/start"}})
	if len(st.registered) != 0 || len(cl.sent) != 0 {
		t.Fatalf("unauthorized update must be a no-op: %+v %+v", st.registered, cl.sent)
	}
}

func TestHandleUpdate_StartRegistersAllowedUser(t *testing.T) {
	st := &fakeStore{}
	cl := &fakeClient{}
	b := &Bot{Store: st, Client: cl, Allowed: Allowlist{111: true}, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	b.handleUpdate(context.Background(), Update{Message: &Message{Chat: Chat{ID: 9001}, From: &User{ID: 111, Username: "owner"}, Text: "/start"}})
	if len(st.registered) != 1 || st.registered[0].chatID != 9001 {
		t.Fatalf("expected registration: %+v", st.registered)
	}
	if len(cl.sent) != 1 || cl.sent[0].chatID != 9001 {
		t.Fatalf("expected a usage reply sent to the chat: %+v", cl.sent)
	}
}

// TestRun_AdvancesOffsetOnlyAfterProcessing drives Run with a fakeClient
// whose GetUpdates returns exactly one batch (update_id=5) and then blocks
// until ctx is cancelled. Because Run is single-threaded and processes a
// batch's updates before persisting the new offset (see Run's doc comment),
// and because this fakeClient only ever completes one iteration before
// blocking forever, SetBotUpdateOffset is necessarily called exactly once,
// and only after handleUpdate (and therefore RegisterBotUser) already ran.
func TestRun_AdvancesOffsetOnlyAfterProcessing(t *testing.T) {
	st := &fakeStore{}
	cl := &fakeClient{
		updates: [][]Update{
			{{UpdateID: 5, Message: &Message{Chat: Chat{ID: 1}, From: &User{ID: 111}, Text: "/start"}}},
		},
	}
	b := &Bot{Store: st, Client: cl, Allowed: Allowlist{111: true}, Pending: NewPendingStore(time.Minute), Log: discardLog()}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		b.Run(ctx)
		close(done)
	}()

	// Let Run process the one available batch and block on the next
	// GetUpdates call, then cancel and wait for a clean return.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx was cancelled")
	}

	if len(st.registered) != 1 {
		t.Fatalf("expected the update to have been dispatched exactly once, got %d registrations", len(st.registered))
	}
	if st.offset != 5 {
		t.Fatalf("expected the offset to advance to the last processed update_id (5), got %d", st.offset)
	}
}

func TestHandleUpdate_OverRateLimitIsIgnoredLikeUnauthorized(t *testing.T) {
	st := &fakeStore{}
	cl := &fakeClient{}
	rl := NewRateLimiter(1, time.Minute)
	b := &Bot{Store: st, Client: cl, Allowed: Allowlist{111: true}, RateLimit: rl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	msg := func() Update {
		return Update{Message: &Message{Chat: Chat{ID: 1}, From: &User{ID: 111}, Text: "/start"}}
	}
	b.handleUpdate(context.Background(), msg()) // consumes the one allowed slot
	b.handleUpdate(context.Background(), msg()) // over budget
	if len(st.registered) != 1 {
		t.Fatalf("expected exactly one registration, got %d", len(st.registered))
	}
}
