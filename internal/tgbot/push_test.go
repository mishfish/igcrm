package tgbot

import (
	"context"
	"testing"

	"igcrm/internal/store"
)

func TestPushWorker_FansOutReviewAndOrderPushesToAllRegisteredUsers(t *testing.T) {
	st := &fakeStore{
		users:         []store.BotUser{{TGUserID: 111, ChatID: 9001}, {TGUserID: 222, ChatID: 9002}},
		needingReview: []int64{}, // ContactsPendingReviewPush uses a separate fake field below
	}
	st.reviewPushPending = []int64{1}
	st.contacts = map[int64]store.Contact{1: {ID: 1}}
	st.ttnPushPending = []int64{10}
	st.botOrders = map[int64]store.BotOrder{10: {ID: 10}}

	cl := &fakeClient{}
	w := &PushWorker{Store: st, Client: cl, Log: discardLog()}
	w.Tick(context.Background())

	if len(cl.sent) != 4 { // 1 review card + 1 order card, each to 2 users
		t.Fatalf("sent: %d messages, want 4: %+v", len(cl.sent), cl.sent)
	}
	if len(st.reviewPushedIDs) != 1 || len(st.ttnPushedIDs) != 1 {
		t.Fatalf("mark-pushed calls: review=%v ttn=%v", st.reviewPushedIDs, st.ttnPushedIDs)
	}
}

func TestPushWorker_UnregisteredAllowedUserDoesNotBreakFanout(t *testing.T) {
	// a user can be in TG_ALLOWED_USER_IDS but never have sent /start — they simply have
	// no row in BotUsers(); the push worker only reads BotUsers(), so this is already the
	// natural behavior, this test just pins it.
	st := &fakeStore{users: []store.BotUser{{TGUserID: 111, ChatID: 9001}}, reviewPushPending: []int64{1}, contacts: map[int64]store.Contact{1: {ID: 1}}}
	cl := &fakeClient{}
	w := &PushWorker{Store: st, Client: cl, Log: discardLog()}
	w.Tick(context.Background())
	if len(cl.sent) != 1 {
		t.Fatalf("sent: %+v", cl.sent)
	}
}

func TestPushWorker_NothingPendingSendsNothing(t *testing.T) {
	st := &fakeStore{users: []store.BotUser{{TGUserID: 111, ChatID: 9001}}}
	cl := &fakeClient{}
	w := &PushWorker{Store: st, Client: cl, Log: discardLog()}
	w.Tick(context.Background())
	if len(cl.sent) != 0 {
		t.Fatalf("sent: %+v", cl.sent)
	}
}
