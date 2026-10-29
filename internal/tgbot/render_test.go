package tgbot

import (
	"strings"
	"testing"

	"igcrm/internal/store"
)

func TestRenderContactCard_ShowsFieldsAndDiff(t *testing.T) {
	c := store.Contact{ID: 1, Username: "olena_k", Values: map[string]string{"full_name": "Оленко", "city": "Львів"}}
	text := RenderContactCard(c, map[string]store.Change{"city": {Old: "Київ", New: "Львів"}})
	if !strings.Contains(text, "Оленко") || !strings.Contains(text, "Київ") || !strings.Contains(text, "Львів") {
		t.Errorf("card missing expected content: %s", text)
	}
}

func TestDirectusLink(t *testing.T) {
	if got := DirectusLink("https://crm.example.com/", "contacts", 7); got != "https://crm.example.com/admin/content/contacts/7" {
		t.Errorf("got %s", got)
	}
	if got := DirectusLink("", "contacts", 7); got != "" {
		t.Errorf("empty base must yield empty link, got %s", got)
	}
}

func TestReviewKeyboard_CallbackDataAndOptionalDirectusButton(t *testing.T) {
	kb := ReviewKeyboard(42, "")
	if kb.InlineKeyboard[0][0].CallbackData != "confirm_review:42" {
		t.Errorf("callback data: %+v", kb.InlineKeyboard)
	}
	if len(kb.InlineKeyboard) != 1 {
		t.Errorf("expected no Directus row when directusURL is empty: %+v", kb.InlineKeyboard)
	}
	kb = ReviewKeyboard(42, "https://crm.example.com")
	if len(kb.InlineKeyboard) != 2 || kb.InlineKeyboard[1][0].URL == "" {
		t.Errorf("expected a Directus url button when directusURL is set: %+v", kb.InlineKeyboard)
	}
}

func TestOrderActionsKeyboard_NeverOffersShippedAndHidesCreateTTNWhenAlreadyShipped(t *testing.T) {
	ttn := "20450000000001"
	kb := OrderActionsKeyboard(store.BotOrder{ID: 5, Status: "shipped", TTN: &ttn}, "")
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if strings.Contains(b.CallbackData, "shipped") || strings.Contains(b.CallbackData, "create_ttn:") {
				t.Errorf("unexpected button for shipped order: %+v", b)
			}
		}
	}
}
