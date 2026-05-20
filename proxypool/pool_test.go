package proxypool

import (
	"errors"
	"testing"
	"time"
)

func TestNew_EmptyURLsReturnsErrEmptyPool(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, ErrEmptyPool) {
		t.Fatalf("nil urls: want ErrEmptyPool, got %v", err)
	}
	if _, err := New([]string{}); !errors.Is(err, ErrEmptyPool) {
		t.Fatalf("empty urls: want ErrEmptyPool, got %v", err)
	}
}

func TestNew_AllEntriesStartHealthy(t *testing.T) {
	urls := []string{"http://a", "http://b", "http://c"}
	p, err := New(urls)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(p.entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(p.entries))
	}
	for i, e := range p.entries {
		if e.state != stateHealthy {
			t.Errorf("entry %d (%s): state = %s, want healthy", i, e.url, e.state)
		}
		if e.consecutiveFails != 0 || e.cumulativeFails != 0 {
			t.Errorf("entry %d: counters should be zero, got cons=%d cum=%d",
				i, e.consecutiveFails, e.cumulativeFails)
		}
	}
}

// fakeClock is a manually-advanced clock for testing cooling timers without
// time.Sleep. Tests construct with newFakeClock and advance via Advance.
type fakeClock struct {
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }
