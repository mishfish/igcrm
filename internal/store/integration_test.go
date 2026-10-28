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
	var sizes sql.NullString
	if err := db.QueryRow(`INSERT INTO orders (contact_id, items, amount) VALUES ($1, 'сукня M', 1200)
		RETURNING full_name, phone, np_branch, sizes`, ids[0]).Scan(&name, &phone, &branch, &sizes); err != nil {
		t.Fatal(err)
	}
	if name != "Петренко Олена" || phone != "+380671234567" || branch != "Відділення №45" {
		t.Errorf("order recipient: %q %q %q", name, phone, branch)
	}
	if sizes.String != "M" {
		t.Errorf("order sizes: %q, want M (from card)", sizes.String)
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

func TestOrderForShippingAndSetTTN(t *testing.T) {
	ctx := context.Background()
	st, db := openTest(t)

	var contactID int64
	if err := db.QueryRow(`
		INSERT INTO contacts (igsid, full_name, phone, city, np_branch)
		VALUES ('999', 'Іваненко Іван', '+380671234567', 'Київ', 'Відділення №45')
		RETURNING id`).Scan(&contactID); err != nil {
		t.Fatal(err)
	}
	var orderID int64
	if err := db.QueryRow(`
		INSERT INTO orders (contact_id, amount, payment_type) VALUES ($1, 850, 'cod') RETURNING id`,
		contactID).Scan(&orderID); err != nil {
		t.Fatal(err)
	}

	order, err := st.OrderForShipping(ctx, orderID)
	if err != nil {
		t.Fatal(err)
	}
	if order.FullName != "Іваненко Іван" || order.Phone != "+380671234567" ||
		order.City != "Київ" || order.NPBranch != "Відділення №45" || order.PaymentType != "cod" {
		t.Errorf("order for shipping: %+v", order)
	}
	if order.Amount == nil || *order.Amount != 850 {
		t.Errorf("order amount: %v", order.Amount)
	}
	if order.TTN != nil {
		t.Errorf("fresh order must not have a ttn yet, got %v", order.TTN)
	}
	if order.NPDocRef != nil {
		t.Errorf("fresh order must not have a np_doc_ref yet, got %v", order.NPDocRef)
	}

	if err := st.SetOrderTTN(ctx, orderID, "20450000000001", "doc-ref-1"); err != nil {
		t.Fatal(err)
	}
	order, err = st.OrderForShipping(ctx, orderID)
	if err != nil {
		t.Fatal(err)
	}
	if order.TTN == nil || *order.TTN != "20450000000001" {
		t.Errorf("ttn not saved: %v", order.TTN)
	}
	if order.NPDocRef == nil || *order.NPDocRef != "doc-ref-1" {
		t.Errorf("np_doc_ref not saved: %v", order.NPDocRef)
	}
	var status string
	db.QueryRow(`SELECT status FROM orders WHERE id = $1`, orderID).Scan(&status)
	if status != "shipped" {
		t.Errorf("status = %q, want shipped", status)
	}

	// повторний виклик на вже відправленому замовленні — помилка, без перезапису
	if err := st.SetOrderTTN(ctx, orderID, "20450000000002", "doc-ref-2"); err == nil {
		t.Fatal("expected error when ttn already set")
	}
	db.QueryRow(`SELECT ttn FROM orders WHERE id = $1`, orderID).Scan(&status)
	if status != "20450000000001" {
		t.Errorf("ttn overwritten on second call: %q", status)
	}

	// скасування ТТН прибирає і номер, і внутрішній Ref
	if err := st.ClearOrderTTN(ctx, orderID); err != nil {
		t.Fatal(err)
	}
	order, err = st.OrderForShipping(ctx, orderID)
	if err != nil {
		t.Fatal(err)
	}
	if order.TTN != nil || order.NPDocRef != nil {
		t.Errorf("ttn/np_doc_ref not cleared: ttn=%v doc_ref=%v", order.TTN, order.NPDocRef)
	}

	// повторне скасування вже очищеного замовлення — помилка
	if err := st.ClearOrderTTN(ctx, orderID); err == nil {
		t.Fatal("expected error when order has no ttn to clear")
	}
}

func TestBotUsersOffsetAndAuditLog(t *testing.T) {
	ctx := context.Background()
	st, db := openTest(t)

	if err := st.RegisterBotUser(ctx, 111, 9001, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterBotUser(ctx, 222, 9002, "staff"); err != nil {
		t.Fatal(err)
	}
	// re-registering (e.g. reinstalled Telegram → new chat_id) updates, not duplicates
	if err := st.RegisterBotUser(ctx, 111, 9999, "owner"); err != nil {
		t.Fatal(err)
	}
	users, err := st.BotUsers(ctx)
	if err != nil || len(users) != 2 {
		t.Fatalf("users: %v %v", users, err)
	}

	off, err := st.BotUpdateOffset(ctx)
	if err != nil || off != 0 {
		t.Fatalf("initial offset: %v %v", off, err)
	}
	if err := st.SetBotUpdateOffset(ctx, 42); err != nil {
		t.Fatal(err)
	}
	off, _ = st.BotUpdateOffset(ctx)
	if off != 42 {
		t.Fatalf("offset = %d, want 42", off)
	}

	// bot_actions.contact_id is a real FK → the contact must exist first
	if _, err := db.ExecContext(ctx, `INSERT INTO contacts (id, igsid) VALUES (5, 'bot-test')`); err != nil {
		t.Fatal(err)
	}
	if err := st.LogBotAction(ctx, 111, "confirm_review", intPtr(5), nil, nil); err != nil {
		t.Fatal(err)
	}
	var action string
	var contactID int64
	db.QueryRow(`SELECT action, contact_id FROM bot_actions WHERE tg_user_id = 111`).Scan(&action, &contactID)
	if action != "confirm_review" || contactID != 5 {
		t.Errorf("logged action: %q contact=%d", action, contactID)
	}
}

func TestReviewQueueAndPush(t *testing.T) {
	ctx := context.Background()
	st, db := openTest(t)
	now := time.Now().UTC()

	st.SaveMessage(ctx, store.Message{MID: "r1", IGSID: "321", Direction: "in", Text: "Оленко, 0671234567, Київ, 45", SentAt: now}, 0)
	ids, _ := st.ClaimDue(ctx, 10)
	id := ids[0]
	w := &extract.Worker{Store: st, Model: "fake", History: 30, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		LLM: fakeLLM{`{"full_name":"Оленко","phone":"0671234567","city":"Київ","np_branch":"45"}`}}
	if err := w.Process(ctx, id); err != nil {
		t.Fatal(err)
	}

	needing, err := st.ContactsNeedingReview(ctx, 10)
	if err != nil || len(needing) != 1 || needing[0] != id {
		t.Fatalf("needing review: %v %v", needing, err)
	}
	pending, _ := st.ContactsPendingReviewPush(ctx, 10)
	if len(pending) != 1 || pending[0] != id {
		t.Fatalf("pending push: %v", pending)
	}

	ext, err := st.LastExtraction(ctx, id)
	if err != nil || ext.Changes["full_name"].New != "Оленко" {
		t.Fatalf("last extraction: %+v %v", ext, err)
	}

	if err := st.MarkReviewPushed(ctx, id); err != nil {
		t.Fatal(err)
	}
	pending, _ = st.ContactsPendingReviewPush(ctx, 10)
	if len(pending) != 0 {
		t.Errorf("still pending push after mark: %v", pending)
	}
	needing, _ = st.ContactsNeedingReview(ctx, 10)
	if len(needing) != 1 {
		t.Errorf("marking push must not confirm review: %v", needing)
	}

	// a new round of real changes re-opens the push queue
	w.LLM = fakeLLM{`{"city":"Львів"}`}
	w.Process(ctx, id)
	pending, _ = st.ContactsPendingReviewPush(ctx, 10)
	if len(pending) != 1 {
		t.Errorf("new changes must reset bot_review_pushed_at: %v", pending)
	}

	if err := st.ConfirmReview(ctx, id); err != nil {
		t.Fatal(err)
	}
	needing, _ = st.ContactsNeedingReview(ctx, 10)
	if len(needing) != 0 {
		t.Errorf("confirmed contact still needing review: %v", needing)
	}
	if err := st.ConfirmReview(ctx, id); err == nil {
		t.Fatal("expected error confirming an already-confirmed contact")
	}
	_ = db
}

func TestOrderQueueAndStatus(t *testing.T) {
	ctx := context.Background()
	st, db := openTest(t)

	var contactID int64
	db.QueryRow(`INSERT INTO contacts (igsid, full_name, phone, city, np_branch) VALUES
		('bo1', 'Коваль Ірина', '+380671112233', 'Одеса', 'Відділення №1') RETURNING id`).Scan(&contactID)
	var orderID int64
	db.QueryRow(`INSERT INTO orders (contact_id, items, amount) VALUES ($1, 'сукня', 600) RETURNING id`, contactID).Scan(&orderID)

	pending, err := st.OrdersPendingShipment(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0] != orderID {
		t.Fatalf("pending shipment: %v %v", pending, err)
	}
	pushPending, _ := st.OrdersPendingTTNPush(ctx, 10)
	if len(pushPending) != 1 {
		t.Fatalf("pending ttn push: %v", pushPending)
	}
	if err := st.MarkTTNPushed(ctx, orderID); err != nil {
		t.Fatal(err)
	}
	pushPending, _ = st.OrdersPendingTTNPush(ctx, 10)
	if len(pushPending) != 0 {
		t.Errorf("still pending ttn push after mark: %v", pushPending)
	}
	pending, _ = st.OrdersPendingShipment(ctx, 10)
	if len(pending) != 1 {
		t.Errorf("marking ttn push must not remove from shipment queue: %v", pending)
	}

	bo, err := st.BotOrder(ctx, orderID)
	if err != nil || bo.Items != "сукня" || bo.FullName != "Коваль Ірина" || bo.Status != "new" {
		t.Fatalf("bot order: %+v %v", bo, err)
	}

	if err := st.SetOrderStatus(ctx, orderID, "paid"); err != nil {
		t.Fatal(err)
	}
	bo, _ = st.BotOrder(ctx, orderID)
	if bo.Status != "paid" {
		t.Errorf("status = %q, want paid", bo.Status)
	}
	if err := st.SetOrderStatus(ctx, orderID, "shipped"); err == nil {
		t.Fatal("expected error: shipped must not be settable manually")
	}
	if err := st.SetOrderStatus(ctx, orderID, "bogus"); err == nil {
		t.Fatal("expected error for unknown status")
	}
}

func intPtr(v int64) *int64 { return &v }
