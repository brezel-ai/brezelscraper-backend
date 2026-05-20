package proxypool

import (
	"sync/atomic"
	"time"
)

// Lease holds a single proxy URL acquired from the Pool. The caller MUST
// call exactly one of ReportSuccess or ReportFailure on a Lease before
// discarding it — without that signal the pool cannot track health.
//
// Calling either method more than once is a no-op (idempotent guard via
// the reported flag). The Lease is returned by pointer from Pool.Acquire
// because the atomic guard field cannot safely be copied.
type Lease struct {
	URL string

	pool     *Pool
	e        *entry
	reported atomic.Bool
}

// ReportSuccess marks the proxy as having served a successful request. It
// resets the consecutive-failure counter and, if the entry was cooling
// with an expired deadline, promotes it back to healthy. Quarantined
// entries stay quarantined — a process restart is required to recover them.
//
// Safe to call multiple times; subsequent calls are no-ops.
func (l *Lease) ReportSuccess() {
	if !l.reported.CompareAndSwap(false, true) {
		return
	}
	l.pool.mu.Lock()
	defer l.pool.mu.Unlock()
	now := l.pool.clock.Now()

	l.e.totalSuccesses++
	l.e.consecutiveFails = 0

	if l.e.state == stateCooling && !now.Before(l.e.nextOK) {
		l.e.state = stateHealthy
		l.e.lastTransitionAt = now
	}
}

// ReportFailure marks the proxy as having failed a request. The reason
// classifies the failure category and drives the state transition:
//
//	BlockedByTarget → immediate quarantine (proxy IP is burned)
//	other reasons   → increment counters; if consecutive ≥ coolingFailThreshold,
//	                  transition to cooling with exponential backoff;
//	                  if cumulative ≥ quarantineFailThreshold, quarantine
//
// Safe to call multiple times; subsequent calls are no-ops.
func (l *Lease) ReportFailure(reason FailureReason) {
	if !l.reported.CompareAndSwap(false, true) {
		return
	}
	l.pool.mu.Lock()
	defer l.pool.mu.Unlock()
	now := l.pool.clock.Now()

	l.e.consecutiveFails++
	l.e.cumulativeFails++
	l.e.lastFailureReason = reason

	if reason == BlockedByTarget {
		l.transitionLocked(stateQuarantined, now)
		return
	}

	if l.e.cumulativeFails >= int64(l.pool.quarantineFailThreshold) {
		l.transitionLocked(stateQuarantined, now)
		return
	}

	if l.e.consecutiveFails >= l.pool.coolingFailThreshold {
		l.e.nextOK = now.Add(l.pool.coolDuration(l.e.consecutiveFails))
		l.transitionLocked(stateCooling, now)
	}
}

// transitionLocked updates state + lastTransitionAt only when the state
// actually changes. Called with l.pool.mu held.
func (l *Lease) transitionLocked(next state, now time.Time) {
	if l.e.state == next {
		return
	}
	l.e.state = next
	l.e.lastTransitionAt = now
}
