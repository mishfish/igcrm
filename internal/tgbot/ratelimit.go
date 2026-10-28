package tgbot

import (
	"sync"
	"time"
)

type rateWindow struct {
	count       int
	windowStart time.Time
}

// RateLimiter is a per-user fixed-window request counter: each user may make
// at most limit calls to Allow within each window. It is safe for concurrent use.
type RateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	users  map[int64]rateWindow
	now    func() time.Time
}

// NewRateLimiter returns a limiter allowing limit calls per user per window.
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		limit:  limit,
		window: window,
		users:  make(map[int64]rateWindow),
		now:    time.Now,
	}
}

// Allow records a call by userID and reports whether it is within budget.
// When the current window has elapsed, the user's budget resets.
func (r *RateLimiter) Allow(userID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	w := r.users[userID]
	if now.Sub(w.windowStart) > r.window {
		w = rateWindow{windowStart: now}
	}
	w.count++
	r.users[userID] = w
	return w.count <= r.limit
}
