package tgbot

import (
	"testing"
	"time"
)

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
