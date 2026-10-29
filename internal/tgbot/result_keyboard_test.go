package tgbot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"igcrm/internal/store"
)

// botOrderErrStore makes BotOrder fail while delegating everything else to the shared fakeStore.
type botOrderErrStore struct{ *fakeStore }

func (s botOrderErrStore) BotOrder(ctx context.Context, id int64) (store.BotOrder, error) {
	return store.BotOrder{}, errors.New("db down")
}

func hasCallback(kb *InlineKeyboardMarkup, prefix string) bool {
	if kb == nil {
		return false
	}
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if strings.HasPrefix(btn.CallbackData, prefix) {
				return true
			}
		}
	}
	return false
}

func TestCreateTTN_SuccessResultCarriesOrderActionKeyboard(t *testing.T) {
	ttn := "20450000000001"
	st := &fakeStore{botOrders: map[int64]store.BotOrder{10: {ID: 10, Status: "paid", TTN: &ttn}}}
	cl := &fakeClient{}
	b := &Bot{Store: st, Shipping: &fakeShipper{createTTN: ttn}, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	b.Pending.Set(111, PendingAction{Kind: "create_ttn", OrderID: 10, Step: "confirm", PromptMsgID: 1, Weight: 1.2, CreatedAt: time.Now()})
	b.actionCreateTTNConfirm(context.Background(), &CallbackQuery{ID: "cb", From: User{ID: 111}, Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}, 10)
	if len(cl.edited) != 1 || !strings.Contains(cl.edited[0].text, "ТТН створено") {
		t.Fatalf("edited: %+v", cl.edited)
	}
	kb := cl.edited[0].kb
	if !hasCallback(kb, "delete_ttn:10") || !hasCallback(kb, "set_status:10:") || hasCallback(kb, "create_ttn:") {
		t.Errorf("unexpected keyboard: %+v", kb)
	}
	if len(cl.answered) != 1 || len(st.actions) != 1 {
		t.Errorf("answered=%d actions=%d", len(cl.answered), len(st.actions))
	}
}

func TestCreateTTN_SuccessBotOrderErrorEditsWithoutKeyboard(t *testing.T) {
	st := &fakeStore{}
	cl := &fakeClient{}
	b := &Bot{Store: botOrderErrStore{st}, Shipping: &fakeShipper{createTTN: "t1"}, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	b.Pending.Set(111, PendingAction{Kind: "create_ttn", OrderID: 10, Step: "confirm", PromptMsgID: 1, Weight: 1.2, CreatedAt: time.Now()})
	b.actionCreateTTNConfirm(context.Background(), &CallbackQuery{ID: "cb", From: User{ID: 111}, Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}, 10)
	if len(cl.edited) != 1 || cl.edited[0].kb != nil || !strings.Contains(cl.edited[0].text, "ТТН створено: t1") {
		t.Fatalf("edited: %+v", cl.edited)
	}
	if len(st.actions) != 1 || st.actions[0].action != "create_ttn" || len(cl.answered) != 1 {
		t.Errorf("actions=%+v answered=%+v", st.actions, cl.answered)
	}
}

func TestSetStatus_SuccessResultCarriesOrderActionKeyboard(t *testing.T) {
	ttn := "20450000000001"
	st := &fakeStore{botOrders: map[int64]store.BotOrder{10: {ID: 10, Status: "paid", TTN: &ttn}}}
	cl := &fakeClient{}
	b := &Bot{Store: st, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	b.Pending.Set(111, PendingAction{Kind: "set_status", OrderID: 10, Status: "paid", Step: "confirm", PromptMsgID: 1, CreatedAt: time.Now()})
	b.actionSetStatusConfirm(context.Background(), &CallbackQuery{ID: "cb", From: User{ID: 111}, Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}, 10)
	if len(cl.edited) != 1 {
		t.Fatalf("edited: %+v", cl.edited)
	}
	kb := cl.edited[0].kb
	if !hasCallback(kb, "delete_ttn:10") || !hasCallback(kb, "set_status:10:delivered") {
		t.Errorf("unexpected keyboard: %+v", kb)
	}
	if len(cl.answered) != 1 || len(st.actions) != 1 {
		t.Errorf("answered=%d actions=%d", len(cl.answered), len(st.actions))
	}
}

func TestSetStatus_SuccessBotOrderErrorEditsWithoutKeyboard(t *testing.T) {
	st := &fakeStore{}
	cl := &fakeClient{}
	b := &Bot{Store: botOrderErrStore{st}, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	b.Pending.Set(111, PendingAction{Kind: "set_status", OrderID: 10, Status: "paid", Step: "confirm", PromptMsgID: 1, CreatedAt: time.Now()})
	b.actionSetStatusConfirm(context.Background(), &CallbackQuery{ID: "cb", From: User{ID: 111}, Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}, 10)
	if len(cl.edited) != 1 || cl.edited[0].kb != nil {
		t.Fatalf("edited: %+v", cl.edited)
	}
	if len(st.actions) != 1 || st.actions[0].action != "set_status" || len(cl.answered) != 1 {
		t.Errorf("actions=%+v answered=%+v", st.actions, cl.answered)
	}
}
