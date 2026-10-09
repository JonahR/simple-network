package network

import (
	"sync"
	"time"
)

// Breaker states.
const (
	BreakerClosed   = "closed"    // Sending to the issuer normally
	BreakerOpen     = "open"      // Too many failures; not sending
	BreakerHalfOpen = "half_open" // Letting one request through to test the issuer
)

// Breaker is a per-issuer circuit breaker (D8). After Threshold consecutive
// failures it opens and the network declines without calling the issuer. After
// Cooldown it lets one trial request through; success closes it again.
type Breaker struct {
	Threshold int
	Cooldown  time.Duration
	now       func() time.Time

	mu        sync.Mutex
	failures  int
	openUntil time.Time
	trial     bool // a half-open trial request is in flight
}

// NewBreaker creates a closed breaker.
func NewBreaker(threshold int, cooldown time.Duration, now func() time.Time) *Breaker {
	return &Breaker{Threshold: threshold, Cooldown: cooldown, now: now}
}

// Allow reports whether a request may be sent to the issuer.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state() {
	case BreakerClosed:
		return true
	case BreakerHalfOpen:
		if b.trial {
			return false
		}
		b.trial = true
		return true
	default:
		return false
	}
}

// Success records a good response and closes the breaker.
func (b *Breaker) Success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures, b.trial, b.openUntil = 0, false, time.Time{}
}

// Failure records a timeout or error. It returns true if this opened the breaker.
func (b *Breaker) Failure() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	wasOpen := b.state() != BreakerClosed
	b.failures++
	b.trial = false
	if wasOpen || b.failures >= b.Threshold {
		b.openUntil = b.now().Add(b.Cooldown)
		return !wasOpen
	}
	return false
}

// State returns the breaker's current state.
func (b *Breaker) State() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state()
}

func (b *Breaker) state() string {
	switch {
	case b.openUntil.IsZero():
		return BreakerClosed
	case b.now().Before(b.openUntil):
		return BreakerOpen
	default:
		return BreakerHalfOpen
	}
}
