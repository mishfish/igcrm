package tgbot

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"igcrm/internal/store"
)

func TestCmdReview_NoPendingSendsEmptyMessage(t *testing.T) {
	st := &fakeStore{}
	cl := &fakeClient{}
	b := &Bot{Store: st, Client: cl, DirectusURL: "", Log: discardLog()}
	b.cmdReview(context.Background(), 9001)
	if len(cl.sent) != 1 || !strings.Contains(cl.sent[0].text, "Немає") {
		t.Fatalf("sent: %+v", cl.sent)
	}
}

func TestCmdReview_SendsOneCardPerPendingContactWithConfirmButton(t *testing.T) {
	st := &fakeStore{needingReview: []int64{1, 2}, contacts: map[int64]store.Contact{
		1: {ID: 1, Values: map[string]string{"full_name": "A"}},
		2: {ID: 2, Values: map[string]string{"full_name": "B"}},
	}}
	cl := &fakeClient{}
	b := &Bot{Store: st, Client: cl, Log: discardLog()}
	b.cmdReview(context.Background(), 9001)
	if len(cl.sent) != 2 {
		t.Fatalf("sent: %+v", cl.sent)
	}
	if cl.sent[0].kb.InlineKeyboard[0][0].CallbackData != "confirm_review:1" {
		t.Errorf("missing confirm button: %+v", cl.sent[0].kb)
	}
	if cl.sent[1].kb.InlineKeyboard[0][0].CallbackData != "confirm_review:2" {
		t.Errorf("missing confirm button on second card: %+v", cl.sent[1].kb)
	}
}

func TestCmdReview_CapsAtFiveWithOverflowNote(t *testing.T) {
	contacts := map[int64]store.Contact{}
	ids := []int64{1, 2, 3, 4, 5, 6, 7}
	for _, id := range ids {
		contacts[id] = store.Contact{ID: id, Values: map[string]string{"full_name": fmt.Sprint("C", id)}}
	}
	st := &fakeStore{needingReview: ids, contacts: contacts}
	cl := &fakeClient{}
	b := &Bot{Store: st, Client: cl, Log: discardLog()}
	b.cmdReview(context.Background(), 9001)
	if len(cl.sent) != 6 { // 5 cards + 1 overflow note
		t.Fatalf("sent: %d messages", len(cl.sent))
	}
	if !strings.Contains(cl.sent[5].text, "і ще 2") {
		t.Errorf("overflow note: %q", cl.sent[5].text)
	}
}

func TestCmdOrders_NoPendingSendsEmptyMessage(t *testing.T) {
	st := &fakeStore{}
	cl := &fakeClient{}
	b := &Bot{Store: st, Client: cl, Log: discardLog()}
	b.cmdOrders(context.Background(), 9001)
	if len(cl.sent) != 1 || !strings.Contains(cl.sent[0].text, "Немає") {
		t.Fatalf("sent: %+v", cl.sent)
	}
}

func TestCmdOrders_SendsSummaryWithCreateTTNButton(t *testing.T) {
	st := &fakeStore{pendingShipment: []int64{10}, botOrders: map[int64]store.BotOrder{10: {ID: 10, Status: "new"}}}
	cl := &fakeClient{}
	b := &Bot{Store: st, Client: cl, Log: discardLog()}
	b.cmdOrders(context.Background(), 9001)
	if len(cl.sent) != 1 || cl.sent[0].kb.InlineKeyboard[0][0].CallbackData != "create_ttn:10" {
		t.Fatalf("sent: %+v", cl.sent)
	}
}

func TestCmdOrders_CapsAtFiveWithOverflowNote(t *testing.T) {
	ids := []int64{1, 2, 3, 4, 5, 6}
	orders := map[int64]store.BotOrder{}
	for _, id := range ids {
		orders[id] = store.BotOrder{ID: id, Status: "new"}
	}
	st := &fakeStore{pendingShipment: ids, botOrders: orders}
	cl := &fakeClient{}
	b := &Bot{Store: st, Client: cl, Log: discardLog()}
	b.cmdOrders(context.Background(), 9001)
	if len(cl.sent) != 6 { // 5 cards + 1 overflow note
		t.Fatalf("sent: %d messages", len(cl.sent))
	}
	if !strings.Contains(cl.sent[5].text, "і ще 1") {
		t.Errorf("overflow note: %q", cl.sent[5].text)
	}
}

func TestHandleMessage_ReviewAndOrdersAreWired(t *testing.T) {
	st := &fakeStore{pendingShipment: []int64{10}, botOrders: map[int64]store.BotOrder{10: {ID: 10, Status: "new"}}}
	cl := &fakeClient{}
	b := &Bot{Store: st, Client: cl, Log: discardLog()}
	b.handleMessage(context.Background(), &Message{Chat: Chat{ID: 9001}, Text: "/orders"})
	if len(cl.sent) != 1 || !strings.Contains(cl.sent[0].text, "Замовлення #10") {
		t.Fatalf("/orders not dispatched: %+v", cl.sent)
	}

	cl.sent = nil
	st.needingReview = []int64{1}
	st.contacts = map[int64]store.Contact{1: {ID: 1}}
	b.handleMessage(context.Background(), &Message{Chat: Chat{ID: 9001}, Text: "/review"})
	if len(cl.sent) != 1 || !strings.Contains(cl.sent[0].text, "Контакт #1") {
		t.Fatalf("/review not dispatched: %+v", cl.sent)
	}
}
