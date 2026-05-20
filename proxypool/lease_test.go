package proxypool

import (
	"testing"
	"time"
)

func TestReportSuccess_ResetsConsecutiveFails(t *testing.T) {
	p, err := New([]string{"http://a"}, WithClock(newFakeClock()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	p.mu.Lock()
	p.entries[0].consecutiveFails = 2
	p.mu.Unlock()

	lease, err := p.Acquire()
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	lease.ReportSuccess()

	p.mu.Lock()
	defer p.mu.Unlock()
	if got := p.entries[0].consecutiveFails; got != 0 {
		t.Errorf("consecutiveFails: got %d, want 0", got)
	}
	if got := p.entries[0].totalSuccesses; got != 1 {
		t.Errorf("totalSuccesses: got %d, want 1", got)
	}
}

func TestReportSuccess_PromotesCoolingToHealthy(t *testing.T) {
	clk := newFakeClock()
	p, err := New([]string{"http://a"}, WithClock(clk))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Cooling with an already-expired deadline so Acquire hands it out.
	p.mu.Lock()
	p.entries[0].state = stateCooling
	p.entries[0].nextOK = clk.Now().Add(-time.Second)
	p.mu.Unlock()

	lease, err := p.Acquire()
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	lease.ReportSuccess()

	p.mu.Lock()
	defer p.mu.Unlock()
	if got := p.entries[0].state; got != stateHealthy {
		t.Errorf("state after success: got %s, want healthy", got)
	}
}

func TestLease_ReportSuccessTwiceIsNoOp(t *testing.T) {
	p, err := New([]string{"http://a"}, WithClock(newFakeClock()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	lease, _ := p.Acquire()
	lease.ReportSuccess()
	lease.ReportSuccess()

	p.mu.Lock()
	defer p.mu.Unlock()
	if got := p.entries[0].totalSuccesses; got != 1 {
		t.Errorf("double-report counted: totalSuccesses = %d, want 1", got)
	}
}
