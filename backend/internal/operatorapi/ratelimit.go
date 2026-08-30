package operatorapi

import (
	"sync"
	"time"
)

// loginRateLimiter is a basic fixed-window failed-login limiter, kept
// in-process — no new dependency, no Redis, matching this repo's own
// established "not load-bearing" stance on Redis (03-architecture.md) and
// operatorauth's identical reasoning for why sessions aren't stored there
// either. A single self-hosted instance (this project's only deployment
// shape, 01-scope-and-non-goals.md) never needs a shared cross-process
// limiter; resetting on process restart is an acceptable, deliberate
// trade-off for the same reason a stateless session survives one anyway.
//
// Two independent keys are tracked, both must pass: the attempted email
// (bounds how many guesses any single attacker gets against the one real
// operator account, 02-requirements.md's single-operator baseline) and the
// remote IP (bounds a single source's total attempt volume regardless of
// which email it tries, since a self-hosted single-operator email is often
// guessable or known).
type loginRateLimiter struct {
	mu       sync.Mutex
	attempts map[string]*window
	limit    int
	window   time.Duration
}

type window struct {
	count      int
	windowEnds time.Time
}

// newLoginRateLimiter constructs a limiter allowing limit failed attempts
// per key within window before further attempts are refused.
func newLoginRateLimiter(limit int, windowDuration time.Duration) *loginRateLimiter {
	return &loginRateLimiter{
		attempts: make(map[string]*window),
		limit:    limit,
		window:   windowDuration,
	}
}

// allow reports whether a new attempt for key is permitted right now,
// without recording anything — used to check both the email and IP keys
// before doing any password verification work at all.
func (l *loginRateLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.attempts[key]
	if !ok || now.After(w.windowEnds) {
		return true
	}
	return w.count < l.limit
}

// recordFailure counts one failed attempt against key, starting a fresh
// window if none is active or the previous one expired.
func (l *loginRateLimiter) recordFailure(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.attempts[key]
	if !ok || now.After(w.windowEnds) {
		l.attempts[key] = &window{count: 1, windowEnds: now.Add(l.window)}
		return
	}
	w.count++
}

// reset clears key's window entirely — called on a successful login so a
// legitimate operator who mistyped their password a few times isn't left
// partially throttled afterward.
func (l *loginRateLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}

// loginFailureLimit and loginRateWindow are this session's reasoned
// defaults, not a numeric requirement any FR/NFR names: generous enough
// that a real operator mistyping a password a few times never gets locked
// out, tight enough to make online brute-forcing a single-operator account
// impractical.
const (
	loginFailureLimit = 5
	loginRateWindow   = 15 * time.Minute
)
