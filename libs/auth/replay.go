package praxisauth

import (
	"sync"
	"time"
)

// replayCache remembers the tokens a verifier has already accepted, so each token is usable once.
type replayCache struct {
	mu        sync.Mutex
	seen      map[string]time.Time
	nextSweep time.Time
}

func newReplayCache() *replayCache {
	return &replayCache{seen: map[string]time.Time{}}
}

// firstUse records the token and reports whether it was not seen before.
// validUntil is the last moment the token passes the time checks.
func (r *replayCache) firstUse(issuer, jti string, validUntil, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if now.After(r.nextSweep) {
		for k, until := range r.seen {
			if !until.After(now) {
				delete(r.seen, k)
			}
		}
		r.nextSweep = now.Add(MaxServiceTokenLifetime)
	}

	// JTI is unique per issuer (not globally)
	// Create Key with 0x00 separation as 'service0' with JTI '1' and 'service with JTI '01' use not the same key
	// (override each other).
	key := issuer + "\x00" + jti

	// replay detection
	// ok: issuer and jti known
	// if pair still valid: reject (return false)
	if until, ok := r.seen[key]; ok && until.After(now) {
		return false
	}

	r.seen[key] = validUntil
	return true
}
