package ig

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"igcrm/internal/store"
)

// VerifySignature перевіряє X-Hub-Signature-256: "sha256=<hex hmac(body, appSecret)>".
func VerifySignature(body []byte, header, appSecret string) bool {
	sig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

type payload struct {
	Object string `json:"object"`
	Entry  []struct {
		ID        string            `json:"id"` // твій IG professional account id
		Messaging []json.RawMessage `json:"messaging"`
	} `json:"entry"`
}

type event struct {
	Sender    struct{ ID string } `json:"sender"`
	Recipient struct{ ID string } `json:"recipient"`
	Timestamp int64               `json:"timestamp"` // ms
	Message   *struct {
		Mid         string          `json:"mid"`
		Text        string          `json:"text"`
		IsEcho      bool            `json:"is_echo"`
		IsDeleted   bool            `json:"is_deleted"`
		Attachments json.RawMessage `json:"attachments"`
	} `json:"message"`
}

// ParseMessages витягує з вебхука повідомлення в обидва боки.
// Вхідні: sender = клієнт. Echo (твої відповіді з телефону): is_echo, sender = ти, recipient = клієнт.
// Реакції, прочитання, редагування тощо ігноруються.
func ParseMessages(body []byte) ([]store.Message, error) {
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	var out []store.Message
	for _, e := range p.Entry {
		own := e.ID
		for _, raw := range e.Messaging {
			var ev event
			if err := json.Unmarshal(raw, &ev); err != nil || ev.Message == nil {
				continue
			}
			m := ev.Message
			if m.Mid == "" || m.IsDeleted {
				continue
			}
			msg := store.Message{
				MID:         m.Mid,
				Text:        m.Text,
				Attachments: m.Attachments,
				SentAt:      time.UnixMilli(ev.Timestamp).UTC(),
				Source:      "webhook",
				Raw:         raw,
			}
			if m.IsEcho || (own != "" && ev.Sender.ID == own) {
				msg.Direction, msg.IGSID = "out", ev.Recipient.ID
			} else {
				msg.Direction, msg.IGSID = "in", ev.Sender.ID
			}
			if msg.IGSID == "" || msg.IGSID == own {
				continue
			}
			if ev.Timestamp == 0 {
				msg.SentAt = time.Now().UTC()
			}
			out = append(out, msg)
		}
	}
	return out, nil
}

type MessageSaver interface {
	SaveMessage(ctx context.Context, m store.Message, debounce time.Duration) (bool, error)
}

type WebhookHandler struct {
	AppSecret   string
	VerifyToken string
	Store       MessageSaver
	Debounce    time.Duration
	Log         *slog.Logger
}

func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.verify(w, r)
	case http.MethodPost:
		h.receive(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// verify — хендшейк при налаштуванні вебхука в дашборді Meta.
func (h *WebhookHandler) verify(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("hub.mode") == "subscribe" &&
		hmac.Equal([]byte(q.Get("hub.verify_token")), []byte(h.VerifyToken)) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, q.Get("hub.challenge"))
		return
	}
	h.Log.Warn("webhook verify failed", "mode", q.Get("hub.mode"))
	http.Error(w, "forbidden", http.StatusForbidden)
}

const maxWebhookBody = 1 << 20 // 1MB

func (h *WebhookHandler) receive(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody+1))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if len(body) > maxWebhookBody {
		// довший за ліміт тіло не можна безпечно обрізати: обрізаний байт-рядок
		// провалить HMAC і буде помилково залогований як "bad signature"
		h.Log.Error("webhook body exceeds limit", "limit", maxWebhookBody)
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	if !VerifySignature(body, r.Header.Get("X-Hub-Signature-256"), h.AppSecret) {
		h.Log.Warn("webhook bad signature (перевір IG_APP_SECRET: потрібен Instagram app secret)")
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	msgs, err := ParseMessages(body)
	if err != nil {
		h.Log.Warn("webhook parse", "err", err)
		w.WriteHeader(http.StatusOK) // кривий payload ретраями не виправиться
		return
	}
	for _, m := range msgs {
		inserted, err := h.Store.SaveMessage(r.Context(), m, h.Debounce)
		if err != nil {
			// 500 → Meta повторить доставку пізніше; дублікати відсіються по mid
			h.Log.Error("save message", "err", err, "mid", m.MID)
			http.Error(w, "storage error", http.StatusInternalServerError)
			return
		}
		if inserted {
			h.Log.Info("message", "dir", m.Direction, "igsid", m.IGSID)
		}
	}
	w.WriteHeader(http.StatusOK)
}
