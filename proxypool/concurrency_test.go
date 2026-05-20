package proxypool

import (
	"sync"
	"testing"
	"time"
)

// TestAcquire_ConcurrentCallersDoNotPanicOrDeadlock fires N goroutines that
// each Acquire + report-outcome in a tight loop. With go test -race, mutex
// violations or unsafe entry mutations would surface here.
//
// We deliberately do NOT assert on final per-entry counts — the rotation
// is non-deterministic under concurrent Acquire and the goal is mutex /
// state-machine integrity, not a specific failure distribution. The
// totalOps sanity check at the end catches the trivial "every call
// returned ErrPoolExhausted" regression.
func TestAcquire_ConcurrentCallersDoNotPanicOrDeadlock(t *testing.T) {
	const (
		nGoroutines = 32
		nIterations = 200
	)
	p, err := New([]string{"http://a", "http://b", "http://c"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(nGoroutines)
	for g := range nGoroutines {
		_ = g
		go func() {
			defer wg.Done()
			for i := range nIterations {
				lease, err := p.Acquire()
				if err != nil {
					// Acceptable if every entry happens to be cooling at the
					// same instant; very unlikely with healthy entries.
					continue
				}
				if i%5 == 0 {
					lease.ReportFailure(SoftReject)
				} else {
					lease.ReportSuccess()
				}
			}
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock: goroutines did not finish within 10s")
	}

	// Sanity: cumulative bookkeeping must add up.
	s := p.Stats()
	var totalOps int64
	for _, e := range s.Entries {
		totalOps += e.TotalSuccesses + e.CumulativeFails
	}
	if totalOps == 0 {
		t.Fatal("no operations recorded despite 32×200 iterations")
	}
}

// TestPool_BurnoutScenario simulates the production failure pattern: one of
// three proxies returns the 33-byte stub (SoftReject) every time. The pool
// must cool it out, leave the two healthy proxies serving, and ensure
// "bad" is never in the healthy state at the end of the run.
func TestPool_BurnoutScenario(t *testing.T) {
	clk := newFakeClock()
	p, err := New(
		[]string{"http://good-1", "http://bad", "http://good-2"},
		WithClock(clk),
		WithThresholds(3, 10, time.Minute, 30*time.Minute),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for range 30 {
		lease, err := p.Acquire()
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		if lease.URL == "http://bad" {
			lease.ReportFailure(SoftReject)
		} else {
			lease.ReportSuccess()
		}
		clk.Advance(5 * time.Second)
	}

	s := p.Stats()
	for _, e := range s.Entries {
		if e.Host == "bad" && e.State == "healthy" {
			t.Errorf("bad proxy still healthy after burnout simulation (cons=%d cum=%d)",
				e.ConsecutiveFails, e.CumulativeFails)
		}
	}

	healthyGoods := 0
	for _, e := range s.Entries {
		if (e.Host == "good-1" || e.Host == "good-2") && e.State == "healthy" && e.TotalSuccesses > 0 {
			healthyGoods++
		}
	}
	if healthyGoods != 2 {
		t.Errorf("good proxies serving: got %d, want 2", healthyGoods)
	}
}

// TestPool_FullBurnoutReturnsErrPoolExhausted covers the worst-case path:
// every configured proxy gets quarantined. Acquire must surface
// ErrPoolExhausted rather than spinning or returning a quarantined URL.
func TestPool_FullBurnoutReturnsErrPoolExhausted(t *testing.T) {
	clk := newFakeClock()
	p, err := New(
		[]string{"http://a", "http://b"},
		WithClock(clk),
		WithThresholds(2, 4, time.Hour, time.Hour),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Burn both proxies past their cumulative-quarantine threshold.
	for range 8 {
		lease, err := p.Acquire()
		if err != nil {
			break
		}
		lease.ReportFailure(SoftReject)
	}

	if _, err := p.Acquire(); err == nil {
		t.Fatal("expected ErrPoolExhausted after every entry quarantined")
	}
}
