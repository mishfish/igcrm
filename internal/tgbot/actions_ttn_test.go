package tgbot

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeShipper implements Shipper, recording calls into the slices below.
type fakeShipper struct {
	createCalls []struct {
		orderID          int64
		weight, declared float64
	}
	createTTN   string
	createErr   error
	deleteCalls []int64
	deleteErr   error
}

func (f *fakeShipper) CreateTTN(ctx context.Context, orderID int64, weight, declared float64) (string, error) {
	f.createCalls = append(f.createCalls, struct {
		orderID          int64
		weight, declared float64
	}{orderID, weight, declared})
	return f.createTTN, f.createErr
}

func (f *fakeShipper) DeleteTTN(ctx context.Context, orderID int64) error {
	f.deleteCalls = append(f.deleteCalls, orderID)
	return f.deleteErr
}

// mustGet is a local test helper for this file only (actions_ttn_test.go) — fetches a pending
// action or fails the test immediately; every test below sets the pending action itself first.
func mustGet(t *testing.T, p *PendingStore, userID int64) PendingAction {
	a, ok := p.Get(userID)
	if !ok {
		t.Fatalf("expected a pending action for user %d", userID)
	}
	return a
}

func TestCreateTTN_HappyPath(t *testing.T) {
	sh := &fakeShipper{createTTN: "20450000000001"}
	cl := &fakeClient{}
	b := &Bot{Store: &fakeStore{}, Shipping: sh, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	userID, chatID := int64(111), int64(9001)

	b.actionCreateTTNStart(context.Background(), &CallbackQuery{ID: "cb1", From: User{ID: userID}, Message: &Message{Chat: Chat{ID: chatID}}}, 10)
	b.handlePendingReply(context.Background(), &Message{Chat: Chat{ID: chatID}, From: &User{ID: userID}, Text: "1,2"}, mustGet(t, b.Pending, userID))
	p := mustGet(t, b.Pending, userID)
	if p.Step != "confirm" || p.Weight != 1.2 {
		t.Fatalf("pending after weight: %+v", p)
	}

	b.actionCreateTTNConfirm(context.Background(), &CallbackQuery{ID: "cb2", From: User{ID: userID}, Message: &Message{Chat: Chat{ID: chatID}, MessageID: 1}}, 10)
	if len(sh.createCalls) != 1 || sh.createCalls[0].weight != 1.2 {
		t.Fatalf("create calls: %+v", sh.createCalls)
	}
	if _, ok := b.Pending.Get(userID); ok {
		t.Error("pending must be cleared after a successful confirm")
	}
}

func TestCreateTTN_InvalidWeightAsksAgainWithoutClearingPending(t *testing.T) {
	cl := &fakeClient{}
	b := &Bot{Store: &fakeStore{}, Shipping: &fakeShipper{}, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	userID, chatID := int64(111), int64(9001)
	b.Pending.Set(userID, PendingAction{Kind: "create_ttn", OrderID: 10, Step: "await_weight", CreatedAt: time.Now()})
	b.handlePendingReply(context.Background(), &Message{Chat: Chat{ID: chatID}, From: &User{ID: userID}, Text: "не число"}, mustGet(t, b.Pending, userID))
	p, ok := b.Pending.Get(userID)
	if !ok || p.Step != "await_weight" {
		t.Fatalf("pending must survive a bad weight: %+v %v", p, ok)
	}
}

func TestCreateTTN_ConfirmWithoutMatchingPendingIsRejected(t *testing.T) {
	sh := &fakeShipper{}
	cl := &fakeClient{}
	b := &Bot{Store: &fakeStore{}, Shipping: sh, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	b.actionCreateTTNConfirm(context.Background(), &CallbackQuery{ID: "cb1", From: User{ID: 111}, Message: &Message{Chat: Chat{ID: 1}}}, 10)
	if len(sh.createCalls) != 0 {
		t.Fatal("must not call CreateTTN without a matching pending action")
	}
}

func TestCreateTTN_ShippingServiceErrorIsShownVerbatim(t *testing.T) {
	sh := &fakeShipper{createErr: fmt.Errorf("замовлення №10: невідома сума, вкажи оголошену вартість вручну")}
	cl := &fakeClient{}
	b := &Bot{Store: &fakeStore{}, Shipping: sh, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	userID := int64(111)
	b.Pending.Set(userID, PendingAction{Kind: "create_ttn", OrderID: 10, Step: "confirm", Weight: 1.2, CreatedAt: time.Now()})
	b.actionCreateTTNConfirm(context.Background(), &CallbackQuery{ID: "cb1", From: User{ID: userID}, Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}, 10)
	if len(cl.edited) != 1 || !strings.Contains(cl.edited[0].text, "невідома сума") {
		t.Fatalf("expected the service error to reach the user verbatim: %+v", cl.edited)
	}
}

func TestCreateTTN_RepeatedConfirmAfterSuccessDoesNotCreateASecondTTN(t *testing.T) {
	// simulates a redelivered Telegram update hitting confirm twice: second call has no
	// matching pending action (already cleared by the first), so it must be a no-op —
	// same guarantee shipping.Service.CreateTTN's own "ttn IS NULL" check gives at the DB layer.
	sh := &fakeShipper{createTTN: "ttn-1"}
	cl := &fakeClient{}
	b := &Bot{Store: &fakeStore{}, Shipping: sh, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	userID := int64(111)
	b.Pending.Set(userID, PendingAction{Kind: "create_ttn", OrderID: 10, Step: "confirm", Weight: 1.2, CreatedAt: time.Now()})
	cq := &CallbackQuery{ID: "cb1", From: User{ID: userID}, Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}
	b.actionCreateTTNConfirm(context.Background(), cq, 10)
	b.actionCreateTTNConfirm(context.Background(), cq, 10) // redelivered
	if len(sh.createCalls) != 1 {
		t.Fatalf("expected exactly one CreateTTN call, got %d", len(sh.createCalls))
	}
}

func TestCreateTTN_NilShippingRepliesConfigurationError(t *testing.T) {
	cl := &fakeClient{}
	b := &Bot{Store: &fakeStore{}, Shipping: nil, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	b.actionCreateTTNStart(context.Background(), &CallbackQuery{ID: "cb1", From: User{ID: 111}, Message: &Message{Chat: Chat{ID: 1}}}, 10)
	if len(cl.sent) != 1 || !strings.Contains(cl.sent[0].text, "не налаштован") {
		t.Fatalf("expected a configuration-error reply: %+v", cl.sent)
	}
}

func TestDeleteTTN_ConfirmCallsShipperDeleteTTN(t *testing.T) {
	sh := &fakeShipper{}
	cl := &fakeClient{}
	b := &Bot{Store: &fakeStore{}, Shipping: sh, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	userID := int64(111)
	b.Pending.Set(userID, PendingAction{Kind: "delete_ttn", OrderID: 10, Step: "confirm", CreatedAt: time.Now()})
	b.actionDeleteTTNConfirm(context.Background(), &CallbackQuery{ID: "cb1", From: User{ID: userID}, Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}, 10)
	if len(sh.deleteCalls) != 1 || sh.deleteCalls[0] != 10 {
		t.Fatalf("delete calls: %+v", sh.deleteCalls)
	}
}

// --- Additional coverage for the safety-critical paths and the wiring into handleCallback ---

func TestHandleCallback_CreateTTNMalformedIDAnswersOnce(t *testing.T) {
	for _, data := range []string{"create_ttn", "create_ttn:", "create_ttn:bad", "create_ttn_confirm:bad", "create_ttn_cancel:bad", "delete_ttn:bad", "delete_ttn_confirm:bad"} {
		t.Run(data, func(t *testing.T) {
			sh := &fakeShipper{}
			cl := &fakeClient{}
			b := &Bot{Store: &fakeStore{}, Shipping: sh, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
			cq := &CallbackQuery{ID: "cb1", From: User{ID: 111}, Data: data, Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}
			b.handleCallback(context.Background(), cq)
			if len(sh.createCalls) != 0 || len(sh.deleteCalls) != 0 {
				t.Errorf("malformed data must not act: create=%+v delete=%+v", sh.createCalls, sh.deleteCalls)
			}
			if len(cl.answered) != 1 {
				t.Errorf("expected exactly one answer, got %+v", cl.answered)
			}
		})
	}
}

func TestHandleCallback_CreateTTNStartDispatchesAndAnswers(t *testing.T) {
	sh := &fakeShipper{}
	cl := &fakeClient{}
	b := &Bot{Store: &fakeStore{}, Shipping: sh, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	cq := &CallbackQuery{ID: "cb1", From: User{ID: 111}, Data: "create_ttn:10", Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}
	b.handleCallback(context.Background(), cq)
	p, ok := b.Pending.Get(111)
	if !ok || p.Kind != "create_ttn" || p.OrderID != 10 || p.Step != "await_weight" {
		t.Fatalf("expected a pending create_ttn/await_weight action: %+v %v", p, ok)
	}
	if len(cl.answered) != 1 {
		t.Fatalf("expected exactly one answer: %+v", cl.answered)
	}
}

func TestActionCreateTTNCancel_ClearsOnlyMatchingPendingAndAnswersOnce(t *testing.T) {
	cl := &fakeClient{}
	b := &Bot{Store: &fakeStore{}, Shipping: &fakeShipper{}, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	userID := int64(111)
	b.Pending.Set(userID, PendingAction{Kind: "create_ttn", OrderID: 10, Step: "await_weight", CreatedAt: time.Now()})
	cq := &CallbackQuery{ID: "cb1", From: User{ID: userID}, Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}
	b.actionCreateTTNCancel(context.Background(), cq, 10)
	if _, ok := b.Pending.Get(userID); ok {
		t.Error("pending must be cleared on cancel")
	}
	if len(cl.answered) != 1 {
		t.Fatalf("expected exactly one answer: %+v", cl.answered)
	}

	// A cancel for an unrelated/stale dialog must not disturb anything, but
	// must still answer exactly once.
	cl2 := &fakeClient{}
	b2 := &Bot{Store: &fakeStore{}, Shipping: &fakeShipper{}, Client: cl2, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	cq2 := &CallbackQuery{ID: "cb2", From: User{ID: userID}, Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}
	b2.actionCreateTTNCancel(context.Background(), cq2, 10)
	if len(cl2.answered) != 1 {
		t.Fatalf("expected exactly one answer for a stale cancel: %+v", cl2.answered)
	}
}

// TestParseWeight_RejectsNaNInfAndAbsurdMagnitudes guards against
// strconv.ParseFloat's permissiveness: it alone would accept "nan",
// "inf"/"+inf"/"infinity" (case-insensitive) and huge values like "1e9" as
// valid floats, none of which are a sane parcel weight. Any of them reaching
// PendingAction.Weight would show as e.g. "вага NaN кг" in the confirm
// summary and, if confirmed, go to the Nova Poshta API as the literal string
// "NaN"/"+Inf".
func TestParseWeight_RejectsNaNInfAndAbsurdMagnitudes(t *testing.T) {
	bad := []string{"nan", "NaN", "inf", "+inf", "-inf", "Infinity", "1e9", "1e300", "0", "-1", "", "abc"}
	for _, in := range bad {
		if _, ok := parseWeight(in); ok {
			t.Errorf("parseWeight(%q) unexpectedly accepted", in)
		}
	}

	good := map[string]float64{"1.2": 1.2, "1,2": 1.2, " 1.2 ": 1.2, "0.5": 0.5, "999": 999, "1000": 1000}
	for in, want := range good {
		got, ok := parseWeight(in)
		if !ok || got != want {
			t.Errorf("parseWeight(%q) = %v, %v; want %v, true", in, got, ok, want)
		}
	}
}

// TestCreateTTN_NaNAndInfWeightRepliesAreRejectedLikeAnyInvalidWeight exercises
// the same path as TestCreateTTN_InvalidWeightAsksAgainWithoutClearingPending
// but for the specific inputs strconv.ParseFloat alone would have accepted:
// "nan" and "inf" must never reach PendingAction.Weight or Shipping.CreateTTN.
func TestCreateTTN_NaNAndInfWeightRepliesAreRejectedLikeAnyInvalidWeight(t *testing.T) {
	for _, text := range []string{"nan", "inf", "+inf", "1e9"} {
		t.Run(text, func(t *testing.T) {
			sh := &fakeShipper{}
			cl := &fakeClient{}
			b := &Bot{Store: &fakeStore{}, Shipping: sh, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
			userID, chatID := int64(111), int64(9001)
			b.Pending.Set(userID, PendingAction{Kind: "create_ttn", OrderID: 10, Step: "await_weight", CreatedAt: time.Now()})
			b.handlePendingReply(context.Background(), &Message{Chat: Chat{ID: chatID}, From: &User{ID: userID}, Text: text}, mustGet(t, b.Pending, userID))
			p, ok := b.Pending.Get(userID)
			if !ok || p.Step != "await_weight" {
				t.Fatalf("pending must survive a %q weight reply: %+v %v", text, p, ok)
			}
			if len(sh.createCalls) != 0 {
				t.Fatalf("must never reach CreateTTN for a %q weight: %+v", text, sh.createCalls)
			}
		})
	}
}

func TestHandlePendingReply_SlashCancelClearsAnyKind(t *testing.T) {
	cl := &fakeClient{}
	b := &Bot{Store: &fakeStore{}, Shipping: &fakeShipper{}, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	userID, chatID := int64(111), int64(9001)
	b.Pending.Set(userID, PendingAction{Kind: "delete_ttn", OrderID: 10, Step: "confirm", CreatedAt: time.Now()})
	consumed := b.handlePendingReply(context.Background(), &Message{Chat: Chat{ID: chatID}, From: &User{ID: userID}, Text: "/cancel"}, mustGet(t, b.Pending, userID))
	if !consumed {
		t.Error("expected /cancel to be consumed")
	}
	if _, ok := b.Pending.Get(userID); ok {
		t.Error("expected /cancel to clear the pending action")
	}
	if len(cl.sent) != 1 || !strings.Contains(cl.sent[0].text, "Скасовано") {
		t.Fatalf("expected a cancellation reply: %+v", cl.sent)
	}
}
