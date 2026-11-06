package praxisauth

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// ErrUnauthenticated means the request has no acceptable user token. Other errors are infrastructure failures.
var ErrUnauthenticated = errors.New("unauthenticated")

// TokenChecker asks the revocation state of a token (the middleware operation tokenCheck).
type TokenChecker func(ctx context.Context, jti, sub string, iat time.Time) (bool, error)

// Authenticator verifies user tokens for a backend service (docs/api/internal.md section 1):
// signature, issuer, audience, time claims, scope, mfa and the revocation state.
type Authenticator struct {
	Keys  KeySet
	Check TokenChecker
	TTL   time.Duration // positive results are cached this long; zero means 10 s, negative disables the cache
	Now   func() time.Time
	mu    sync.Mutex
	cache map[string]time.Time
}

// Authenticate checks an Authorization header value ("Bearer ..."). A setup-scope token is only accepted if
// allowSetup is set (routes marked scope: setup). A full-scope token must carry mfa=true.
func (a *Authenticator) Authenticate(ctx context.Context, header string, allowSetup bool) (*UserClaims, error) {
	if len(header) < 8 || header[:7] != "Bearer " {
		return nil, ErrUnauthenticated
	}
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	c, err := VerifyUser(header[7:], a.Keys, AudienceAPI, now)
	if err != nil {
		return nil, ErrUnauthenticated
	}
	switch {
	case c.Scope == ScopeSetup && allowSetup:
	case c.Scope == ScopeFull && c.MFA:
	default:
		return nil, ErrUnauthenticated
	}
	ttl := a.TTL
	if ttl == 0 {
		ttl = 10 * time.Second
	}
	if ttl > 0 {
		a.mu.Lock()
		until, ok := a.cache[c.JTI]
		a.mu.Unlock()
		if ok && now.Before(until) {
			return c, nil
		}
	}
	valid, err := a.Check(ctx, c.JTI, c.Subject, time.Unix(c.IssuedAt, 0))
	if err != nil {
		return nil, fmt.Errorf("token check: %w", err)
	}
	if !valid {
		return nil, ErrUnauthenticated
	}
	if ttl > 0 {
		a.mu.Lock()
		if a.cache == nil {
			a.cache = map[string]time.Time{}
		}
		if len(a.cache) > 10_000 {
			for k, v := range a.cache {
				if !now.Before(v) {
					delete(a.cache, k)
				}
			}
		}
		a.cache[c.JTI] = now.Add(ttl)
		a.mu.Unlock()
	}
	return c, nil
}

// RemoteKeySet resolves key ids from the JWKS endpoint of user_service. An unknown key id triggers one refresh,
// at most once per MinRefresh (key rotation without a restart, no request storm from forged key ids).
type RemoteKeySet struct {
	URL        string
	HTTP       *http.Client
	MinRefresh time.Duration // default 1 minute

	mu        sync.Mutex
	keys      StaticKeySet
	lastFetch time.Time
}

// Key implements KeySet.
func (r *RemoteKeySet) Key(kid string) (ed25519.PublicKey, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if k, ok := r.keys[kid]; ok {
		return k, true
	}
	min := r.MinRefresh
	if min == 0 {
		min = time.Minute
	}
	if time.Since(r.lastFetch) < min && r.keys != nil {
		return nil, false
	}
	r.lastFetch = time.Now()
	if keys, err := r.fetch(); err == nil {
		r.keys = keys
	}
	k, ok := r.keys[kid]
	return k, ok
}

// Load fetches the key set now (start-up). Errors are returned so the caller can retry.
func (r *RemoteKeySet) Load() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	keys, err := r.fetch()
	if err != nil {
		return err
	}
	r.keys, r.lastFetch = keys, time.Now()
	return nil
}

func (r *RemoteKeySet) fetch() (StaticKeySet, error) {
	c := r.HTTP
	if c == nil {
		c = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := c.Get(r.URL) //nolint:noctx // short-lived refresh with a client timeout
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks: status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	return ParseJWKS(b)
}
