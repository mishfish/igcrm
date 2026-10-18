package extract

import (
	"encoding/json"
	"testing"
)

func TestNormalizePhone(t *testing.T) {
	cases := map[string]string{
		"067 123 45 67":      "+380671234567",
		"+38 (067) 123-4567": "+380671234567",
		"380671234567":       "+380671234567",
		"671234567":          "671234567", // без коду країни чи провідного 0 — неоднозначно, лишаємо як є
		"+48 600 700 800":    "+48600700800",
		"дзвоніть увечері":   "дзвоніть увечері",
	}
	for in, want := range cases {
		if got := NormalizePhone(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeBranch(t *testing.T) {
	cases := map[string]string{
		"45":                        "Відділення №45",
		"відділення 45":             "Відділення №45",
		"Відділення №45":            "Відділення №45",
		"відд. №7":                  "Відділення №7",
		"поштомат 1234":             "Поштомат №1234",
		"Поштомат №1234":            "Поштомат №1234",
		"вул. Шевченка 5, до 30 кг": "вул. Шевченка 5, до 30 кг",
		"null": "",
	}
	for in, want := range cases {
		if got := Normalize("np_branch", in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

func TestDiff(t *testing.T) {
	current := map[string]string{"city": "Київ", "phone": "+380671234567", "sizes": "M"}
	result := json.RawMessage(`{
		"full_name": "Петренко Олена",
		"phone": "067 123 45 67",
		"city": "Львів",
		"np_branch": "12",
		"sizes": null,
		"last_interest": "",
		"unknown_field": "x"
	}`)
	ch, err := Diff(current, result)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ch["phone"]; ok {
		t.Error("same phone in other format must not be a change")
	}
	if _, ok := ch["sizes"]; ok {
		t.Error("null must not wipe existing value")
	}
	if c := ch["city"]; c.Old != "Київ" || c.New != "Львів" {
		t.Errorf("city change: %+v", c)
	}
	if ch["np_branch"].New != "Відділення №12" || ch["full_name"].New != "Петренко Олена" {
		t.Errorf("unexpected: %+v", ch)
	}
	if len(ch) != 3 {
		t.Errorf("want 3 changes, got %+v", ch)
	}
}

func TestOrderSignal(t *testing.T) {
	ready, amount, pt := OrderSignal(json.RawMessage(`{"order_ready": true, "order_amount": 850, "payment_type": "partial_prepay"}`))
	if !ready || amount == nil || *amount != 850 || pt != "partial_prepay" {
		t.Errorf("ready=%v amount=%v payment_type=%q", ready, amount, pt)
	}
	ready, amount, pt = OrderSignal(json.RawMessage(`{"order_ready": false, "order_amount": null, "payment_type": null}`))
	if ready || amount != nil || pt != "" {
		t.Errorf("ready=%v amount=%v payment_type=%q", ready, amount, pt)
	}
	ready, amount, pt = OrderSignal(json.RawMessage(`{}`))
	if ready || amount != nil || pt != "" {
		t.Errorf("missing fields: ready=%v amount=%v payment_type=%q", ready, amount, pt)
	}
	// значення поза білим списком (CHECK у БД) ігнорується, а не передається далі
	_, _, pt = OrderSignal(json.RawMessage(`{"payment_type": "crypto"}`))
	if pt != "" {
		t.Errorf("unexpected payment_type passthrough: %q", pt)
	}
}

func TestExtractJSONObject(t *testing.T) {
	raw, err := extractJSONObject("Ось результат:\n```json\n{\"city\": \"Одеса\"}\n```")
	if err != nil || string(raw) != `{"city": "Одеса"}` {
		t.Fatalf("%s %v", raw, err)
	}
	if _, err := extractJSONObject("немає json"); err == nil {
		t.Fatal("expected error")
	}
}
