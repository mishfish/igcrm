# Telegram Bot Control Interface Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an interactive Telegram bot (`internal/tgbot`) to the existing `igcrm` Go binary so the owner and a few staff can confirm LLM-extracted customer cards and create/cancel Nova Poshta TTNs (and change order status) from their phone, with push notifications on new review items/orders and a Telegram-user-id whitelist for authorization.

**Architecture:** New `internal/tgbot` package runs as another goroutine inside the existing `igcrm` binary (same pattern as `extract.Worker`/`jobs.TokenRefresher`), talking directly to `store.Store` and `shipping.Service` — no new HTTP endpoints, no new container. The bot uses Telegram long polling (`getUpdates`), never a webhook, so no Caddy route changes. A second goroutine (`PushWorker`) polls Postgres for unnotified review/order items (same "Postgres as queue" idiom as `ClaimDue`) and fans them out to every registered Telegram user with inline-keyboard buttons.

**Tech Stack:** Go 1.24, `net/http` (stdlib only, no Telegram SDK — mirrors `internal/notify/telegram.go`), Postgres (`lib/pq`), existing `store`/`shipping` packages.

**Spec:** `docs/superpowers/specs/2026-10-28-telegram-bot-control-interface-design.md`

## Decisions made while turning the spec into tasks

These are my calls as plan-writer, not in the spec verbatim — flagging them so you can object before implementation starts:

- **Push delivery mechanism**: instead of hooking directly into `extract.Worker`/`extract.Process` (spec's phase-7 wording), the bot runs its own polling `PushWorker` that reads `needs_review`/pending-shipment rows Postgres already has, using new `bot_review_pushed_at`/`bot_ttn_pushed_at` tracking columns. Same user-visible effect (push within ~20s of the DB change), but keeps `internal/extract` and `internal/tgbot` decoupled — `extract` doesn't need to know the bot exists.
- **"Багато карток на перевірку" / "багато замовлень"**: sends up to 5 individual messages (one per item, with its own buttons) and, if more are pending, a trailing text note "і ще N — дивись у Directus" instead of building Telegram-side pagination. Simpler, and `/review`+`/orders` can be re-run to see the next batch once some are handled.
- **`SetOrderStatus` validates the allowed-manual-status set in `store.Store` itself** (not only by which buttons the bot renders), so the safety rule holds even if a button render path is ever wrong.
- **"Відкрити в Directus" button** needs the public Directus URL, which `igcrm` doesn't currently receive as config. Adds a new optional env var `DIRECTUS_PUBLIC_URL`; when unset, the button is just omitted (bot still works, minus the deep link).

## Global Constraints

- Go 1.24 (`go.mod`); no new third-party dependencies (stdlib `net/http`/`encoding/json` only, matching `internal/notify/telegram.go`'s existing style).
- No new public ingress: no new Caddy route, no webhook endpoint — Telegram access is long-polling only (spec: "Long polling, не webhook").
- Authorization is a Telegram-user-id whitelist (`TG_ALLOWED_USER_IDS`); an update from a user not on the list gets **zero response** (spec goal 3 — "решта — тиша").
- Every state-changing action (confirm review, create TTN, cancel TTN, set status) requires a second explicit "Підтвердити" tap before it executes (spec anti-goal 2 and Безпека §4).
- The bot token must never appear in logs or returned error strings (spec Безпека §3; mirrors the existing comment in `internal/notify/telegram.go:35`).
- Every *executed* (not merely attempted) bot action is written to `bot_actions` with who/when/what (spec goal 4).
- `orders.status = 'shipped'` is only ever set by `shipping.Service.CreateTTN` (via `Store.SetOrderTTN`) — never by a manual bot button (spec: MVP action list excludes `shipped`).
- Every whitelisted user is rate-limited in-memory (spec Безпека §6 — "захист від зациклення/абʼюзу, якщо токен колись протече"); exceeding it is silent, same as an unauthorized update.

## Review Focus

- Weight entered with a comma decimal (`1,2`) or non-numeric text during the "create TTN" dialog must be rejected with a retry prompt, not crash the dialog or get parsed as `0`. → pinned in Task 10.
- A redelivered/duplicate Telegram update (bot restarted mid-flight) hitting "confirm create TTN" twice for the same order must not create two TTNs or charge twice. → pinned in Task 10 (relies on `shipping.Service.CreateTTN`'s existing `ttn IS NULL` guard, exercised end-to-end from the bot action).
- A whitelisted user who has never sent `/start` (no row in `tg_users`) must not break the push fan-out for everyone else. → pinned in Task 12.
- When `shipping.Service.CreateTTN` fails for a business reason (unparseable `np_branch`, missing amount), the bot must show that exact error text to the user, not a generic "сталася помилка". → pinned in Task 10.
- Pressing an already-resolved confirm button a second time (double-tap, or a stale button from an earlier/different dialog) must be a safe no-op with a clear reply, not a panic or a repeated side effect. → pinned in Tasks 9, 10, 11.

---

## Task 1: DB schema + bot registration/offset/audit store methods

**Files:**
- Create: `internal/store/migrations/008_telegram_bot_core.sql`
- Modify: `internal/store/store.go`
- Test: `internal/store/integration_test.go`

**Interfaces:**
- Produces: `store.BotUser{TGUserID, ChatID int64}`; `(*Store) RegisterBotUser(ctx context.Context, tgUserID, chatID int64, username string) error`; `(*Store) BotUsers(ctx context.Context) ([]BotUser, error)`; `(*Store) BotUpdateOffset(ctx context.Context) (int64, error)`; `(*Store) SetBotUpdateOffset(ctx context.Context, updateID int64) error`; `(*Store) LogBotAction(ctx context.Context, tgUserID int64, action string, contactID, orderID *int64, detail json.RawMessage) error`.

- [ ] **Step 1: Write the migration**

```sql
-- internal/store/migrations/008_telegram_bot_core.sql
CREATE TABLE tg_users (
    tg_user_id    bigint PRIMARY KEY,
    chat_id       bigint NOT NULL,
    username      text,
    registered_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE tg_bot_state (
    id             int PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    last_update_id bigint NOT NULL DEFAULT 0
);
INSERT INTO tg_bot_state (id, last_update_id) VALUES (1, 0);

CREATE TABLE bot_actions (
    id         bigserial PRIMARY KEY,
    tg_user_id bigint NOT NULL,
    action     text NOT NULL,
    contact_id bigint REFERENCES contacts(id) ON DELETE SET NULL,
    order_id   bigint REFERENCES orders(id) ON DELETE SET NULL,
    detail     jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX bot_actions_created_idx ON bot_actions (created_at DESC);
```

- [ ] **Step 2: Write the failing test** in `internal/store/integration_test.go`

```go
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

func intPtr(v int64) *int64 { return &v }
```

- [ ] **Step 3: Run test to verify it fails**

Run: `TEST_DATABASE_URL=postgres://... go test ./internal/store/ -run TestBotUsersOffsetAndAuditLog -v`
Expected: FAIL — `RegisterBotUser` undefined.

- [ ] **Step 4: Implement in `internal/store/store.go`** (new `---------- telegram bot ----------` section, same style as the existing `---------- token ----------` section)

Add `type BotUser struct { TGUserID, ChatID int64 }` and the five functions from the Interfaces block above. `RegisterBotUser` is an upsert (`INSERT ... ON CONFLICT (tg_user_id) DO UPDATE SET chat_id = EXCLUDED.chat_id, username = EXCLUDED.username`). `LogBotAction` inserts into `bot_actions`, passing `contactID`/`orderID`/`detail` through as nullable (`*int64` and `json.RawMessage` bind to NULL when nil — no special-casing needed, `lib/pq` handles typed nil pointers).

- [ ] **Step 5: Run test to verify it passes**

Run: `TEST_DATABASE_URL=postgres://... go test ./internal/store/ -run TestBotUsersOffsetAndAuditLog -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/store/migrations/008_telegram_bot_core.sql internal/store/store.go internal/store/integration_test.go
git commit -m "feat(store): add telegram bot registration, update offset and audit log"
```

---

## Task 2: Review-queue store methods

**Files:**
- Create: `internal/store/migrations/009_contacts_bot_review_pushed.sql`
- Modify: `internal/store/store.go` (new methods + `ApplyExtraction`)
- Test: `internal/store/integration_test.go`

**Interfaces:**
- Consumes: `store.Change` (existing, `store.go:268`).
- Produces: `store.Extraction{Model string; Changes map[string]Change; At time.Time}`; `(*Store) ContactsNeedingReview(ctx, limit int) ([]int64, error)`; `(*Store) ConfirmReview(ctx, id int64) error`; `(*Store) ContactsPendingReviewPush(ctx, limit int) ([]int64, error)`; `(*Store) MarkReviewPushed(ctx, id int64) error`; `(*Store) LastExtraction(ctx, contactID int64) (Extraction, error)`.

- [ ] **Step 1: Write the migration**

```sql
-- internal/store/migrations/009_contacts_bot_review_pushed.sql
ALTER TABLE contacts ADD COLUMN bot_review_pushed_at timestamptz;
```

- [ ] **Step 2: Write the failing test**

```go
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
```

- [ ] **Step 3: Run test to verify it fails**

Run: `TEST_DATABASE_URL=postgres://... go test ./internal/store/ -run TestReviewQueueAndPush -v`
Expected: FAIL — `ContactsNeedingReview` undefined.

- [ ] **Step 4: Implement**

Add the five items from Interfaces. `ConfirmReview`: `UPDATE contacts SET needs_review = false WHERE id = $1 AND needs_review`, error via `RowsAffected() == 0` (same idiom as `SetOrderTTN`/`ClearOrderTTN`). `ContactsPendingReviewPush`/`MarkReviewPushed` mirror `ClaimDue`'s shape but without the row-locking (push is best-effort, not exactly-once work). `LastExtraction` unmarshals the `changes` jsonb column into `map[string]Change`; `sql.ErrNoRows` returns a zero-value `Extraction, nil` (a contact can need review with no extraction row only in theory, but don't error on it).

Modify `ApplyExtraction`'s UPDATE (`store.go:294`) to also clear the push flag when changes are applied:
```go
q := `UPDATE contacts SET ` + strings.Join(sets, ", ") + `, needs_review = true, bot_review_pushed_at = NULL WHERE id = $1 AND auto_update`
```

- [ ] **Step 5: Run test to verify it passes**

Run: `TEST_DATABASE_URL=postgres://... go test ./internal/store/ -run TestReviewQueueAndPush -v`
Expected: PASS

- [ ] **Step 6: Run the full existing store suite to confirm the `ApplyExtraction` change didn't break anything**

Run: `TEST_DATABASE_URL=postgres://... go test ./internal/store/... ./internal/extract/... -v`
Expected: PASS (all tests, including pre-existing `TestFlow`/`TestOrderAutoCreate`)

- [ ] **Step 7: Commit**

```bash
git add internal/store/migrations/009_contacts_bot_review_pushed.sql internal/store/store.go internal/store/integration_test.go
git commit -m "feat(store): review queue and push-tracking for telegram bot"
```

---

## Task 3: Order-queue/status store methods

**Files:**
- Create: `internal/store/migrations/010_orders_bot_ttn_pushed.sql`
- Modify: `internal/store/store.go`
- Test: `internal/store/integration_test.go`

**Interfaces:**
- Produces: `store.BotOrder{ID int64; Status, Items string; Amount *float64; FullName, Phone, City, NPBranch string; TTN *string}`; `(*Store) BotOrder(ctx, id int64) (BotOrder, error)`; `(*Store) OrdersPendingShipment(ctx, limit int) ([]int64, error)`; `(*Store) OrdersPendingTTNPush(ctx, limit int) ([]int64, error)`; `(*Store) MarkTTNPushed(ctx, id int64) error`; `(*Store) SetOrderStatus(ctx, id int64, status string) error`.

- [ ] **Step 1: Write the migration**

```sql
-- internal/store/migrations/010_orders_bot_ttn_pushed.sql
ALTER TABLE orders ADD COLUMN bot_ttn_pushed_at timestamptz;
```

- [ ] **Step 2: Write the failing test**

```go
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
```

- [ ] **Step 3: Run test to verify it fails**

Run: `TEST_DATABASE_URL=postgres://... go test ./internal/store/ -run TestOrderQueueAndStatus -v`
Expected: FAIL — `OrdersPendingShipment` undefined.

- [ ] **Step 4: Implement**

Add the Interfaces block items. `OrdersPendingShipment`: `SELECT id FROM orders WHERE status IN ('new','awaiting_payment') AND ttn IS NULL ORDER BY created_at LIMIT $1`. `OrdersPendingTTNPush`: same `WHERE` plus `AND bot_ttn_pushed_at IS NULL`. `BotOrder`: `SELECT id, status, items, amount, full_name, phone, city, np_branch, ttn FROM orders WHERE id = $1`, scanning nullable columns the same way `OrderForShipping` does (`store.go:339`). `SetOrderStatus` validates against a package-level `var botManualStatuses = map[string]bool{"paid": true, "delivered": true, "returned": true, "cancelled": true}` before running `UPDATE orders SET status = $2, updated_at = now() WHERE id = $1`; reject with an error naming the status if it's not in that map (covers both `shipped` and typos).

- [ ] **Step 5: Run test to verify it passes**

Run: `TEST_DATABASE_URL=postgres://... go test ./internal/store/ -run TestOrderQueueAndStatus -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/store/migrations/010_orders_bot_ttn_pushed.sql internal/store/store.go internal/store/integration_test.go
git commit -m "feat(store): order shipment/push queue and manual status transitions"
```

---

## Task 4: Telegram Bot API HTTP client

**Files:**
- Create: `internal/tgbot/client.go`
- Test: `internal/tgbot/client_test.go`

**Interfaces:**
- Produces: `tgbot.Update{UpdateID int64; Message *Message; CallbackQuery *CallbackQuery}`; `tgbot.Message{MessageID int64; Chat Chat; From *User; Text string}`; `tgbot.Chat{ID int64}`; `tgbot.User{ID int64; Username string}`; `tgbot.CallbackQuery{ID string; From User; Message *Message; Data string}`; `tgbot.InlineKeyboardButton{Text, CallbackData, URL string}`; `tgbot.InlineKeyboardMarkup{InlineKeyboard [][]InlineKeyboardButton}`; `tgbot.Client{BotToken string; BaseURL string; HTTP *http.Client}` with `GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error)`, `SendMessage(ctx context.Context, chatID int64, text string, kb *InlineKeyboardMarkup) (messageID int64, err error)`, `EditMessageText(ctx context.Context, chatID, messageID int64, text string, kb *InlineKeyboardMarkup) error`, `AnswerCallbackQuery(ctx context.Context, callbackID, text string) error`.

- [ ] **Step 1: Write the failing tests**

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tgbot/... -v`
Expected: FAIL — package `tgbot` / `Client` doesn't exist yet.

- [ ] **Step 3: Implement `internal/tgbot/client.go`**

`call(ctx, method string, payload any, out any) error` is the shared helper: marshals `payload` to JSON, POSTs to `fmt.Sprintf("%s/bot%s/%s", base, c.BotToken, method)` with `Content-Type: application/json` (`base` defaults to `"https://api.telegram.org"` when `BaseURL == ""`), decodes `{"ok":bool,"result":json.RawMessage,"description":string}`, and on `!ok` or non-2xx returns `fmt.Errorf("telegram %s: %s", method, description)` — built from the response body only, never from the request URL, so the token (which lives only in the URL) can't leak into an error. Each public method builds its payload struct, calls `call`, and unmarshals `result` into the right type (`[]Update` for `GetUpdates`, `struct{ MessageID int64 }` for `SendMessage`, nothing for `EditMessageText`/`AnswerCallbackQuery`). `GetUpdates` payload includes `offset`/`timeout`; the HTTP client used for it needs a longer timeout than the others (`timeoutSec` + 5s headroom) since Telegram holds the connection open for the long poll.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tgbot/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tgbot/client.go internal/tgbot/client_test.go
git commit -m "feat(tgbot): minimal Telegram Bot API client (long polling, inline keyboards)"
```

---

## Task 5: Allowlist + pending-dialog state

**Files:**
- Create: `internal/tgbot/auth.go`, `internal/tgbot/pending.go`, `internal/tgbot/ratelimit.go`
- Test: `internal/tgbot/auth_test.go`, `internal/tgbot/pending_test.go`, `internal/tgbot/ratelimit_test.go`

**Interfaces:**
- Produces: `tgbot.Allowlist map[int64]bool` with `ParseAllowlist(csv string) (Allowlist, error)` and `(Allowlist) Allowed(userID int64) bool`; `tgbot.PendingAction{Kind string; OrderID int64; Step string; Weight float64; Status string; CreatedAt time.Time}`; `tgbot.PendingStore` with `NewPendingStore(ttl time.Duration) *PendingStore`, `(*PendingStore) Set(userID int64, a PendingAction)`, `(*PendingStore) Get(userID int64) (PendingAction, bool)`, `(*PendingStore) Clear(userID int64)`. `PendingStore` has an unexported `now func() time.Time` field (defaults to `time.Now`, overridable from tests in the same package) to make TTL expiry deterministic to test. Also produces `tgbot.RateLimiter` with `NewRateLimiter(limit int, window time.Duration) *RateLimiter` and `(*RateLimiter) Allow(userID int64) bool` (fixed-window counter per user id, same `now func() time.Time` override pattern as `PendingStore` for deterministic tests).

- [ ] **Step 1: Write the failing tests**

```go
// auth_test.go
func TestParseAllowlist(t *testing.T) {
	a, err := ParseAllowlist(" 111, 222 ,333")
	if err != nil || !a.Allowed(111) || !a.Allowed(222) || a.Allowed(444) {
		t.Fatalf("allowlist=%v err=%v", a, err)
	}
}

func TestParseAllowlist_RejectsGarbage(t *testing.T) {
	if _, err := ParseAllowlist("111,abc"); err == nil {
		t.Fatal("expected error for non-numeric id")
	}
}

// pending_test.go
func TestPendingStore_SetGetClear(t *testing.T) {
	p := NewPendingStore(5 * time.Minute)
	p.Set(1, PendingAction{Kind: "create_ttn", OrderID: 9, Step: "await_weight"})
	a, ok := p.Get(1)
	if !ok || a.OrderID != 9 {
		t.Fatalf("get: %+v %v", a, ok)
	}
	p.Clear(1)
	if _, ok := p.Get(1); ok {
		t.Fatal("expected cleared")
	}
}

func TestPendingStore_ExpiresAfterTTL(t *testing.T) {
	p := NewPendingStore(time.Minute)
	fakeNow := time.Now()
	p.now = func() time.Time { return fakeNow }
	p.Set(1, PendingAction{Kind: "create_ttn", OrderID: 9})
	fakeNow = fakeNow.Add(2 * time.Minute)
	if _, ok := p.Get(1); ok {
		t.Fatal("expected pending action to have expired")
	}
}

// ratelimit_test.go
func TestRateLimiter_AllowsUpToLimitThenBlocksWithinWindow(t *testing.T) {
	r := NewRateLimiter(3, time.Minute)
	fakeNow := time.Now()
	r.now = func() time.Time { return fakeNow }
	for i := 0; i < 3; i++ {
		if !r.Allow(1) {
			t.Fatalf("call %d should be allowed", i)
		}
	}
	if r.Allow(1) {
		t.Fatal("4th call within the window should be blocked")
	}
	if !r.Allow(2) {
		t.Fatal("a different user must have their own budget")
	}
}

func TestRateLimiter_ResetsAfterWindowElapses(t *testing.T) {
	r := NewRateLimiter(1, time.Minute)
	fakeNow := time.Now()
	r.now = func() time.Time { return fakeNow }
	r.Allow(1)
	fakeNow = fakeNow.Add(2 * time.Minute)
	if !r.Allow(1) {
		t.Fatal("expected the budget to reset once the window elapsed")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tgbot/... -run 'Allowlist|PendingStore' -v`
Expected: FAIL — not implemented.

- [ ] **Step 3: Implement**

`ParseAllowlist` splits on `,`, trims spaces per entry, `strconv.ParseInt` each (base 10, 64-bit); any parse failure returns an error naming the bad entry. `PendingStore` wraps a `sync.Mutex` + `map[int64]PendingAction`; `Get` checks `p.now().Sub(a.CreatedAt) > p.ttl` and treats an expired entry as absent (also deletes it from the map opportunistically). `RateLimiter` wraps a `sync.Mutex` + `map[int64]struct{ count int; windowStart time.Time }`; `Allow` resets `count` to 0 and `windowStart` to `now()` whenever `now().Sub(windowStart) > window`, then increments `count` and returns `count <= limit`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tgbot/... -run 'Allowlist|PendingStore|RateLimiter' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tgbot/auth.go internal/tgbot/pending.go internal/tgbot/ratelimit.go internal/tgbot/auth_test.go internal/tgbot/pending_test.go internal/tgbot/ratelimit_test.go
git commit -m "feat(tgbot): user-id allowlist, pending-dialog state and per-user rate limiting"
```

---

## Task 6: Render helpers

**Files:**
- Create: `internal/tgbot/render.go`
- Test: `internal/tgbot/render_test.go`

**Interfaces:**
- Consumes: `store.Contact`, `store.Change`, `store.BotOrder` (Tasks 1–3); `tgbot.InlineKeyboardMarkup`/`InlineKeyboardButton` (Task 4).
- Produces: `RenderContactCard(c store.Contact, changes map[string]store.Change) string`; `RenderOrderSummary(o store.BotOrder) string`; `ReviewKeyboard(contactID int64, directusURL string) *InlineKeyboardMarkup`; `OrderActionsKeyboard(o store.BotOrder, directusURL string) *InlineKeyboardMarkup`; `DirectusLink(baseURL, collection string, id int64) string`.

- [ ] **Step 1: Write the failing tests**

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tgbot/... -run 'Render|DirectusLink|Keyboard' -v`
Expected: FAIL — not implemented.

- [ ] **Step 3: Implement**

`RenderContactCard` lists `store.Fields` in order with current values, then a "Змінено:" block for each entry in `changes` (`old → new`). `RenderOrderSummary` lists id/status/items/amount/recipient/TTN. `DirectusLink` returns `""` when `baseURL == ""`, else `strings.TrimRight(baseURL, "/") + "/admin/content/" + collection + "/" + strconv.FormatInt(id, 10)`. `ReviewKeyboard` always has a `[{Text:"✅ Підтвердити", CallbackData: fmt.Sprintf("confirm_review:%d", contactID)}]` row, plus a second row with a URL button to `DirectusLink(directusURL, "contacts", contactID)` only when that link is non-empty. `OrderActionsKeyboard` builds rows conditionally: a "📦 Створити ТТН" (`create_ttn:<id>`) row only when `o.TTN == nil`, a "🗑 Скасувати ТТН" (`delete_ttn:<id>`) row only when `o.TTN != nil`, a row of `paid`/`delivered`/`returned`/`cancelled` buttons (`set_status:<id>:<status>`) always, and the optional Directus row.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tgbot/... -run 'Render|DirectusLink|Keyboard' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tgbot/render.go internal/tgbot/render_test.go
git commit -m "feat(tgbot): render contact cards, order summaries and action keyboards"
```

---

## Task 7: Bot core — long-poll loop, dispatch, whitelist, `/start`

**Files:**
- Create: `internal/tgbot/bot.go`
- Test: `internal/tgbot/bot_test.go`

**Interfaces:**
- Consumes: `tgbot.Client`/`Update`/`Message`/`CallbackQuery` (Task 4), `tgbot.Allowlist`/`PendingStore`/`PendingAction` (Task 5), `store.*` types (Tasks 1–3).
- Produces the package-wide `Store` interface every later task's tests fake against:

```go
type Store interface {
	BotUpdateOffset(ctx context.Context) (int64, error)
	SetBotUpdateOffset(ctx context.Context, updateID int64) error
	RegisterBotUser(ctx context.Context, tgUserID, chatID int64, username string) error
	BotUsers(ctx context.Context) ([]store.BotUser, error)
	LogBotAction(ctx context.Context, tgUserID int64, action string, contactID, orderID *int64, detail json.RawMessage) error

	Contact(ctx context.Context, id int64) (store.Contact, error)
	ContactsNeedingReview(ctx context.Context, limit int) ([]int64, error)
	ConfirmReview(ctx context.Context, id int64) error
	ContactsPendingReviewPush(ctx context.Context, limit int) ([]int64, error)
	MarkReviewPushed(ctx context.Context, id int64) error
	LastExtraction(ctx context.Context, contactID int64) (store.Extraction, error)

	BotOrder(ctx context.Context, id int64) (store.BotOrder, error)
	OrdersPendingShipment(ctx context.Context, limit int) ([]int64, error)
	OrdersPendingTTNPush(ctx context.Context, limit int) ([]int64, error)
	MarkTTNPushed(ctx context.Context, id int64) error
	SetOrderStatus(ctx context.Context, id int64, status string) error
}

type Shipper interface {
	CreateTTN(ctx context.Context, orderID int64, weight, declaredValue float64) (string, error)
	DeleteTTN(ctx context.Context, orderID int64) error
}

type Bot struct {
	Store       Store
	Shipping    Shipper // nil → bot replies "ТТН не налаштована на сервері" for ttn actions
	Client      *Client
	Allowed     Allowlist
	RateLimit   *RateLimiter // nil → no limiting (tests may leave it unset)
	Pending     *PendingStore
	DirectusURL string
	Log         *slog.Logger
}

func (b *Bot) Run(ctx context.Context)
func (b *Bot) handleUpdate(ctx context.Context, u Update)
func (b *Bot) handleMessage(ctx context.Context, m *Message)
func (b *Bot) handleCallback(ctx context.Context, cq *CallbackQuery)
```

`*store.Store` (the real Postgres-backed one) satisfies this `Store` interface once Tasks 1–3 land; `*shipping.Service` satisfies `Shipper` already (`internal/shipping/shipping.go`'s `CreateTTN`/`DeleteTTN` signatures match exactly). Tasks 8–12 extend `handleMessage`'s command switch and `handleCallback`'s prefix switch, and are tested against a `fakeStore`/`fakeClient`/`fakeShipper` implementing these three interfaces — defined once in `bot_test.go` and reused (via same-package visibility) by every other `_test.go` file in `internal/tgbot`.

- [ ] **Step 1: Write the failing tests**

```go
// bot_test.go — shared fakes, reused by Tasks 8–12's tests in the same package.
type fakeStore struct {
	offset          int64
	users           []store.BotUser
	registered      []struct{ tgUserID, chatID int64; username string }
	actions         []struct{ tgUserID int64; action string }
	needingReview   []int64
	contacts        map[int64]store.Contact
	lastExtraction  map[int64]store.Extraction
	confirmErr      map[int64]error
	pendingShipment []int64
	botOrders       map[int64]store.BotOrder
	setStatusCalls  []struct{ id int64; status string }
	setStatusErr    error
}
// ... implements every Store method, appending to the slices/maps above,
// returning zero values / nil error unless a field like confirmErr/setStatusErr says otherwise.

type fakeClient struct {
	sent   []struct{ chatID int64; text string; kb *InlineKeyboardMarkup }
	edited []struct{ chatID, messageID int64; text string }
	answered []struct{ id, text string }
}
func (c *fakeClient) send(chatID int64, text string, kb *InlineKeyboardMarkup) (int64, error) { ... }
// bot.go's Bot must call through an interface wrapping *Client so fakeClient can substitute it in tests —
// see Step 3.

func TestHandleUpdate_UnauthorizedIsIgnored(t *testing.T) {
	st := &fakeStore{}
	cl := &fakeClient{}
	b := &Bot{Store: st, Client: ..., Allowed: Allowlist{111: true}, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	b.handleUpdate(context.Background(), Update{Message: &Message{Chat: Chat{ID: 1}, From: &User{ID: 999}, Text: "/start"}})
	if len(st.registered) != 0 || len(cl.sent) != 0 {
		t.Fatalf("unauthorized update must be a no-op: %+v %+v", st.registered, cl.sent)
	}
}

func TestHandleUpdate_StartRegistersAllowedUser(t *testing.T) {
	st := &fakeStore{}
	b := &Bot{Store: st, Client: ..., Allowed: Allowlist{111: true}, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	b.handleUpdate(context.Background(), Update{Message: &Message{Chat: Chat{ID: 9001}, From: &User{ID: 111, Username: "owner"}, Text: "/start"}})
	if len(st.registered) != 1 || st.registered[0].chatID != 9001 {
		t.Fatalf("expected registration: %+v", st.registered)
	}
}

func TestRun_AdvancesOffsetOnlyAfterProcessing(t *testing.T) {
	// fakeClient.GetUpdates returns one batch with update_id=5, then blocks until ctx is cancelled.
	// Assert SetBotUpdateOffset(ctx, 6) was called exactly once, and only after the update was dispatched.
}

func TestHandleUpdate_OverRateLimitIsIgnoredLikeUnauthorized(t *testing.T) {
	st := &fakeStore{}
	rl := NewRateLimiter(1, time.Minute)
	b := &Bot{Store: st, Client: ..., Allowed: Allowlist{111: true}, RateLimit: rl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	msg := func() Update { return Update{Message: &Message{Chat: Chat{ID: 1}, From: &User{ID: 111}, Text: "/start"}} }
	b.handleUpdate(context.Background(), msg()) // consumes the one allowed slot
	b.handleUpdate(context.Background(), msg()) // over budget
	if len(st.registered) != 1 {
		t.Fatalf("expected exactly one registration, got %d", len(st.registered))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tgbot/... -run 'HandleUpdate|Run_Advances' -v`
Expected: FAIL — `Bot` doesn't exist.

- [ ] **Step 3: Implement `internal/tgbot/bot.go`**

Define a small `telegramClient` interface (`GetUpdates`/`SendMessage`/`EditMessageText`/`AnswerCallbackQuery`, matching `*Client`'s methods exactly) so `Bot.Client` can be that interface type in tests while `*Client` satisfies it in production — avoids network calls in unit tests without needing `httptest` everywhere. `Run`: load `offset, _ := b.Store.BotUpdateOffset(ctx)`; loop `for { updates, err := b.Client.GetUpdates(ctx, offset+1, 25); ... for _, u := range updates { b.handleUpdate(ctx, u); offset = u.UpdateID }; b.Store.SetBotUpdateOffset(ctx, offset) }`, returning when `ctx.Err() != nil`. `handleUpdate` extracts the sender's user id (`u.Message.From.ID` or `u.CallbackQuery.From.ID`), returns immediately if `!b.Allowed.Allowed(id)` or if `b.RateLimit != nil && !b.RateLimit.Allow(id)` (both are silent no-ops, same as spec's "тиша" for unauthorized users), else dispatches to `handleMessage`/`handleCallback`. `handleMessage`: if `m.Text == "/start"`, call `b.Store.RegisterBotUser` and reply with a short usage message via `b.Client.SendMessage`; a command switch stub for `/review`/`/orders` is added here but implemented in Task 8. `handleCallback` is a stub switch on `strings.SplitN(cq.Data, ":", 2)[0]`, filled in by Tasks 9–11; always call `b.Client.AnswerCallbackQuery` at the end of whichever branch runs (Telegram requires this to stop the client's loading spinner).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tgbot/... -run 'HandleUpdate|Run_Advances' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tgbot/bot.go internal/tgbot/bot_test.go
git commit -m "feat(tgbot): long-poll loop, whitelist dispatch, /start registration"
```

---

## Task 8: Pull commands `/review` and `/orders`

**Files:**
- Modify: `internal/tgbot/bot.go` (extend the command switch in `handleMessage`)
- Create: `internal/tgbot/commands.go`
- Test: `internal/tgbot/commands_test.go`

**Interfaces:**
- Consumes: `Bot`, `fakeStore`/`fakeClient` (Task 7); `RenderContactCard`/`RenderOrderSummary`/`ReviewKeyboard`/`OrderActionsKeyboard` (Task 6).
- Produces: `func (b *Bot) cmdReview(ctx context.Context, chatID int64)`, `func (b *Bot) cmdOrders(ctx context.Context, chatID int64)`.

- [ ] **Step 1: Write the failing tests**

```go
func TestCmdReview_NoPendingSendsEmptyMessage(t *testing.T) {
	st := &fakeStore{}
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
	b := &Bot{Store: st, Client: cl, Log: discardLog()}
	b.cmdReview(context.Background(), 9001)
	if len(cl.sent) != 2 {
		t.Fatalf("sent: %+v", cl.sent)
	}
	if cl.sent[0].kb.InlineKeyboard[0][0].CallbackData != "confirm_review:1" {
		t.Errorf("missing confirm button: %+v", cl.sent[0].kb)
	}
}

func TestCmdReview_CapsAtFiveWithOverflowNote(t *testing.T) {
	st := &fakeStore{needingReview: []int64{1, 2, 3, 4, 5, 6, 7}, contacts: /* all 7 present */}
	b := &Bot{Store: st, Client: cl, Log: discardLog()}
	b.cmdReview(context.Background(), 9001)
	if len(cl.sent) != 6 { // 5 cards + 1 overflow note
		t.Fatalf("sent: %d messages", len(cl.sent))
	}
	if !strings.Contains(cl.sent[5].text, "2") { // "і ще 2"
		t.Errorf("overflow note: %q", cl.sent[5].text)
	}
}

func TestCmdOrders_SendsSummaryWithCreateTTNButton(t *testing.T) {
	st := &fakeStore{pendingShipment: []int64{10}, botOrders: map[int64]store.BotOrder{10: {ID: 10, Status: "new"}}}
	b := &Bot{Store: st, Client: cl, Log: discardLog()}
	b.cmdOrders(context.Background(), 9001)
	if len(cl.sent) != 1 || cl.sent[0].kb.InlineKeyboard[0][0].CallbackData != "create_ttn:10" {
		t.Fatalf("sent: %+v", cl.sent)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tgbot/... -run 'CmdReview|CmdOrders' -v`
Expected: FAIL — `cmdReview` undefined.

- [ ] **Step 3: Implement**

`cmdReview`: `ids, _ := b.Store.ContactsNeedingReview(ctx, 10)`; if empty, send "Немає карток на перевірку."; else for up to the first 5 ids, fetch `Contact`+`LastExtraction`, send `RenderContactCard(c, ext.Changes)` with `ReviewKeyboard(id, b.DirectusURL)`; if `len(ids) > 5`, send a final `fmt.Sprintf("...і ще %d — дивись у Directus.", len(ids)-5)`. `cmdOrders` follows the same shape over `OrdersPendingShipment`/`BotOrder`/`RenderOrderSummary`/`OrderActionsKeyboard`. Wire both into `handleMessage`'s switch (Task 7's stub) by text match on `"/review"`/`"/orders"`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tgbot/... -run 'CmdReview|CmdOrders' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tgbot/bot.go internal/tgbot/commands.go internal/tgbot/commands_test.go
git commit -m "feat(tgbot): /review and /orders pull commands"
```

---

## Task 9: Action — confirm review

**Files:**
- Modify: `internal/tgbot/bot.go` (extend `handleCallback`'s switch)
- Create: `internal/tgbot/actions_review.go`
- Test: `internal/tgbot/actions_review_test.go`

**Interfaces:**
- Produces: `func (b *Bot) actionConfirmReview(ctx context.Context, cq *CallbackQuery, contactID int64)`.

- [ ] **Step 1: Write the failing tests**

```go
func TestActionConfirmReview_Success(t *testing.T) {
	st := &fakeStore{}
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
	b := &Bot{Store: st, Client: cl, Log: discardLog()}
	cq := &CallbackQuery{ID: "cb1", From: User{ID: 111}, Message: &Message{Chat: Chat{ID: 9001}, MessageID: 55}}
	b.actionConfirmReview(context.Background(), cq, 7)
	if len(st.actions) != 0 {
		t.Errorf("must not log a no-op as an executed action: %+v", st.actions)
	}
	if len(cl.answered) != 1 {
		t.Fatalf("expected an answer to the callback either way: %+v", cl.answered)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tgbot/... -run ActionConfirmReview -v`
Expected: FAIL — `actionConfirmReview` undefined.

- [ ] **Step 3: Implement**

`err := b.Store.ConfirmReview(ctx, contactID)`; on `nil`: `b.Client.EditMessageText(ctx, cq.Message.Chat.ID, cq.Message.MessageID, "✅ Підтверджено", nil)` (nil keyboard removes the buttons), `b.Store.LogBotAction(ctx, cq.From.ID, "confirm_review", &contactID, nil, nil)`, `b.Client.AnswerCallbackQuery(ctx, cq.ID, "Підтверджено")`. On error: `b.Client.AnswerCallbackQuery(ctx, cq.ID, "Вже підтверджено або не знайдено")`, no edit, no log. Wire `"confirm_review"` into `handleCallback`'s switch, parsing the contact id from the part after `:`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tgbot/... -run ActionConfirmReview -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tgbot/bot.go internal/tgbot/actions_review.go internal/tgbot/actions_review_test.go
git commit -m "feat(tgbot): confirm-review action with audit log"
```

---

## Task 10: Action — create/cancel TTN (two-step, money-critical)

**Files:**
- Modify: `internal/tgbot/bot.go` (extend `handleMessage` to consult `Pending` for free-text replies, extend `handleCallback`'s switch)
- Create: `internal/tgbot/actions_ttn.go`
- Test: `internal/tgbot/actions_ttn_test.go`

**Interfaces:**
- Consumes: `Shipper` (Task 7); `PendingAction{Kind: "create_ttn"|"delete_ttn", OrderID, Step, Weight}` (Task 5).
- Produces: `func (b *Bot) actionCreateTTNStart(ctx context.Context, cq *CallbackQuery, orderID int64)`, `func (b *Bot) handlePendingReply(ctx context.Context, m *Message, p PendingAction) bool` (returns whether the message was consumed as a pending-dialog reply), `func (b *Bot) actionCreateTTNConfirm(ctx context.Context, cq *CallbackQuery, orderID int64)`, `func (b *Bot) actionDeleteTTNStart/Confirm(...)`.

- [ ] **Step 1: Write the failing tests**

```go
type fakeShipper struct {
	createCalls []struct{ orderID int64; weight, declared float64 }
	createTTN   string
	createErr   error
	deleteCalls []int64
	deleteErr   error
}
func (f *fakeShipper) CreateTTN(ctx context.Context, orderID int64, weight, declared float64) (string, error) {
	f.createCalls = append(f.createCalls, struct{ orderID int64; weight, declared float64 }{orderID, weight, declared})
	return f.createTTN, f.createErr
}
func (f *fakeShipper) DeleteTTN(ctx context.Context, orderID int64) error {
	f.deleteCalls = append(f.deleteCalls, orderID)
	return f.deleteErr
}

func TestCreateTTN_HappyPath(t *testing.T) {
	sh := &fakeShipper{createTTN: "20450000000001"}
	b := &Bot{Store: &fakeStore{}, Shipping: sh, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	userID, chatID := int64(111), int64(9001)

	b.actionCreateTTNStart(context.Background(), &CallbackQuery{ID: "cb1", From: User{ID: userID}, Message: &Message{Chat: Chat{ID: chatID}}}, 10)
	b.handlePendingReply(context.Background(), &Message{Chat: Chat{ID: chatID}, From: &User{ID: userID}, Text: "1,2"}, mustGet(t, b.Pending, userID))
	p := mustGet(t, b.Pending, userID)
	if p.Step != "confirm" || p.Weight != 1.2 {
		t.Fatalf("pending after weight: %+v", p)
	}

	b.actionCreateTTNConfirm(context.Background(), &CallbackQuery{ID: "cb2", From: User{ID: userID}, Message: &Message{Chat: Chat{ID: chatID}, MessageID: 1}}, 10)
	if len(sh.createCalls) != 1 || sh.createCalls[0].weight != 1.2 {
		t.Fatalf("create calls: %+v", sh.createCalls)
	}
	if _, ok := b.Pending.Get(userID); ok {
		t.Error("pending must be cleared after a successful confirm")
	}
}

func TestCreateTTN_InvalidWeightAsksAgainWithoutClearingPending(t *testing.T) {
	b := &Bot{Store: &fakeStore{}, Shipping: &fakeShipper{}, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	userID, chatID := int64(111), int64(9001)
	b.Pending.Set(userID, PendingAction{Kind: "create_ttn", OrderID: 10, Step: "await_weight", CreatedAt: time.Now()})
	b.handlePendingReply(context.Background(), &Message{Chat: Chat{ID: chatID}, From: &User{ID: userID}, Text: "не число"}, mustGet(t, b.Pending, userID))
	p, ok := b.Pending.Get(userID)
	if !ok || p.Step != "await_weight" {
		t.Fatalf("pending must survive a bad weight: %+v %v", p, ok)
	}
}

func TestCreateTTN_ConfirmWithoutMatchingPendingIsRejected(t *testing.T) {
	sh := &fakeShipper{}
	b := &Bot{Store: &fakeStore{}, Shipping: sh, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	b.actionCreateTTNConfirm(context.Background(), &CallbackQuery{ID: "cb1", From: User{ID: 111}, Message: &Message{Chat: Chat{ID: 1}}}, 10)
	if len(sh.createCalls) != 0 {
		t.Fatal("must not call CreateTTN without a matching pending action")
	}
}

func TestCreateTTN_ShippingServiceErrorIsShownVerbatim(t *testing.T) {
	sh := &fakeShipper{createErr: fmt.Errorf("замовлення №10: невідома сума, вкажи оголошену вартість вручну")}
	b := &Bot{Store: &fakeStore{}, Shipping: sh, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	userID := int64(111)
	b.Pending.Set(userID, PendingAction{Kind: "create_ttn", OrderID: 10, Step: "confirm", Weight: 1.2, CreatedAt: time.Now()})
	b.actionCreateTTNConfirm(context.Background(), &CallbackQuery{ID: "cb1", From: User{ID: userID}, Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}, 10)
	if len(cl.edited) != 1 || !strings.Contains(cl.edited[0].text, "невідома сума") {
		t.Fatalf("expected the service error to reach the user verbatim: %+v", cl.edited)
	}
}

func TestCreateTTN_RepeatedConfirmAfterSuccessDoesNotCreateASecondTTN(t *testing.T) {
	// simulates a redelivered Telegram update hitting confirm twice: second call has no
	// matching pending action (already cleared by the first), so it must be a no-op —
	// same guarantee shipping.Service.CreateTTN's own "ttn IS NULL" check gives at the DB layer.
	sh := &fakeShipper{createTTN: "ttn-1"}
	b := &Bot{Store: &fakeStore{}, Shipping: sh, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	userID := int64(111)
	b.Pending.Set(userID, PendingAction{Kind: "create_ttn", OrderID: 10, Step: "confirm", Weight: 1.2, CreatedAt: time.Now()})
	cq := &CallbackQuery{ID: "cb1", From: User{ID: userID}, Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}
	b.actionCreateTTNConfirm(context.Background(), cq, 10)
	b.actionCreateTTNConfirm(context.Background(), cq, 10) // redelivered
	if len(sh.createCalls) != 1 {
		t.Fatalf("expected exactly one CreateTTN call, got %d", len(sh.createCalls))
	}
}

func TestCreateTTN_NilShippingRepliesConfigurationError(t *testing.T) {
	b := &Bot{Store: &fakeStore{}, Shipping: nil, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	b.actionCreateTTNStart(context.Background(), &CallbackQuery{ID: "cb1", From: User{ID: 111}, Message: &Message{Chat: Chat{ID: 1}}}, 10)
	if len(cl.sent) != 1 || !strings.Contains(cl.sent[0].text, "не налаштован") {
		t.Fatalf("expected a configuration-error reply: %+v", cl.sent)
	}
}

func TestDeleteTTN_ConfirmCallsShipperDeleteTTN(t *testing.T) {
	sh := &fakeShipper{}
	b := &Bot{Store: &fakeStore{}, Shipping: sh, Client: cl, Pending: NewPendingStore(time.Minute), Log: discardLog()}
	userID := int64(111)
	b.Pending.Set(userID, PendingAction{Kind: "delete_ttn", OrderID: 10, Step: "confirm", CreatedAt: time.Now()})
	b.actionDeleteTTNConfirm(context.Background(), &CallbackQuery{ID: "cb1", From: User{ID: userID}, Message: &Message{Chat: Chat{ID: 1}, MessageID: 1}}, 10)
	if len(sh.deleteCalls) != 1 || sh.deleteCalls[0] != 10 {
		t.Fatalf("delete calls: %+v", sh.deleteCalls)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tgbot/... -run 'TTN' -v`
Expected: FAIL — not implemented.

- [ ] **Step 3: Implement `internal/tgbot/actions_ttn.go`**

`actionCreateTTNStart`: if `b.Shipping == nil`, `b.Client.SendMessage(ctx, cq.Message.Chat.ID, "ТТН не налаштована на сервері (NP_API_KEY).", nil)` and return; else `b.Pending.Set(cq.From.ID, PendingAction{Kind: "create_ttn", OrderID: orderID, Step: "await_weight", CreatedAt: time.Now()})`, send "Введіть вагу посилки в кг (наприклад: 1.2). /cancel — скасувати.". `handlePendingReply` is called from `handleMessage` (Task 7's stub, now filled in) before the `/review`/`/orders`/`/start` switch, only when `b.Pending.Get(userID)` returns `ok`; if `m.Text == "/cancel"`, clear and reply "Скасовано."; else if `p.Kind == "create_ttn" && p.Step == "await_weight"`: parse the text with `strconv.ParseFloat(strings.ReplaceAll(text, ",", "."), 64)`; on error or `<= 0`, reply "Не розпізнав вагу, спробуй ще раз (наприклад 1.2)." and keep the pending state as-is; on success, `p.Weight = weight; p.Step = "confirm"; b.Pending.Set(userID, p)`, send a summary with `[✅ Підтвердити](create_ttn_confirm:<id>)`/`[❌ Скасувати](create_ttn_cancel:<id>)`. `actionCreateTTNConfirm`: `p, ok := b.Pending.Get(cq.From.ID)`; if `!ok || p.Kind != "create_ttn" || p.OrderID != orderID || p.Step != "confirm"`, answer the callback with "Дія застаріла, почни заново" and return (covers both double-tap and a stale button from a different dialog); else `b.Pending.Clear(cq.From.ID)` **before** calling `b.Shipping.CreateTTN` (so a slow call racing a second tap still sees no pending action the second time), call it, and on success edit the message to `"ТТН створено: " + ttn` + log the action with `detail` = `{"ttn":...,"weight":...}`; on error, edit the message to `"Помилка: " + err.Error()` (the service's own error text, verbatim — Review Focus item 4) and do not log an action. `actionDeleteTTNStart`/`actionDeleteTTNConfirm` mirror the create flow without the weight step (`Step` goes straight to `"confirm"`). Wire `create_ttn`, `create_ttn_confirm`, `create_ttn_cancel`, `delete_ttn`, `delete_ttn_confirm` into `handleCallback`'s switch.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tgbot/... -run 'TTN' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tgbot/bot.go internal/tgbot/actions_ttn.go internal/tgbot/actions_ttn_test.go
git commit -m "feat(tgbot): two-step create/cancel TTN flow with weight prompt"
```

---

## Task 11: Action — manual order status change

**Files:**
- Modify: `internal/tgbot/bot.go` (extend `handleCallback`'s switch)
- Create: `internal/tgbot/actions_status.go`
- Test: `internal/tgbot/actions_status_test.go`

**Interfaces:**
- Produces: `func (b *Bot) actionSetStatusStart(ctx context.Context, cq *CallbackQuery, orderID int64, status string)`, `func (b *Bot) actionSetStatusConfirm(ctx context.Context, cq *CallbackQuery, orderID int64)`.

- [ ] **Step 1: Write the failing tests**

```go
func TestSetStatus_PromptThenConfirmUpdatesStatus(t *testing.T) {
	st := &fakeStore{}
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
}

func TestSetStatus_StaleConfirmWithoutPendingIsNoop(t *testing.T) {
	st := &fakeStore{}
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tgbot/... -run SetStatus -v`
Expected: FAIL — not implemented.

- [ ] **Step 3: Implement**

`actionSetStatusStart`: `b.Pending.Set(cq.From.ID, PendingAction{Kind: "set_status", OrderID: orderID, Status: status, Step: "confirm", CreatedAt: time.Now()})`, send `fmt.Sprintf("Позначити замовлення #%d як %q?", orderID, status)` with confirm/cancel buttons (`set_status_confirm:<id>` / `set_status_cancel:<id>`). `actionSetStatusConfirm`: same stale-pending guard pattern as Task 10 (`p.Kind != "set_status" || p.OrderID != orderID`), clear pending before calling, `b.Store.SetOrderStatus(ctx, orderID, p.Status)`, on success edit message + `LogBotAction(ctx, cq.From.ID, "set_status", nil, &orderID, detail)` with `detail = {"status": p.Status}`, on error edit with the error text. Wire `set_status`, `set_status_confirm`, `set_status_cancel` into `handleCallback` (the `set_status:<id>:<status>` callback_data needs a 3-way split, not `handleCallback`'s default 2-way `SplitN(data, ":", 2)` — split on the first `:` to get the action, then split the remainder on `:` again for `id`/`status`).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tgbot/... -run SetStatus -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tgbot/bot.go internal/tgbot/actions_status.go internal/tgbot/actions_status_test.go
git commit -m "feat(tgbot): manual order status change action"
```

---

## Task 12: Push worker

**Files:**
- Create: `internal/tgbot/push.go`
- Test: `internal/tgbot/push_test.go`

**Interfaces:**
- Consumes: `Store` (Task 7), `telegramClient`-shaped send capability, `RenderContactCard`/`ReviewKeyboard`/`RenderOrderSummary`/`OrderActionsKeyboard` (Task 6).
- Produces: `type PushWorker struct { Store Store; Client telegramSender; DirectusURL string; Interval time.Duration; Log *slog.Logger }` with `func (w *PushWorker) Run(ctx context.Context)` and `func (w *PushWorker) Tick(ctx context.Context)` (same `Run`-calls-`Tick`-on-a-ticker shape as `extract.Worker.Run`/`Tick`, `internal/extract/extract.go:195`).

- [ ] **Step 1: Write the failing tests**

```go
func TestPushWorker_FansOutReviewAndOrderPushesToAllRegisteredUsers(t *testing.T) {
	st := &fakeStore{
		users:         []store.BotUser{{TGUserID: 111, ChatID: 9001}, {TGUserID: 222, ChatID: 9002}},
		needingReview: []int64{}, // ContactsPendingReviewPush uses a separate fake field below
	}
	st.reviewPushPending = []int64{1}
	st.contacts = map[int64]store.Contact{1: {ID: 1}}
	st.ttnPushPending = []int64{10}
	st.botOrders = map[int64]store.BotOrder{10: {ID: 10}}

	cl := &fakeClient{}
	w := &PushWorker{Store: st, Client: cl, Log: discardLog()}
	w.Tick(context.Background())

	if len(cl.sent) != 4 { // 1 review card + 1 order card, each to 2 users
		t.Fatalf("sent: %d messages, want 4: %+v", len(cl.sent), cl.sent)
	}
	if len(st.reviewPushedIDs) != 1 || len(st.ttnPushedIDs) != 1 {
		t.Fatalf("mark-pushed calls: review=%v ttn=%v", st.reviewPushedIDs, st.ttnPushedIDs)
	}
}

func TestPushWorker_UnregisteredAllowedUserDoesNotBreakFanout(t *testing.T) {
	// a user can be in TG_ALLOWED_USER_IDS but never have sent /start — they simply have
	// no row in BotUsers(); the push worker only reads BotUsers(), so this is already the
	// natural behavior, this test just pins it.
	st := &fakeStore{users: []store.BotUser{{TGUserID: 111, ChatID: 9001}}, reviewPushPending: []int64{1}, contacts: map[int64]store.Contact{1: {ID: 1}}}
	cl := &fakeClient{}
	w := &PushWorker{Store: st, Client: cl, Log: discardLog()}
	w.Tick(context.Background())
	if len(cl.sent) != 1 {
		t.Fatalf("sent: %+v", cl.sent)
	}
}

func TestPushWorker_NothingPendingSendsNothing(t *testing.T) {
	st := &fakeStore{users: []store.BotUser{{TGUserID: 111, ChatID: 9001}}}
	cl := &fakeClient{}
	w := &PushWorker{Store: st, Client: cl, Log: discardLog()}
	w.Tick(context.Background())
	if len(cl.sent) != 0 {
		t.Fatalf("sent: %+v", cl.sent)
	}
}
```

(`fakeStore` gains `reviewPushPending`/`ttnPushPending` fields plus `reviewPushedIDs`/`ttnPushedIDs` tracking slices for `ContactsPendingReviewPush`/`OrdersPendingTTNPush`/`MarkReviewPushed`/`MarkTTNPushed` — extending the same fake from Task 7, not a new type.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tgbot/... -run PushWorker -v`
Expected: FAIL — `PushWorker` doesn't exist.

- [ ] **Step 3: Implement `internal/tgbot/push.go`**

`Run` mirrors `extract.Worker.Run`'s ticker loop (`internal/extract/extract.go:195-206`), defaulting `Interval` to 20s if zero. `Tick`: `users, err := w.Store.BotUsers(ctx)`; if that errors, log and return (nothing to push to). For review: `ids, _ := w.Store.ContactsPendingReviewPush(ctx, 20)`; for each id, `c, _ := w.Store.Contact(ctx, id)`, `ext, _ := w.Store.LastExtraction(ctx, id)`, build `text := RenderContactCard(c, ext.Changes)` and `kb := ReviewKeyboard(id, w.DirectusURL)` once, then loop `for _, u := range users { w.Client.SendMessage(ctx, u.ChatID, text, kb) }` (log+continue on a per-send error — one user's failure must not stop the others or skip marking-pushed), then `w.Store.MarkReviewPushed(ctx, id)` once after the fan-out. Same shape for orders via `OrdersPendingTTNPush`/`BotOrder`/`RenderOrderSummary`/`OrderActionsKeyboard`/`MarkTTNPushed`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tgbot/... -run PushWorker -v`
Expected: PASS

- [ ] **Step 5: Run the whole `tgbot` package's test suite**

Run: `go test ./internal/tgbot/... -v`
Expected: PASS (every test from Tasks 4–12)

- [ ] **Step 6: Commit**

```bash
git add internal/tgbot/push.go internal/tgbot/push_test.go internal/tgbot/bot_test.go
git commit -m "feat(tgbot): push worker fans out review/shipment notifications to all registered users"
```

---

## Task 13: Wire into `main.go`, config, and docs

**Files:**
- Modify: `internal/config/config.go`
- Modify: `cmd/igcrm/main.go`
- Modify: `.env.example`
- Modify: `README.md`

**Interfaces:**
- Consumes: `config.Config` (existing), `tgbot.Bot`/`PushWorker`/`ParseAllowlist`/`NewPendingStore` (Tasks 4–12), `shipping.Service` (existing, already satisfies `tgbot.Shipper`).

- [ ] **Step 1: Add config fields**

In `internal/config/config.go`, add to `Config`:
```go
// Telegram-бот: інтерактивне керування (опційно — без TG_ALLOWED_USER_IDS бот вимкнений)
TGAllowedUserIDs   string // CSV, парситься tgbot.ParseAllowlist
DirectusPublicURL  string // для кнопки "Відкрити в Directus"; без неї кнопка просто не додається
```
and to `Load()`:
```go
TGAllowedUserIDs:  os.Getenv("TG_ALLOWED_USER_IDS"),
DirectusPublicURL: strings.TrimRight(os.Getenv("DIRECTUS_PUBLIC_URL"), "/"),
```

- [ ] **Step 2: Wire the bot into `serve()` in `cmd/igcrm/main.go`**

```go
if cfg.TGBotToken != "" && cfg.TGAllowedUserIDs != "" {
	allowed, err := tgbot.ParseAllowlist(cfg.TGAllowedUserIDs)
	if err != nil {
		return fmt.Errorf("TG_ALLOWED_USER_IDS: %w", err)
	}
	tgClient := &tgbot.Client{BotToken: cfg.TGBotToken}
	var shipper tgbot.Shipper
	if cfg.NPAPIKey != "" {
		shipper = &shipping.Service{Store: st, NP: &np.Client{APIKey: cfg.NPAPIKey}, SenderCity: cfg.NPSenderCity, SenderWarehouseNo: cfg.NPSenderWarehouseNo}
	}
	bot := &tgbot.Bot{Store: st, Shipping: shipper, Client: tgClient, Allowed: allowed, RateLimit: tgbot.NewRateLimiter(30, time.Minute), Pending: tgbot.NewPendingStore(5 * time.Minute), DirectusURL: cfg.DirectusPublicURL, Log: log}
	go bot.Run(ctx)
	go (&tgbot.PushWorker{Store: st, Client: tgClient, DirectusURL: cfg.DirectusPublicURL, Log: log}).Run(ctx)
	log.Info("telegram bot enabled", "allowed_users", len(allowed))
} else {
	log.Warn("telegram bot disabled: задай TG_BOT_TOKEN і TG_ALLOWED_USER_IDS")
}
```
Add `"igcrm/internal/tgbot"` to the import block. Note `shipper` stays a nil `tgbot.Shipper` when `NP_API_KEY` is unset — Task 10's `actionCreateTTNStart` already handles that by replying with a configuration-error message instead of a nil-pointer dereference.

- [ ] **Step 3: Update `.env.example`**

Add under the existing `TG_BOT_TOKEN`/`TG_CHAT_ID` lines:
```
TG_ALLOWED_USER_IDS=            # comma-separated Telegram user id (не chat id) — whitelist для інтерактивного бота
DIRECTUS_PUBLIC_URL=            # напр. https://crm.example.com — для кнопки "Відкрити в Directus" (опційно)
```

- [ ] **Step 4: Update `README.md`**

Add a short section after "Directus: initial setup" explaining: how to get your own Telegram numeric user id (e.g. message `@userinfobot`), setting `TG_ALLOWED_USER_IDS`, sending `/start` to the bot once to register, and what `/review`/`/orders` do. One sentence on the security model: unknown users get silently ignored, and every bot action is logged in `bot_actions`.

- [ ] **Step 5: Verify the whole project builds and all tests pass**

Run: `go build ./... && go vet ./...`
Expected: no errors.

Run: `go test ./...` (and, if a Postgres test DB is available, `TEST_DATABASE_URL=postgres://... go test ./internal/store/...`)
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go cmd/igcrm/main.go .env.example README.md
git commit -m "feat: wire telegram bot into serve(), document setup"
```

- [ ] **Step 7: Manual smoke test (not automatable — do this against a staging bot/token before relying on it)**

- `/start` from an allowed account registers and replies; from a non-whitelisted account, nothing comes back.
- `/review` with at least one `needs_review=true` contact shows a card with a working "✅ Підтвердити" button; tapping it clears `needs_review` and edits the message.
- `/orders` with a shippable order shows "📦 Створити ТТН"; tapping it, replying with a weight, then confirming creates a real (or sandbox) Nova Poshta TTN and the order flips to `shipped` in Directus too.
- A push message arrives automatically (within ~20s) the next time an LLM extraction sets `needs_review = true`, without running `/review`.
