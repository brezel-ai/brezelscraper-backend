package proxypool

import "sync/atomic"

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
