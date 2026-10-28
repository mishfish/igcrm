package tgbot

import (
	"sync"
	"time"
)

// PendingAction describes a multi-step dialog a user is in the middle of.
type PendingAction struct {
	Kind      string
	OrderID   int64
	Step      string
	Weight    float64
	Status    string
	CreatedAt time.Time
}

// PendingStore keeps at most one pending action per user, expiring entries
// older than its TTL. It is safe for concurrent use.
type PendingStore struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[int64]PendingAction
	now   func() time.Time
}

// NewPendingStore returns an empty store whose entries expire after ttl.
func NewPendingStore(ttl time.Duration) *PendingStore {
	return &PendingStore{
		ttl:   ttl,
		items: make(map[int64]PendingAction),
		now:   time.Now,
	}
}

// Set stores a as the pending action for userID, replacing any previous one.
// CreatedAt is stamped with the store's clock.
func (p *PendingStore) Set(userID int64, a PendingAction) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a.CreatedAt = p.now()
	p.items[userID] = a
}

// Get returns the pending action for userID. An expired action is removed and
// reported as absent.
func (p *PendingStore) Get(userID int64) (PendingAction, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, ok := p.items[userID]
	if !ok {
		return PendingAction{}, false
	}
	if p.now().Sub(a.CreatedAt) > p.ttl {
		delete(p.items, userID)
		return PendingAction{}, false
	}
	return a, true
}

// Clear removes any pending action for userID.
func (p *PendingStore) Clear(userID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.items, userID)
}

// Take atomically gets and removes the pending action for userID under a
// single lock acquisition, so a caller can check-and-consume it without a
// window between Get and Clear in which a concurrent caller could also see
// it as present. An expired action is removed and reported as absent, same
// as Get. Money-critical confirm flows (e.g. create/cancel TTN) must use
// Take instead of Get+Clear so the "already consumed" guard against a
// redelivered/duplicate update holds even if dispatch ever stops being
// single-threaded.
func (p *PendingStore) Take(userID int64) (PendingAction, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, ok := p.items[userID]
	if !ok {
		return PendingAction{}, false
	}
	delete(p.items, userID)
	if p.now().Sub(a.CreatedAt) > p.ttl {
		return PendingAction{}, false
	}
	return a, true
}
