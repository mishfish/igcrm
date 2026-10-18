package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"igcrm/internal/extract"
	"igcrm/internal/store"
)

// Запуск: TEST_DATABASE_URL=postgres://... go test ./internal/store/
func openTest(t *testing.T) (*store.Store, *sql.DB) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	raw, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(context.Background()); err != nil { // повторно — no-op
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close(); raw.Close() })
	return st, raw
}

type fakeLLM struct{ out string }

func (f fakeLLM) Complete(context.Context, string, string) (json.RawMessage, error) {
	return json.RawMessage(f.out), nil
}

type fakeNotifier struct{ alerts []string }

func (n *fakeNotifier) Alert(_ context.Context, text string) {
	n.alerts = append(n.alerts, text)
}

func TestFlow(t *testing.T) {
	ctx := context.Background()
	st, db := openTest(t)
	now := time.Now().UTC()

	msgs := []store.Message{
		{MID: "m1", IGSID: "555", Direction: "in", Text: "Хочу сукню розмір M", SentAt: now.Add(-3 * time.Minute)},
		{MID: "m2", IGSID: "555", Direction: "out", Text: "Куди відправити?", SentAt: now.Add(-2 * time.Minute)},
		{MID: "m3", IGSID: "555", Direction: "in", Text: "Петренко Олена, 0671234567, Київ, 45", SentAt: now.Add(-time.Minute)},
	}
	for _, m := range msgs {
		ok, err := st.SaveMessage(ctx, m, 0)
		if err != nil || !ok {
			t.Fatalf("save %s: %v %v", m.MID, ok, err)
		}
	}
	// ретрай від Meta — дубль не вставляється
	if ok, err := st.SaveMessage(ctx, msgs[2], 0); err != nil || ok {
		t.Fatalf("duplicate inserted: %v %v", ok, err)
	}

	ids, err := st.ClaimDue(ctx, 10)
	if err != nil || len(ids) != 1 {
		t.Fatalf("claim: %v %v", ids, err)
	}
	if again, _ := st.ClaimDue(ctx, 10); len(again) != 0 {
		t.Fatal("claimed twice")
	}

	w := &extract.Worker{
		Store: st, Model: "fake", History: 30, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		LLM: fakeLLM{`{"full_name":"Петренко Олена","phone":"067 123 45 67","city":"Київ","np_branch":"45","sizes":"M","last_interest":"сукня"}`},
	}
	if err := w.Process(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	c, err := st.Contact(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"full_name": "Петренко Олена", "phone": "+380671234567", "city": "Київ",
		"np_branch": "Відділення №45", "sizes": "M", "last_interest": "сукня"}
	for f, v := range want {
		if c.Values[f] != v {
			t.Errorf("%s = %q, want %q", f, c.Values[f], v)
		}
	}
	var review bool
	db.QueryRow(`SELECT needs_review FROM contacts WHERE id = $1`, ids[0]).Scan(&review)
	if !review {
		t.Error("needs_review not set")
	}

	// ручне блокування: auto_update=false → поля не чіпаються
	db.Exec(`UPDATE contacts SET auto_update = false, needs_review = false WHERE id = $1`, ids[0])
	w.LLM = fakeLLM{`{"city":"Львів"}`}
	if err := w.Process(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	c, _ = st.Contact(ctx, ids[0])
	if c.Values["city"] != "Київ" {
		t.Errorf("locked contact overwritten: %q", c.Values["city"])
	}

	// замовлення підтягує отримувача з картки
	var name, phone, branch string
	if err := db.QueryRow(`INSERT INTO orders (contact_id, items, amount) VALUES ($1, 'сукня M', 1200)
		RETURNING full_name, phone, np_branch`, ids[0]).Scan(&name, &phone, &branch); err != nil {
		t.Fatal(err)
	}
	if name != "Петренко Олена" || phone != "+380671234567" || branch != "Відділення №45" {
		t.Errorf("order recipient: %q %q %q", name, phone, branch)
	}

	// токен: seed не перезаписує існуючий, force — перезаписує
	st.SeedToken(ctx, "tok1", false)
	st.SeedToken(ctx, "tok2", false)
	tok, _ := st.Token(ctx)
	if tok.AccessToken != "tok1" {
		t.Errorf("seed overwrote token: %q", tok.AccessToken)
	}
	st.SeedToken(ctx, "tok3", true)
	tok, _ = st.Token(ctx)
	if tok.AccessToken != "tok3" || tok.ExpiresAt != nil {
		t.Errorf("force seed: %+v", tok)
	}
	exp := time.Now().Add(60 * 24 * time.Hour)
	st.SaveToken(ctx, "tok4", exp)
	tok, _ = st.Token(ctx)
	if tok.AccessToken != "tok4" || tok.ExpiresAt == nil {
		t.Errorf("save token: %+v", tok)
	}

	var extractions int
	db.QueryRow(`SELECT count(*) FROM extractions`).Scan(&extractions)
	if extractions != 1 { // заблокований контакт у LLM не відправляється
		t.Errorf("extractions logged: %d", extractions)
	}
}

func TestOrderAutoCreate(t *testing.T) {
	ctx := context.Background()
	st, db := openTest(t)
	now := time.Now().UTC()

	if ok, err := st.SaveMessage(ctx, store.Message{
		MID: "o1", IGSID: "777", Direction: "in", Text: "Беру шаровари M, оформляйте", SentAt: now,
	}, 0); err != nil || !ok {
		t.Fatalf("save: %v %v", ok, err)
	}
	ids, err := st.ClaimDue(ctx, 10)
	if err != nil || len(ids) != 1 {
		t.Fatalf("claim: %v %v", ids, err)
	}
	id := ids[0]

	notifier := &fakeNotifier{}
	w := &extract.Worker{
		Store: st, Model: "fake", History: 30, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		LLM:    fakeLLM{`{"last_interest":"шаровари M","order_ready":true,"order_amount":850,"payment_type":"partial_prepay"}`},
		Notify: notifier,
	}
	if err := w.Process(ctx, id); err != nil {
		t.Fatal(err)
	}
	if len(notifier.alerts) != 1 {
		t.Errorf("want 1 alert, got %v", notifier.alerts)
	}

	var count int
	var items string
	var amount float64
	var autoCreated bool
	var status, paymentType string
	db.QueryRow(`SELECT count(*) FROM orders WHERE contact_id = $1`, id).Scan(&count)
	if count != 1 {
		t.Fatalf("want 1 order, got %d", count)
	}
	db.QueryRow(`SELECT items, amount, auto_created, status, payment_type FROM orders WHERE contact_id = $1`, id).
		Scan(&items, &amount, &autoCreated, &status, &paymentType)
	if items != "шаровари M" || amount != 850 || !autoCreated || status != "new" || paymentType != "partial_prepay" {
		t.Errorf("order: items=%q amount=%v auto=%v status=%q payment_type=%q", items, amount, autoCreated, status, paymentType)
	}

	// той самий сигнал на наступному debounce-циклі не плодить дублікат
	if err := w.Process(ctx, id); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT count(*) FROM orders WHERE contact_id = $1`, id).Scan(&count)
	if count != 1 {
		t.Errorf("duplicate order created: count=%d", count)
	}

	// замовлення просунулось далі (paid) → новий order_ready створює нове замовлення
	db.Exec(`UPDATE orders SET status = 'paid' WHERE contact_id = $1`, id)
	w.LLM = fakeLLM{`{"last_interest":"футболка L","order_ready":true,"order_amount":500}`}
	if err := w.Process(ctx, id); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT count(*) FROM orders WHERE contact_id = $1`, id).Scan(&count)
	if count != 2 {
		t.Errorf("want 2 orders after previous one was paid, got %d", count)
	}
}
