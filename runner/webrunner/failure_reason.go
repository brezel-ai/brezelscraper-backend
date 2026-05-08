package webrunner

import (
	"fmt"
	"strings"
)

// sanitizeSeedError translates a raw seed-level scrape error into a short,
// user-facing failure_reason that's safe to surface in the UI and useful for
// support triage (the user can read it back; on-call can correlate to the
// raw error in Loki via job_id). Always log the raw error separately at
// ERROR with the job ID — this function is for the failure_reason field
// that lands in the jobs table and shows in the frontend.
//
// The sanitization rules favour:
//   - SHORT (one line, no stack traces, no full URLs)
//   - DOMAIN-RECOGNIZABLE (mention "Proxy", "DNS", "timeout" rather than
//     leaking internal library names like "playwright" or "Frame.Goto")
//   - DETERMINISTIC (a given error class always maps to the same string —
//     so log aggregators and support runbooks can match)
//
// Strings start with a capital letter and have no leading prefix because
// the frontend already renders them as `Job failed. Reason: <message>`;
// adding a "Scraping aborted:" prefix here would produce the redundant
// "Reason: Scraping aborted: proxy connection failed".
//
// Unrecognized errors fall through to a generic "Scrape engine error" —
// never the raw err.Error() (which can leak proxy URLs, internal
// hostnames, or stack-trace fragments).
func sanitizeSeedError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "ERR_PROXY_CONNECTION_FAILED"):
		return "Proxy connection failed"
	case strings.Contains(s, "ERR_TUNNEL_CONNECTION_FAILED"):
		return "Proxy tunnel failed"
	case strings.Contains(s, "ERR_NAME_NOT_RESOLVED"):
		return "DNS resolution failed"
	case strings.Contains(s, "ERR_CONNECTION_REFUSED"):
		return "Target connection refused"
	case strings.Contains(s, "ERR_CONNECTION_RESET"):
		return "Target connection reset"
	case strings.Contains(s, "ERR_CONNECTION_TIMED_OUT"),
		strings.Contains(s, "ERR_TIMED_OUT"):
		return "Connection timed out"
	case strings.Contains(s, "ERR_INTERNET_DISCONNECTED"):
		return "Network unavailable"
	case strings.Contains(s, "ERR_CERT_"):
		return "TLS/certificate error"
	case strings.Contains(s, "playwright: net::"):
		// Other Chromium net:: codes — extract the ERR_* token without
		// leaking surrounding URL/path detail.
		if i := strings.Index(s, "net::"); i >= 0 {
			tail := s[i+len("net::"):]
			// ERR_* tokens are word characters and underscores; cut at the
			// first whitespace, " at ", or end of string.
			end := len(tail)
			for k, r := range tail {
				if r == ' ' || r == '\t' || r == '\n' {
					end = k
					break
				}
			}
			tok := tail[:end]
			if tok != "" {
				return fmt.Sprintf("Network error (%s)", tok)
			}
		}
		return "Network error"
	case strings.Contains(s, "Frame.Goto"),
		strings.Contains(s, "page.goto"):
		// Generic playwright navigation failure with no recognizable net::
		// code (e.g. browser context closed, page crashed mid-navigation).
		return "Page failed to load"
	default:
		return "Scrape engine error"
	}
}
