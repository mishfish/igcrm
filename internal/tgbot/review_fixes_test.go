package tgbot

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"igcrm/internal/store"
)

func TestPushWorker_SkipsUsersNotOnAllowlist(t *testing.T) {
	st := &fakeStore{
		users:             []store.BotUser{{TGUserID: 111, ChatID: 9001}, {TGUserID: 999, ChatID: 9999}},
		reviewPushPending: []int64{1},
		contacts:          map[int64]store.Contact{1: {ID: 1}},
	}
	cl := &fakeClient{}
	w := &PushWorker{Store: st, Client: cl, Allowed: Allowlist{111: true}}
	w.Tick(context.Background())
	if len(cl.sent) != 1 || cl.sent[0].chatID != 9001 {
		t.Fatalf("only the allowed user may receive pushes: %+v", cl.sent)
	}
}

func TestPushWorker_NoAllowedRecipientsMarksNothingPushed(t *testing.T) {
	st := &fakeStore{
		users:             []store.BotUser{{TGUserID: 999, ChatID: 9999}},
		reviewPushPending: []int64{1},
		contacts:          map[int64]store.Contact{1: {ID: 1}},
		ttnPushPending:    []int64{10},
	}
	cl := &fakeClient{}
	w := &PushWorker{Store: st, Client: cl, Allowed: Allowlist{111: true}}
	w.Tick(context.Background())
	if len(cl.sent) != 0 || len(st.reviewPushedIDs) != 0 || len(st.ttnPushedIDs) != 0 {
		t.Fatalf("nothing may be sent or marked pushed: sent=%v review=%v ttn=%v", cl.sent, st.reviewPushedIDs, st.ttnPushedIDs)
	}
}

func staleSetup(t *testing.T) (*Bot, *fakeStore, *fakeShipper, *fakeClient) {
	st, sh, cl := &fakeStore{}, &fakeShipper{createTTN: "ttn"}, &fakeClient{}
	return &Bot{Store: st, Shipping: sh, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}, st, sh, cl
}

func TestSetStatus_StaleConfirmOnSupersededPromptIsRejected(t *testing.T) {
	b, st, _, cl := staleSetup(t)
	ctx := context.Background()
	msg := func(id int64) *Message { return &Message{Chat: Chat{ID: 1}, MessageID: id} }
	b.actionSetStatusStart(ctx, &CallbackQuery{ID: "a", From: User{ID: 111}, Message: msg(100)}, 10, "paid")
	promptA := cl.nextMessageID
	b.actionSetStatusStart(ctx, &CallbackQuery{ID: "b", From: User{ID: 111}, Message: msg(100)}, 10, "cancelled")
	promptB := cl.nextMessageID

	b.actionSetStatusConfirm(ctx, &CallbackQuery{ID: "c", From: User{ID: 111}, Message: msg(promptA)}, 10)
	if len(st.setStatusCalls) != 0 {
		t.Fatalf("stale prompt must not execute: %+v", st.setStatusCalls)
	}
	if p, ok := b.Pending.Get(111); !ok || p.Status != "cancelled" {
		t.Fatalf("B's pending must stay intact: %+v %v", p, ok)
	}
	if len(cl.edited) != 1 || cl.edited[0].messageID != promptA || cl.edited[0].kb != nil {
		t.Fatalf("stale prompt should lose its keyboard: %+v", cl.edited)
	}
	b.actionSetStatusConfirm(ctx, &CallbackQuery{ID: "d", From: User{ID: 111}, Message: msg(promptB)}, 10)
	if len(st.setStatusCalls) != 1 || st.setStatusCalls[0].status != "cancelled" {
		t.Fatalf("B must execute with its own status: %+v", st.setStatusCalls)
	}
}

func TestCreateTTN_StaleConfirmOnSupersededPromptIsRejected(t *testing.T) {
	b, _, sh, cl := staleSetup(t)
	ctx := context.Background()
	start := func() {
		b.actionCreateTTNStart(ctx, &CallbackQuery{ID: "s", From: User{ID: 111}, Message: &Message{Chat: Chat{ID: 1}}}, 10)
	}
	reply := func(text string) int64 {
		p, _ := b.Pending.Get(111)
		b.handlePendingReply(ctx, &Message{Chat: Chat{ID: 1}, From: &User{ID: 111}, Text: text}, p)
		return cl.nextMessageID
	}
	start()
	promptA := reply("1")
	start() // re-open the dialog; weight prompt replaced pending
	promptB := reply("2.5")

	cq := func(id int64) *CallbackQuery {
		return &CallbackQuery{ID: "c", From: User{ID: 111}, Message: &Message{Chat: Chat{ID: 1}, MessageID: id}}
	}
	b.actionCreateTTNConfirm(ctx, cq(promptA), 10)
	if len(sh.createCalls) != 0 {
		t.Fatalf("stale prompt must not execute: %+v", sh.createCalls)
	}
	if p, ok := b.Pending.Get(111); !ok || p.Weight != 2.5 {
		t.Fatalf("B's pending must stay intact: %+v %v", p, ok)
	}
	b.actionCreateTTNConfirm(ctx, cq(promptB), 10)
	if len(sh.createCalls) != 1 || sh.createCalls[0].weight != 2.5 {
		t.Fatalf("B must execute with its own weight: %+v", sh.createCalls)
	}
}

func TestPendingStore_TakeIfLeavesNonMatching(t *testing.T) {
	p := NewPendingStore(time.Minute)
	p.Set(1, PendingAction{Kind: "x"})
	if _, ok := p.TakeIf(1, func(PendingAction) bool { return false }); ok {
		t.Fatal("must not match")
	}
	if _, ok := p.Get(1); !ok {
		t.Fatal("non-matching action must remain")
	}
	if _, ok := p.TakeIf(1, func(a PendingAction) bool { return a.Kind == "x" }); !ok {
		t.Fatal("must match")
	}
	if _, ok := p.Get(1); ok {
		t.Fatal("matching action must be consumed")
	}
}

type errClient struct {
	*fakeClient
	calls atomic.Int64
}

func (c *errClient) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error) {
	c.calls.Add(1)
	return nil, errors.New("boom")
}

func TestRun_BacksOffOnGetUpdatesErrors(t *testing.T) {
	cl := &errClient{fakeClient: &fakeClient{}}
	b := &Bot{Store: &fakeStore{}, Client: cl, Log: discardLog(), retryMin: 40 * time.Millisecond, retryMax: 80 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	b.Run(ctx)
	// 40+80+80+... ms: at most ~5 calls in 250ms; a busy loop would be thousands.
	if n := cl.calls.Load(); n < 2 || n > 8 {
		t.Fatalf("calls=%d, expected a bounded retry rate", n)
	}
}

func TestPendingConfirmStep_OtherTextGetsHintAndCancelWithBotSuffixWorks(t *testing.T) {
	cl := &fakeClient{}
	b := &Bot{Store: &fakeStore{}, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	b.Pending.Set(111, PendingAction{Kind: "delete_ttn", OrderID: 10, Step: "confirm", PromptMsgID: 1})
	p, _ := b.Pending.Get(111)
	m := func(text string) *Message { return &Message{Chat: Chat{ID: 1}, From: &User{ID: 111}, Text: text} }
	if !b.handlePendingReply(context.Background(), m("/orders"), p) {
		t.Fatal("must be consumed")
	}
	if len(cl.sent) != 1 || !strings.Contains(cl.sent[0].text, "/cancel") {
		t.Fatalf("expected a hint: %+v", cl.sent)
	}
	b.handlePendingReply(context.Background(), m("/cancel@my_bot"), p)
	if _, ok := b.Pending.Get(111); ok {
		t.Fatal("/cancel@bot must clear pending")
	}
}

func TestHandleUpdate_GroupChatIsIgnored(t *testing.T) {
	st, cl := &fakeStore{}, &fakeClient{}
	b := &Bot{Store: st, Client: cl, Allowed: Allowlist{111: true}, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	b.handleUpdate(context.Background(), Update{Message: &Message{Chat: Chat{ID: -5, Type: "group"}, From: &User{ID: 111}, Text: "/start"}})
	b.handleUpdate(context.Background(), Update{CallbackQuery: &CallbackQuery{ID: "x", From: User{ID: 111}, Data: "confirm_review:1", Message: &Message{Chat: Chat{ID: -5, Type: "supergroup"}}}})
	if len(st.registered) != 0 || len(cl.sent) != 0 || len(cl.answered) != 0 {
		t.Fatalf("group updates must be ignored: %+v %+v %+v", st.registered, cl.sent, cl.answered)
	}
	b.handleUpdate(context.Background(), Update{Message: &Message{Chat: Chat{ID: 5, Type: "private"}, From: &User{ID: 111}, Text: "/start"}})
	if len(st.registered) != 1 {
		t.Fatal("private chat must still work")
	}
}

type panicStore struct{ *fakeStore }

func (panicStore) RegisterBotUser(context.Context, int64, int64, string) error { panic("boom") }

func TestHandleUpdate_RecoversFromPanic(t *testing.T) {
	b := &Bot{Store: panicStore{&fakeStore{}}, Client: &fakeClient{}, Allowed: Allowlist{111: true}, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	b.handleUpdate(context.Background(), Update{UpdateID: 7, Message: &Message{Chat: Chat{ID: 1}, From: &User{ID: 111}, Text: "/start"}}) // must not panic
}

func TestActionConfirmReview_NilMessageDoesNotPanic(t *testing.T) {
	st, cl := &fakeStore{}, &fakeClient{}
	b := &Bot{Store: st, Client: cl, Log: discardLog()}
	b.actionConfirmReview(context.Background(), &CallbackQuery{ID: "x", From: User{ID: 111}}, 7)
	if len(st.actions) != 1 || len(cl.answered) != 1 {
		t.Fatalf("actions=%v answered=%v", st.actions, cl.answered)
	}
}

func TestParseAllowlist_SkipsEmptyEntries(t *testing.T) {
	for _, in := range []string{"123,", "123, ,456", ",123"} {
		a, err := ParseAllowlist(in)
		if err != nil || !a.Allowed(123) {
			t.Fatalf("%q: %v %v", in, a, err)
		}
	}
	if a, _ := ParseAllowlist("123, ,456"); !a.Allowed(456) {
		t.Fatal("456 missing")
	}
	if _, err := ParseAllowlist("123,abc"); err == nil {
		t.Fatal("non-numeric must still fail")
	}
}
