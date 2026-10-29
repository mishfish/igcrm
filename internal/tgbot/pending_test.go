package tgbot

import (
	"testing"
	"time"
)

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

// TestPendingStore_TakeGetsAndRemovesUnderOneLock verifies Take both returns
// the pending action and removes it, so a second Take (or Get) afterwards
// finds nothing — the atomic check-and-consume this money-critical confirm
// flows rely on instead of a separate Get+Clear pair.
func TestPendingStore_TakeGetsAndRemovesUnderOneLock(t *testing.T) {
	p := NewPendingStore(5 * time.Minute)
	p.Set(1, PendingAction{Kind: "create_ttn", OrderID: 9, Step: "confirm"})

	a, ok := p.Take(1)
	if !ok || a.OrderID != 9 {
		t.Fatalf("take: %+v %v", a, ok)
	}
	if _, ok := p.Take(1); ok {
		t.Fatal("expected a second Take to find nothing")
	}
	if _, ok := p.Get(1); ok {
		t.Fatal("expected Get to find nothing after Take")
	}
}

// TestPendingStore_TakeOfExpiredActionReportsAbsent mirrors Get's expiry
// behavior: an expired entry is removed and reported as absent, not handed
// back to the caller as if it were still live.
func TestPendingStore_TakeOfExpiredActionReportsAbsent(t *testing.T) {
	p := NewPendingStore(time.Minute)
	fakeNow := time.Now()
	p.now = func() time.Time { return fakeNow }
	p.Set(1, PendingAction{Kind: "create_ttn", OrderID: 9})
	fakeNow = fakeNow.Add(2 * time.Minute)
	if _, ok := p.Take(1); ok {
		t.Fatal("expected an expired pending action to be reported absent")
	}
}
