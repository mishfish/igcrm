package tgbot

import (
	"fmt"
	"strconv"
	"strings"

	"igcrm/internal/store"
)

// RenderContactCard formats a contact's current field values, followed by a
// "Змінено:" block for every field in changes (old → new).
func RenderContactCard(c store.Contact, changes map[string]store.Change) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Контакт #%d (@%s)\n", c.ID, c.Username)
	for _, f := range store.Fields {
		fmt.Fprintf(&b, "%s: %s\n", f, c.Values[f])
	}
	if len(changes) > 0 {
		b.WriteString("\nЗмінено:\n")
		for _, f := range store.Fields {
			if ch, ok := changes[f]; ok {
				fmt.Fprintf(&b, "%s: %s → %s\n", f, ch.Old, ch.New)
			}
		}
	}
	return b.String()
}

// RenderOrderSummary formats the order id, status, items, amount, recipient
// and TTN for display in the bot.
func RenderOrderSummary(o store.BotOrder) string {
	amount := "—"
	if o.Amount != nil {
		amount = strconv.FormatFloat(*o.Amount, 'f', 2, 64)
	}
	ttn := "—"
	if o.TTN != nil {
		ttn = *o.TTN
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Замовлення #%d\n", o.ID)
	fmt.Fprintf(&b, "Статус: %s\n", o.Status)
	fmt.Fprintf(&b, "Товари: %s\n", o.Items)
	fmt.Fprintf(&b, "Сума: %s\n", amount)
	fmt.Fprintf(&b, "Отримувач: %s, %s, %s, %s\n", o.FullName, o.Phone, o.City, o.NPBranch)
	fmt.Fprintf(&b, "ТТН: %s\n", ttn)
	return b.String()
}

// DirectusLink returns the admin URL of a record, or "" when baseURL is empty.
func DirectusLink(baseURL, collection string, id int64) string {
	if baseURL == "" {
		return ""
	}
	return strings.TrimRight(baseURL, "/") + "/admin/content/" + collection + "/" + strconv.FormatInt(id, 10)
}

// ReviewKeyboard builds the keyboard for confirming a contact review, with an
// optional row linking to the contact in Directus.
func ReviewKeyboard(contactID int64, directusURL string) *InlineKeyboardMarkup {
	kb := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{{Text: "✅ Підтвердити", CallbackData: fmt.Sprintf("confirm_review:%d", contactID)}},
	}}
	if link := DirectusLink(directusURL, "contacts", contactID); link != "" {
		kb.InlineKeyboard = append(kb.InlineKeyboard, []InlineKeyboardButton{{Text: "🔗 Відкрити в Directus", URL: link}})
	}
	return kb
}

// OrderActionsKeyboard builds the keyboard for an order. The TTN button is
// create or delete depending on whether a TTN already exists. Status buttons
// never include "shipped", which is only set by the shipment flow.
func OrderActionsKeyboard(o store.BotOrder, directusURL string) *InlineKeyboardMarkup {
	kb := &InlineKeyboardMarkup{}
	if o.TTN == nil {
		kb.InlineKeyboard = append(kb.InlineKeyboard, []InlineKeyboardButton{
			{Text: "📦 Створити ТТН", CallbackData: fmt.Sprintf("create_ttn:%d", o.ID)},
		})
	} else {
		kb.InlineKeyboard = append(kb.InlineKeyboard, []InlineKeyboardButton{
			{Text: "🗑 Скасувати ТТН", CallbackData: fmt.Sprintf("delete_ttn:%d", o.ID)},
		})
	}
	var statusRow []InlineKeyboardButton
	for _, s := range []string{"paid", "delivered", "returned", "cancelled"} {
		statusRow = append(statusRow, InlineKeyboardButton{
			Text:         s,
			CallbackData: fmt.Sprintf("set_status:%d:%s", o.ID, s),
		})
	}
	kb.InlineKeyboard = append(kb.InlineKeyboard, statusRow)
	if link := DirectusLink(directusURL, "orders", o.ID); link != "" {
		kb.InlineKeyboard = append(kb.InlineKeyboard, []InlineKeyboardButton{{Text: "🔗 Відкрити в Directus", URL: link}})
	}
	return kb
}
