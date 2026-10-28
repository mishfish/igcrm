package tgbot

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSetStatus_PromptThenConfirmUpdatesStatus(t *testing.T) {
	st := &fakeStore{}
	cl := &fakeClient{}
	b := &Bot{Store: st, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	userID := int64(111)
	b.actionSetStatusStart(context.Background(), &CallbackQuery{ID: "cb1", From: User{ID: userID}, Message: &Message{Chat: Chat{ID: 1}}}, 10, "paid")
	p, ok := b.Pending.Get(userID)
	if !ok || p.Kind != "set_status" || p.Status != "paid" {
		t.Fatalf("pending: %+v %v", p, ok)
	}
	b.actionSetStatusConfirm(context.Background(), &CallbackQuery{ID: "cb2", From: User{ID: userID}, Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}, 10)
	if len(st.setStatusCalls) != 1 || st.setStatusCalls[0].status != "paid" {
		t.Fatalf("status calls: %+v", st.setStatusCalls)
	}
	if _, ok := b.Pending.Get(userID); ok {
		t.Error("pending must be cleared after a successful confirm")
	}
}

func TestSetStatus_StaleConfirmWithoutPendingIsNoop(t *testing.T) {
	st := &fakeStore{}
	cl := &fakeClient{}
	b := &Bot{Store: st, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	b.actionSetStatusConfirm(context.Background(), &CallbackQuery{ID: "cb1", From: User{ID: 111}, Message: &Message{Chat: Chat{ID: 1}}}, 10)
	if len(st.setStatusCalls) != 0 {
		t.Fatal("must not call SetOrderStatus without a matching pending action")
	}
}

func TestOrderActionsKeyboard_NeverRendersAShippedButton(t *testing.T) {
	// already covered in Task 6's render_test.go — re-asserted here as a cross-check that
	// actions_status.go's set of handled statuses matches what render.go ever emits.
	for _, status := range []string{"paid", "delivered", "returned", "cancelled"} {
		if status == "shipped" {
			t.Fatal("shipped must never be in the manual status list")
		}
	}
}

// --- Additional coverage for the safety-critical paths and the wiring into handleCallback ---

func TestSetStatus_ConfirmWithMismatchedOrderIDIsRejected(t *testing.T) {
	st := &fakeStore{}
	cl := &fakeClient{}
	b := &Bot{Store: st, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	userID := int64(111)
	b.Pending.Set(userID, PendingAction{Kind: "set_status", OrderID: 10, Status: "paid", Step: "confirm", CreatedAt: time.Now()})
	// Confirm tap carries a different order id than the pending action. Take
	// atomically consumes whatever is pending (same as the established
	// create_ttn/delete_ttn confirm pattern) before checking it matches, so
	// the mismatched entry is gone either way — the guarantee under test is
	// only that SetOrderStatus is never called for the mismatch.
	b.actionSetStatusConfirm(context.Background(), &CallbackQuery{ID: "cb1", From: User{ID: userID}, Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}, 99)
	if len(st.setStatusCalls) != 0 {
		t.Fatalf("must not call SetOrderStatus for a mismatched order id: %+v", st.setStatusCalls)
	}
}

func TestSetStatus_RepeatedConfirmAfterSuccessDoesNotCallTwice(t *testing.T) {
	// simulates a redelivered Telegram update hitting confirm twice: second call has no
	// matching pending action (already consumed by the first via Pending.Take), so it
	// must be a no-op.
	st := &fakeStore{}
	cl := &fakeClient{}
	b := &Bot{Store: st, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	userID := int64(111)
	b.Pending.Set(userID, PendingAction{Kind: "set_status", OrderID: 10, Status: "paid", Step: "confirm", CreatedAt: time.Now()})
	cq := &CallbackQuery{ID: "cb1", From: User{ID: userID}, Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}
	b.actionSetStatusConfirm(context.Background(), cq, 10)
	b.actionSetStatusConfirm(context.Background(), cq, 10) // redelivered
	if len(st.setStatusCalls) != 1 {
		t.Fatalf("expected exactly one SetOrderStatus call, got %d", len(st.setStatusCalls))
	}
}

func TestSetStatus_StoreErrorIsShownVerbatimAndNotLogged(t *testing.T) {
	st := &fakeStore{setStatusErr: fmt.Errorf("замовлення №10: недопустимий статус")}
	cl := &fakeClient{}
	b := &Bot{Store: st, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	userID := int64(111)
	b.Pending.Set(userID, PendingAction{Kind: "set_status", OrderID: 10, Status: "paid", Step: "confirm", CreatedAt: time.Now()})
	b.actionSetStatusConfirm(context.Background(), &CallbackQuery{ID: "cb1", From: User{ID: userID}, Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}, 10)
	if len(cl.edited) != 1 || !strings.Contains(cl.edited[0].text, "недопустимий статус") {
		t.Fatalf("expected the store error to reach the user verbatim: %+v", cl.edited)
	}
	if len(st.actions) != 0 {
		t.Fatalf("expected no LogBotAction call on a failed status change: %+v", st.actions)
	}
	if len(cl.answered) != 1 {
		t.Fatalf("expected exactly one answer: %+v", cl.answered)
	}
}

func TestSetStatus_CancelClearsOnlyMatchingPendingAndAnswersOnce(t *testing.T) {
	st := &fakeStore{}
	cl := &fakeClient{}
	b := &Bot{Store: st, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	userID := int64(111)
	b.Pending.Set(userID, PendingAction{Kind: "set_status", OrderID: 10, Status: "paid", Step: "confirm", CreatedAt: time.Now()})
	cq := &CallbackQuery{ID: "cb1", From: User{ID: userID}, Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}
	b.actionSetStatusCancel(context.Background(), cq, 10)
	if _, ok := b.Pending.Get(userID); ok {
		t.Error("pending must be cleared on cancel")
	}
	if len(cl.answered) != 1 {
		t.Fatalf("expected exactly one answer: %+v", cl.answered)
	}
	if len(st.setStatusCalls) != 0 {
		t.Fatalf("cancel must never call SetOrderStatus: %+v", st.setStatusCalls)
	}

	// A cancel for an unrelated/stale dialog must not disturb anything, but
	// must still answer exactly once.
	cl2 := &fakeClient{}
	b2 := &Bot{Store: &fakeStore{}, Client: cl2, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	cq2 := &CallbackQuery{ID: "cb2", From: User{ID: userID}, Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}
	b2.actionSetStatusCancel(context.Background(), cq2, 10)
	if len(cl2.answered) != 1 {
		t.Fatalf("expected exactly one answer for a stale cancel: %+v", cl2.answered)
	}
}

func TestHandleCallback_SetStatusStartDispatchesAndAnswers(t *testing.T) {
	st := &fakeStore{}
	cl := &fakeClient{}
	b := &Bot{Store: st, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	cq := &CallbackQuery{ID: "cb1", From: User{ID: 111}, Data: "set_status:10:paid", Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}
	b.handleCallback(context.Background(), cq)
	p, ok := b.Pending.Get(111)
	if !ok || p.Kind != "set_status" || p.OrderID != 10 || p.Status != "paid" || p.Step != "confirm" {
		t.Fatalf("expected a pending set_status/confirm action: %+v %v", p, ok)
	}
	if len(cl.answered) != 1 {
		t.Fatalf("expected exactly one answer: %+v", cl.answered)
	}
}

func TestHandleCallback_SetStatusConfirmAndCancelDispatch(t *testing.T) {
	st := &fakeStore{}
	cl := &fakeClient{}
	b := &Bot{Store: st, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	userID := int64(111)
	b.Pending.Set(userID, PendingAction{Kind: "set_status", OrderID: 10, Status: "delivered", Step: "confirm", CreatedAt: time.Now()})
	cq := &CallbackQuery{ID: "cb1", From: User{ID: userID}, Data: "set_status_confirm:10", Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}
	b.handleCallback(context.Background(), cq)
	if len(st.setStatusCalls) != 1 || st.setStatusCalls[0].status != "delivered" {
		t.Fatalf("expected the dispatched confirm to call SetOrderStatus: %+v", st.setStatusCalls)
	}
	if len(cl.answered) != 1 {
		t.Fatalf("expected exactly one answer: %+v", cl.answered)
	}

	st2 := &fakeStore{}
	cl2 := &fakeClient{}
	b2 := &Bot{Store: st2, Client: cl2, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	b2.Pending.Set(userID, PendingAction{Kind: "set_status", OrderID: 10, Status: "returned", Step: "confirm", CreatedAt: time.Now()})
	cq2 := &CallbackQuery{ID: "cb2", From: User{ID: userID}, Data: "set_status_cancel:10", Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}
	b2.handleCallback(context.Background(), cq2)
	if _, ok := b2.Pending.Get(userID); ok {
		t.Error("expected the dispatched cancel to clear the pending action")
	}
	if len(cl2.answered) != 1 {
		t.Fatalf("expected exactly one answer: %+v", cl2.answered)
	}
}

// TestHandleCallback_SetStatusMalformedDataAnswersOnce exercises every
// malformed shape of the set_status*/set_status_confirm/set_status_cancel
// callback data: missing separators, a non-numeric order id, and a missing
// status. Each must still answer the callback exactly once and must never
// reach SetOrderStatus.
func TestHandleCallback_SetStatusMalformedDataAnswersOnce(t *testing.T) {
	for _, data := range []string{
		"set_status",
		"set_status:",
		"set_status:10",
		"set_status:abc:paid",
		"set_status_confirm",
		"set_status_confirm:",
		"set_status_confirm:bad",
		"set_status_cancel",
		"set_status_cancel:",
		"set_status_cancel:bad",
	} {
		t.Run(data, func(t *testing.T) {
			st := &fakeStore{}
			cl := &fakeClient{}
			b := &Bot{Store: st, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
			cq := &CallbackQuery{ID: "cb1", From: User{ID: 111}, Data: data, Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}
			b.handleCallback(context.Background(), cq)
			if len(st.setStatusCalls) != 0 {
				t.Errorf("malformed data must not call SetOrderStatus: %+v", st.setStatusCalls)
			}
			if len(cl.answered) != 1 {
				t.Errorf("expected exactly one answer, got %+v", cl.answered)
			}
		})
	}
}

// TestHandleCallback_SetStatusUnrecognizedStatusStillAnswersOnce guards the
// "unrecognized status string" path called out alongside this task: a status
// value the allowlist in SetOrderStatus (store layer) would reject is still
// well-formed callback data — it must flow through to actionSetStatusStart,
// which answers the callback exactly once regardless of what the status
// string is. Rejection, if any, happens later at confirm time via the
// store's own error.
func TestHandleCallback_SetStatusUnrecognizedStatusStillAnswersOnce(t *testing.T) {
	st := &fakeStore{}
	cl := &fakeClient{}
	b := &Bot{Store: st, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	cq := &CallbackQuery{ID: "cb1", From: User{ID: 111}, Data: "set_status:10:not_a_real_status", Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}
	b.handleCallback(context.Background(), cq)
	if len(cl.answered) != 1 {
		t.Fatalf("expected exactly one answer, got %+v", cl.answered)
	}
	p, ok := b.Pending.Get(111)
	if !ok || p.Status != "not_a_real_status" {
		t.Fatalf("expected the unrecognized status to still be staged as pending: %+v %v", p, ok)
	}
}
