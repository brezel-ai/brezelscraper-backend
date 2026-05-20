package proxypool

import (
	"errors"
	"sync"
	"time"
)

// ErrPoolExhausted is returned by Acquire when every entry in the pool is
// either cooling or quarantined. Callers should surface this as a hard
// failure for the current scrape — there is no healthy proxy to use.
var ErrPoolExhausted = errors.New("proxypool: all proxies unavailable")

// ErrEmptyPool is returned by New when constructed with zero URLs. We treat
// this as a programmer error rather than a runtime state because the
// alternative — silently succeeding then returning ErrPoolExhausted on the
// first Acquire — masks misconfiguration.
var ErrEmptyPool = errors.New("proxypool: cannot construct pool with zero URLs")

// Default thresholds. Override via WithThresholds.
const (
	defaultCoolingFailThreshold    = 3
	defaultQuarantineFailThreshold = 10
	defaultBaseCoolDuration        = 30 * time.Second
	defaultMaxCoolDuration         = 30 * time.Minute
)

// Pool is a thread-safe rotating pool of proxy URLs with per-proxy health
// tracking. Construct with New. Callers Acquire a Lease, use the URL, and
// report success/failure exactly once on the Lease before discarding it.
type Pool struct {
	mu      sync.Mutex
	entries []*entry
	cursor  int // round-robin starting index for the next Acquire

	coolingFailThreshold    int
	quarantineFailThreshold int
	baseCoolDuration        time.Duration
	maxCoolDuration         time.Duration

	clock clock
}

// New constructs a Pool over the supplied proxy URLs. Returns ErrEmptyPool
// when urls is nil or empty.
func New(urls []string, opts ...Option) (*Pool, error) {
	if len(urls) == 0 {
		return nil, ErrEmptyPool
	}
	p := &Pool{
		entries:                 make([]*entry, 0, len(urls)),
		coolingFailThreshold:    defaultCoolingFailThreshold,
		quarantineFailThreshold: defaultQuarantineFailThreshold,
		baseCoolDuration:        defaultBaseCoolDuration,
		maxCoolDuration:         defaultMaxCoolDuration,
		clock:                   realClock{},
	}
	for _, opt := range opts {
		opt(p)
	}
	now := p.clock.Now()
	for _, u := range urls {
		p.entries = append(p.entries, &entry{
			url:              u,
			state:            stateHealthy,
			lastTransitionAt: now,
		})
	}
	return p, nil
}
