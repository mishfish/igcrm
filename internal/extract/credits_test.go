package extract

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type recNotifier struct{ alerts []string }

func (n *recNotifier) Alert(_ context.Context, text string) { n.alerts = append(n.alerts, text) }

var creditsErr = errors.New(`claude: POST "https://api.anthropic.com/v1/messages": 400 Bad Request {"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."}}`)

func TestCreditsAlert_OnceThenEvery6hThenRecovery(t *testing.T) {
	n := &recNotifier{}
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	w := &Worker{Notify: n, now: func() time.Time { return now }}
	ctx := context.Background()

	w.noteLLMResult(ctx, creditsErr)
	w.noteLLMResult(ctx, creditsErr) // other contacts in the same tick
	now = now.Add(2 * time.Hour)
	w.noteLLMResult(ctx, creditsErr)
	if len(n.alerts) != 1 || !strings.Contains(n.alerts[0], "закінчились кредити") {
		t.Fatalf("want exactly one out-of-credits alert, got %q", n.alerts)
	}

	now = now.Add(5 * time.Hour) // 7h since the first alert
	w.noteLLMResult(ctx, creditsErr)
	if len(n.alerts) != 2 {
		t.Fatalf("want a reminder after 6h, got %q", n.alerts)
	}

	w.noteLLMResult(ctx, nil)
	w.noteLLMResult(ctx, nil)
	if len(n.alerts) != 3 || !strings.Contains(n.alerts[2], "знову працюють") {
		t.Fatalf("want exactly one recovery message, got %q", n.alerts)
	}

	w.noteLLMResult(ctx, creditsErr) // ran out again -> alert immediately
	if len(n.alerts) != 4 {
		t.Fatalf("want a fresh alert after running out again, got %q", n.alerts)
	}
}

func TestCreditsAlert_IgnoresOtherErrorsAndNormalSuccess(t *testing.T) {
	n := &recNotifier{}
	w := &Worker{Notify: n}
	ctx := context.Background()
	w.noteLLMResult(ctx, nil)
	w.noteLLMResult(ctx, errors.New("claude: 529 overloaded_error"))
	w.noteLLMResult(ctx, errors.New("context deadline exceeded"))
	if len(n.alerts) != 0 {
		t.Fatalf("no alerts expected, got %q", n.alerts)
	}
}

func TestCreditsAlert_NilNotifierIsSafe(t *testing.T) {
	(&Worker{}).noteLLMResult(context.Background(), creditsErr)
}
