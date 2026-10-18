package ig

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"igcrm/internal/store"
)

const sample = `{
  "object": "instagram",
  "entry": [{
    "id": "17840000000000001",
    "time": 1760771340000,
    "messaging": [
      {"sender": {"id": "555"}, "recipient": {"id": "17840000000000001"}, "timestamp": 1760771340000,
       "message": {"mid": "m1", "text": "Київ, 45 відділення, розмір M"}},
      {"sender": {"id": "17840000000000001"}, "recipient": {"id": "555"}, "timestamp": 1760771350000,
       "message": {"mid": "m2", "text": "Прийняла!", "is_echo": true}},
      {"sender": {"id": "555"}, "recipient": {"id": "17840000000000001"}, "timestamp": 1760771360000,
       "read": {"mid": "m2"}},
      {"sender": {"id": "555"}, "recipient": {"id": "17840000000000001"}, "timestamp": 1760771370000,
       "message": {"mid": "m3", "is_deleted": true}},
      {"sender": {"id": "555"}, "recipient": {"id": "17840000000000001"}, "timestamp": 1760771380000,
       "message": {"mid": "m4", "attachments": [{"type": "image", "payload": {"url": "https://cdn/x.jpg"}}]}}
    ]
  }]
}`

func sign(body, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	if !VerifySignature([]byte(sample), sign(sample, "s3cret"), "s3cret") {
		t.Fatal("valid signature rejected")
	}
	for _, h := range []string{"", "sha256=zz", sign(sample, "other"), strings.TrimPrefix(sign(sample, "s3cret"), "sha256=")} {
		if VerifySignature([]byte(sample), h, "s3cret") {
			t.Fatalf("bad signature %q accepted", h)
		}
	}
}

func TestParseMessages(t *testing.T) {
	msgs, err := ParseMessages([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages (in, echo, attachment), got %d: %+v", len(msgs), msgs)
	}
	in, echo, att := msgs[0], msgs[1], msgs[2]
	if in.Direction != "in" || in.IGSID != "555" || in.MID != "m1" {
		t.Errorf("incoming parsed wrong: %+v", in)
	}
	if echo.Direction != "out" || echo.IGSID != "555" {
		t.Errorf("echo must be outgoing to client 555: %+v", echo)
	}
	if att.Text != "" || !strings.Contains(string(att.Attachments), "x.jpg") {
		t.Errorf("attachment lost: %+v", att)
	}
	if !in.SentAt.Equal(time.UnixMilli(1760771340000)) {
		t.Errorf("timestamp: %v", in.SentAt)
	}
}

type fakeSaver struct{ got []store.Message }

func (f *fakeSaver) SaveMessage(_ context.Context, m store.Message, _ time.Duration) (bool, error) {
	f.got = append(f.got, m)
	return true, nil
}

func TestHandler(t *testing.T) {
	fs := &fakeSaver{}
	h := &WebhookHandler{AppSecret: "s3cret", VerifyToken: "vt", Store: fs,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	// verify handshake
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/ig/webhook?hub.mode=subscribe&hub.verify_token=vt&hub.challenge=42", nil))
	if rr.Code != 200 || rr.Body.String() != "42" {
		t.Fatalf("verify: %d %q", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/ig/webhook?hub.mode=subscribe&hub.verify_token=nope&hub.challenge=42", nil))
	if rr.Code != 403 {
		t.Fatalf("wrong verify token accepted: %d", rr.Code)
	}

	// unsigned POST
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/ig/webhook", strings.NewReader(sample)))
	if rr.Code != 401 || len(fs.got) != 0 {
		t.Fatalf("unsigned accepted: %d", rr.Code)
	}

	// signed POST
	req := httptest.NewRequest(http.MethodPost, "/ig/webhook", strings.NewReader(sample))
	req.Header.Set("X-Hub-Signature-256", sign(sample, "s3cret"))
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 || len(fs.got) != 3 {
		t.Fatalf("signed: %d, saved %d", rr.Code, len(fs.got))
	}
}

func TestParseGraphTime(t *testing.T) {
	got := ParseGraphTime("2026-10-18T10:09:00+0000")
	if !got.Equal(time.Date(2026, 10, 18, 10, 9, 0, 0, time.UTC)) {
		t.Fatal(got)
	}
}
