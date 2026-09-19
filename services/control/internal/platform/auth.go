package platform

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Authenticator resolves the caller's subject from a bearer credential: an
// operator-configured local token mapping, or an OIDC token verified against
// the configured issuer and audience.
type Authenticator struct {
	Keys     map[string]string
	Verifier *oidc.IDTokenVerifier
	Mode     string
}

func NewAuthenticator(ctx context.Context, c Config) (*Authenticator, error) {
	a := &Authenticator{Keys: c.APIKeys, Mode: "local-bearer"}
	if c.OIDCIssuer != "" {
		p, e := oidc.NewProvider(ctx, c.OIDCIssuer)
		if e != nil {
			return nil, e
		}
		a.Verifier = p.Verifier(&oidc.Config{ClientID: c.OIDCAudience})
		a.Mode = "oidc"
	}
	return a, nil
}
func (a *Authenticator) Subject(r *http.Request) (string, error) {
	raw := r.Header.Get("Authorization")
	if !strings.HasPrefix(raw, "Bearer ") || len(raw) > 16384 {
		return "", errors.New("authentication required")
	}
	token := strings.TrimPrefix(raw, "Bearer ")
	if a.Verifier != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		id, e := a.Verifier.Verify(ctx, token)
		if e != nil || id.Subject == "" || len(id.Subject) > 256 {
			return "", errors.New("invalid token")
		}
		return id.Subject, nil
	}
	for expected, subject := range a.Keys {
		if subtle.ConstantTimeCompare([]byte(expected), []byte(token)) == 1 {
			return subject, nil
		}
	}
	return "", errors.New("invalid token")
}

type bucket struct {
	tokens float64
	last   time.Time
}

// RateLimiter is a per-key token bucket: burst 120, refill 2 per second,
// bounded to 10,000 keys per process.
type RateLimiter struct {
	mu      sync.Mutex
	entries map[string]bucket
}

func NewRateLimiter() *RateLimiter { return &RateLimiter{entries: map[string]bucket{}} }
func (l *RateLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.entries[key]
	if !ok {
		if len(l.entries) >= 10000 {
			for k, v := range l.entries {
				if now.Sub(v.last) > time.Hour {
					delete(l.entries, k)
				}
			}
			if len(l.entries) >= 10000 {
				return false
			}
		}
		b = bucket{tokens: 120, last: now}
	}
	b.tokens += now.Sub(b.last).Seconds() * 2
	if b.tokens > 120 {
		b.tokens = 120
	}
	b.last = now
	ok = b.tokens >= 1
	if ok {
		b.tokens--
	}
	l.entries[key] = b
	return ok
}
