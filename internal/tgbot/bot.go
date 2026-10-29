package tgbot

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"igcrm/internal/store"
)

// Store is everything the bot needs from the database. *store.Store (the
// real Postgres-backed store, Tasks 1–3) satisfies this interface; tests
// substitute a fakeStore (see bot_test.go) so no test needs a database.
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

// Shipper is what the bot needs to create/cancel a shipment's TTN.
// *shipping.Service satisfies this already.
type Shipper interface {
	CreateTTN(ctx context.Context, orderID int64, weight, declaredValue float64) (string, error)
	DeleteTTN(ctx context.Context, orderID int64) error
}

// telegramClient is exactly *Client's method set (Task 4). Bot depends on
// this interface, not the concrete *Client, so tests can substitute a
// fakeClient with no network/HTTP involved.
type telegramClient interface {
	GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error)
	SendMessage(ctx context.Context, chatID int64, text string, kb *InlineKeyboardMarkup) (messageID int64, err error)
	EditMessageText(ctx context.Context, chatID, messageID int64, text string, kb *InlineKeyboardMarkup) error
	AnswerCallbackQuery(ctx context.Context, callbackID, text string) error
}

// Bot wires together the store, the Telegram client, and the auth/rate-limit
// gates into a single long-polling dispatcher.
type Bot struct {
	Store       Store
	Shipping    Shipper // nil → bot replies "ТТН не налаштована на сервері" for ttn actions
	Client      telegramClient
	Allowed     Allowlist
	RateLimit   *RateLimiter // nil → no limiting (tests may leave it unset)
	Pending     *PendingStore
	DirectusURL string
	Log         *slog.Logger

	// retryMin/retryMax bound the GetUpdates error backoff; zero means the
	// defaults (1s doubling up to 30s). Unexported so tests can shrink them.
	retryMin, retryMax time.Duration
}

// startMessage is the short usage reply sent to /start.
const startMessage = "Бот запущено.\n\nКоманди:\n/review — черга контактів на перевірку\n/orders — замовлення, що очікують відправки"

// longPollTimeoutSec is how long each GetUpdates call waits for Telegram to
// deliver a batch before returning empty.
const longPollTimeoutSec = 25

// log returns b.Log, falling back to a discard logger if unset so Run never
// panics on a nil logger.
func (b *Bot) log() *slog.Logger {
	if b.Log != nil {
		return b.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Run long-polls Telegram for updates starting just after the last offset
// persisted in the store, dispatches each one, and only then persists the
// new offset — so a crash mid-batch re-delivers the unprocessed update
// rather than silently skipping it. It returns once ctx is cancelled.
func (b *Bot) Run(ctx context.Context) {
	offset, err := b.Store.BotUpdateOffset(ctx)
	if err != nil {
		b.log().Error("load bot update offset", "error", err)
	}

	backoff := time.Duration(0)
	for {
		if ctx.Err() != nil {
			return
		}

		updates, err := b.Client.GetUpdates(ctx, offset+1, longPollTimeoutSec)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			b.log().Error("get updates", "error", err)
			backoff = b.nextBackoff(backoff)
			t := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
			continue
		}
		backoff = 0

		for _, u := range updates {
			b.handleUpdate(ctx, u)
			offset = u.UpdateID
		}

		if len(updates) > 0 {
			if err := b.Store.SetBotUpdateOffset(ctx, offset); err != nil {
				b.log().Error("save bot update offset", "error", err)
			}
		}
	}
}

// nextBackoff returns the next GetUpdates retry delay: retryMin (default 1s)
// first, then doubling up to retryMax (default 30s).
func (b *Bot) nextBackoff(cur time.Duration) time.Duration {
	lo, hi := b.retryMin, b.retryMax
	if lo <= 0 {
		lo = time.Second
	}
	if hi <= 0 {
		hi = 30 * time.Second
	}
	if cur <= 0 {
		return lo
	}
	cur *= 2
	if cur > hi {
		cur = hi
	}
	return cur
}

// handleUpdate gates an update on the allowlist and rate limit, then
// dispatches it. Both gates are silent no-ops: an update from a user who
// fails either check produces no reply at all.
func (b *Bot) handleUpdate(ctx context.Context, u Update) {
	// One bad update must never take the whole process down.
	defer func() {
		if r := recover(); r != nil {
			b.log().Error("panic while handling update", "update_id", u.UpdateID, "panic", r)
		}
	}()

	// Only private chats are served: in a group the bot's replies (customer
	// PII) would be visible to everyone there. Telegram always sets the type;
	// an empty one is let through only so hand-built updates keep working.
	switch {
	case u.Message != nil:
		if t := u.Message.Chat.Type; t != "" && t != "private" {
			return
		}
	case u.CallbackQuery != nil && u.CallbackQuery.Message != nil:
		if t := u.CallbackQuery.Message.Chat.Type; t != "" && t != "private" {
			return
		}
	}

	var userID int64
	switch {
	case u.Message != nil && u.Message.From != nil:
		userID = u.Message.From.ID
	case u.CallbackQuery != nil:
		userID = u.CallbackQuery.From.ID
	default:
		return
	}

	if !b.Allowed.Allowed(userID) {
		return
	}
	if b.RateLimit != nil && !b.RateLimit.Allow(userID) {
		return
	}

	switch {
	case u.Message != nil:
		b.handleMessage(ctx, u.Message)
	case u.CallbackQuery != nil:
		b.handleCallback(ctx, u.CallbackQuery)
	}
}

// send sends text to chatID and logs (but does not propagate) any error.
func (b *Bot) send(ctx context.Context, chatID int64, text string) {
	if _, err := b.Client.SendMessage(ctx, chatID, text, nil); err != nil {
		b.log().Error("send message", "error", err, "chat_id", chatID)
	}
}

// handleMessage dispatches a text message's command by exact text match. A
// free-text reply from a user with a pending dialog (e.g. the create-TTN
// weight prompt) is routed to handlePendingReply first; only when that
// reports the message wasn't consumed does the command switch below run.
func (b *Bot) handleMessage(ctx context.Context, m *Message) {
	if m.From != nil && b.Pending != nil {
		if p, ok := b.Pending.Get(m.From.ID); ok {
			if b.handlePendingReply(ctx, m, p) {
				return
			}
		}
	}

	switch m.Text {
	case "/start":
		var userID int64
		username := ""
		if m.From != nil {
			userID = m.From.ID
			username = m.From.Username
		}
		if err := b.Store.RegisterBotUser(ctx, userID, m.Chat.ID, username); err != nil {
			b.log().Error("register bot user", "error", err, "tg_user_id", userID)
			return
		}
		b.send(ctx, m.Chat.ID, startMessage)
	case "/review":
		b.cmdReview(ctx, m.Chat.ID)
	case "/orders":
		b.cmdOrders(ctx, m.Chat.ID)
	default:
		// Unknown command or plain text: silently ignored for now.
	}
}

// handleCallback dispatches an inline-keyboard button press by the prefix of
// its callback data (before the first ":"). Each action is responsible for
// answering its own callback query, since the answer text and timing differ
// per action (some prompt for input instead of answering immediately). Only
// the default branch answers here, so unrecognized callbacks still stop
// Telegram's loading spinner.
func (b *Bot) handleCallback(ctx context.Context, cq *CallbackQuery) {
	action := strings.SplitN(cq.Data, ":", 2)[0]
	switch action {
	case "confirm_review":
		contactID, err := parseCallbackOrderID(cq.Data)
		if err != nil {
			b.answerCallback(ctx, cq, invalidRequestMessage)
			return
		}
		b.actionConfirmReview(ctx, cq, contactID)
	case "create_ttn", "create_ttn_confirm", "create_ttn_cancel", "delete_ttn", "delete_ttn_confirm":
		// A malformed/missing order id must still answer the callback here
		// rather than falling through unanswered; the parsed id is then
		// handed to the matching action, which answers on every other path.
		orderID, err := parseCallbackOrderID(cq.Data)
		if err != nil {
			b.answerCallback(ctx, cq, invalidRequestMessage)
			return
		}
		switch action {
		case "create_ttn":
			b.actionCreateTTNStart(ctx, cq, orderID)
		case "create_ttn_confirm":
			b.actionCreateTTNConfirm(ctx, cq, orderID)
		case "create_ttn_cancel":
			b.actionCreateTTNCancel(ctx, cq, orderID)
		case "delete_ttn":
			b.actionDeleteTTNStart(ctx, cq, orderID)
		case "delete_ttn_confirm":
			b.actionDeleteTTNConfirm(ctx, cq, orderID)
		}
	case "set_status":
		// "set_status:<id>:<status>" needs a second split after the action
		// prefix is peeled off (parseSetStatusCallback does that).
		parts := strings.SplitN(cq.Data, ":", 2)
		rest := ""
		if len(parts) == 2 {
			rest = parts[1]
		}
		orderID, status, err := parseSetStatusCallback(rest)
		if err != nil {
			b.answerCallback(ctx, cq, invalidRequestMessage)
			return
		}
		b.actionSetStatusStart(ctx, cq, orderID, status)
	case "set_status_confirm", "set_status_cancel":
		orderID, err := parseCallbackOrderID(cq.Data)
		if err != nil {
			b.answerCallback(ctx, cq, invalidRequestMessage)
			return
		}
		if action == "set_status_confirm" {
			b.actionSetStatusConfirm(ctx, cq, orderID)
		} else {
			b.actionSetStatusCancel(ctx, cq, orderID)
		}
	default:
		b.answerCallback(ctx, cq, "")
	}
}
