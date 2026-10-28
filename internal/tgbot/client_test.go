package tgbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Client{BotToken: "secret-token", BaseURL: srv.URL}
}

func TestGetUpdates_ParsesResult(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/getUpdates") {
			t.Errorf("path = %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []Update{{UpdateID: 7}}})
	})
	updates, err := c.GetUpdates(context.Background(), 1, 1)
	if err != nil || len(updates) != 1 || updates[0].UpdateID != 7 {
		t.Fatalf("updates=%v err=%v", updates, err)
	}
}

func TestSendMessage_ReturnsMessageIDAndIncludesKeyboard(t *testing.T) {
	var gotBody map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 55}})
	})
	kb := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{{Text: "OK", CallbackData: "x"}}}}
	id, err := c.SendMessage(context.Background(), 123, "hi", kb)
	if err != nil || id != 55 {
		t.Fatalf("id=%d err=%v", id, err)
	}
	if gotBody["chat_id"].(float64) != 123 || gotBody["reply_markup"] == nil {
		t.Errorf("body missing chat_id/reply_markup: %v", gotBody)
	}
}

func TestClient_ErrorDoesNotLeakToken(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "description": "Unauthorized"})
	})
	_, err := c.SendMessage(context.Background(), 1, "x", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error leaked bot token: %v", err)
	}
}
