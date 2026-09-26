package operatorapi

import "sync"

// sseConnLimiter caps how many concurrent GET /api/v1/events streams a
// single authenticated operator may hold open, plus a global ceiling across
// all operators — kept in-process, matching loginRateLimiter's identical
// "not load-bearing" stance on Redis (03-architecture.md): a single
// self-hosted instance never needs a shared cross-process limiter, and
// resetting on process restart just means every held connection was already
// severed anyway.
//
// Without this, RequireOperator's session-cookie auth alone lets one
// authenticated operator open unbounded concurrent SSE streams (each its
// own goroutine, heartbeat ticker, and livefeed.Hub subscriber slot), which
// is a real resource-exhaustion path on the same authenticated surface
// every other operatorapi route sits behind.
type sseConnLimiter struct {
	mu          sync.Mutex
	perOperator map[string]int
	total       int
	perOpLimit  int
	globalLimit int
}

// newSSEConnLimiter constructs a limiter allowing at most perOpLimit
// concurrent streams for any one operator, and at most globalLimit
// concurrent streams in total across all operators.
func newSSEConnLimiter(perOpLimit, globalLimit int) *sseConnLimiter {
	return &sseConnLimiter{
		perOperator: make(map[string]int),
		perOpLimit:  perOpLimit,
		globalLimit: globalLimit,
	}
}

// acquire reserves one connection slot for operatorID and reports whether
// it was granted. Callers that receive true must call release(operatorID)
// exactly once when the connection ends; callers that receive false must
// not.
func (l *sseConnLimiter) acquire(operatorID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total >= l.globalLimit || l.perOperator[operatorID] >= l.perOpLimit {
		return false
	}
	l.perOperator[operatorID]++
	l.total++
	return true
}

// release frees the connection slot acquire granted for operatorID.
func (l *sseConnLimiter) release(operatorID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.perOperator[operatorID]--
	if l.perOperator[operatorID] <= 0 {
		delete(l.perOperator, operatorID)
	}
	l.total--
}

// sseConnsPerOperator and sseConnsGlobal are this session's reasoned
// defaults, not a numeric requirement any FR/NFR names: generous enough
// that one operator with several open dashboard tabs/devices never gets
// throttled during normal use, tight enough that a single compromised or
// misbehaving session can't exhaust the process's connection/goroutine
// budget by opening streams in a loop.
const (
	sseConnsPerOperator = 4
	sseConnsGlobal      = 50
)
