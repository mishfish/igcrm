package tgbot

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestActionConfirmReview_Success(t *testing.T) {
	st := &fakeStore{}
	cl := &fakeClient{}
	b := &Bot{Store: st, Client: cl, Log: discardLog()}
	cq := &CallbackQuery{ID: "cb1", From: User{ID: 111}, Message: &Message{Chat: Chat{ID: 9001}, MessageID: 55}}
	b.actionConfirmReview(context.Background(), cq, 7)
	if len(cl.edited) != 1 || !strings.Contains(cl.edited[0].text, "Підтверджено") {
		t.Fatalf("edited: %+v", cl.edited)
	}
	if len(st.actions) != 1 || st.actions[0].action != "confirm_review" {
		t.Errorf("audit log: %+v", st.actions)
	}
}

func TestActionConfirmReview_AlreadyConfirmedIsSafeNoop(t *testing.T) {
	st := &fakeStore{confirmErr: map[int64]error{7: errors.New("already confirmed")}}
	cl := &fakeClient{}
	b := &Bot{Store: st, Client: cl, Log: discardLog()}
	cq := &CallbackQuery{ID: "cb1", From: User{ID: 111}, Message: &Message{Chat: Chat{ID: 9001}, MessageID: 55}}
	b.actionConfirmReview(context.Background(), cq, 7)
	if len(st.actions) != 0 {
		t.Errorf("must not log a no-op as an executed action: %+v", st.actions)
	}
	if len(cl.edited) != 0 {
		t.Errorf("must not edit the message on a failed confirm: %+v", cl.edited)
	}
	if len(cl.answered) != 1 {
		t.Fatalf("expected an answer to the callback either way: %+v", cl.answered)
	}
}

func TestHandleCallback_ConfirmReviewDispatchesParsedID(t *testing.T) {
	// contact 42's confirm fails, so the dispatch must reach ConfirmReview(42):
	// no edit, no audit entry, and exactly one answer.
	st := &fakeStore{confirmErr: map[int64]error{42: errors.New("already confirmed")}}
	cl := &fakeClient{}
	b := &Bot{Store: st, Client: cl, Log: discardLog()}
	cq := &CallbackQuery{ID: "cb1", From: User{ID: 111}, Data: "confirm_review:42", Message: &Message{Chat: Chat{ID: 9001}, MessageID: 55}}
	b.handleCallback(context.Background(), cq)
	if len(cl.edited) != 0 || len(st.actions) != 0 {
		t.Fatalf("confirm_review:42 did not dispatch with id 42: edited=%+v actions=%+v", cl.edited, st.actions)
	}
	if len(cl.answered) != 1 {
		t.Fatalf("expected exactly one answer: %+v", cl.answered)
	}

	// Control: a different id with no error succeeds and edits the message.
	st2 := &fakeStore{}
	cl2 := &fakeClient{}
	b2 := &Bot{Store: st2, Client: cl2, Log: discardLog()}
	cq2 := &CallbackQuery{ID: "cb2", From: User{ID: 111}, Data: "confirm_review:7", Message: &Message{Chat: Chat{ID: 9001}, MessageID: 55}}
	b2.handleCallback(context.Background(), cq2)
	if len(cl2.edited) != 1 || len(st2.actions) != 1 {
		t.Fatalf("confirm_review:7 should succeed: edited=%+v actions=%+v", cl2.edited, st2.actions)
	}
}

func TestHandleCallback_ConfirmReviewMalformedIDAnswersOnce(t *testing.T) {
	for _, data := range []string{"confirm_review", "confirm_review:", "confirm_review:bad"} {
		t.Run(data, func(t *testing.T) {
			st := &fakeStore{}
			cl := &fakeClient{}
			b := &Bot{Store: st, Client: cl, Log: discardLog()}
			cq := &CallbackQuery{ID: "cb1", From: User{ID: 111}, Data: data, Message: &Message{Chat: Chat{ID: 9001}, MessageID: 55}}
			b.handleCallback(context.Background(), cq)
			if len(cl.edited) != 0 || len(st.actions) != 0 {
				t.Errorf("malformed data must not act: edited=%+v actions=%+v", cl.edited, st.actions)
			}
			if len(cl.answered) != 1 {
				t.Errorf("expected exactly one answer, got %+v", cl.answered)
			}
		})
	}
}
