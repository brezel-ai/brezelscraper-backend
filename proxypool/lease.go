package proxypool

// Lease holds a single proxy URL acquired from the Pool. The caller MUST
// call exactly one of ReportSuccess or ReportFailure on a Lease before
// discarding it — without that signal the pool cannot track health.
//
// ReportSuccess / ReportFailure are implemented in subsequent commits;
// the Lease type is introduced here so Acquire has a return shape.
type Lease struct {
	URL string

	pool *Pool
	e    *entry
}
