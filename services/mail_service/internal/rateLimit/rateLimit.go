// Package ratelimit is a small in-memory fixed-window limiter for the public routes.
// It is per process; with several replicas the effective limit multiplies, which is acceptable for this project.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter allows at most Limit events per key and Window.
type Limiter struct {
	Limit  int
	Window time.Duration
	mu     sync.Mutex
	hits   map[string]*bucket
	now    func() time.Time
}

type bucket struct {
	start time.Time
	n     int
}

// New creates a limiter.
func New(limit int, window time.Duration) *Limiter {
	return &Limiter{Limit: limit, Window: window, hits: map[string]*bucket{}, now: time.Now}
}

// Allow records an event and reports whether it is within the limit. retryAfter is set when it is not.
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.hits) > 10_000 { // bound memory
		for k, b := range l.hits {
			if now.Sub(b.start) >= l.Window {
				delete(l.hits, k)
			}
		}
	}
	b := l.hits[key]
	if b == nil || now.Sub(b.start) >= l.Window {
		b = &bucket{start: now}
		l.hits[key] = b
	}
	b.n++
	if b.n > l.Limit {
		return false, b.start.Add(l.Window).Sub(now)
	}
	return true, 0
}
