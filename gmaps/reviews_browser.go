package gmaps

import (
	"context"
	"errors"
	"fmt"

	"github.com/playwright-community/playwright-go"
)

// browserPage is the subset of playwright.Page that fetchInBrowser uses.
// Defined as an interface so unit tests can substitute a fake without
// pulling in a real browser. Keep minimal — Tasks 2 and 3 do not add to it.
type browserPage interface {
	IsClosed() bool
	Evaluate(expression string, arg ...any) (any, error)
}

// Compile-time assertion: playwright.Page must satisfy browserPage so that
// fetcher.params.page (assigned a real playwright.Page in place.go) can be
// passed into fetchInBrowser. If playwright-go bumps the Evaluate signature
// in a future release, this line fails loudly at build time.
var _ browserPage = (playwright.Page)(nil)

// browserFetchResult is what the page.Evaluate JS snippet returns. The four
// fields are deterministic — anything missing or wrong-typed indicates the
// JS snippet has drifted and the call should fail loudly.
type browserFetchResult struct {
	OK     bool
	Status int
	Body   string
	Error  string
}

func unmarshalBrowserFetchResult(raw any) (browserFetchResult, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return browserFetchResult{}, fmt.Errorf("browser fetch result: want object, got %T", raw)
	}
	out := browserFetchResult{}
	if v, ok := m["ok"].(bool); ok {
		out.OK = v
	} else {
		return out, fmt.Errorf("browser fetch result: field 'ok' missing or not bool (was %T)", m["ok"])
	}
	switch v := m["status"].(type) {
	case nil:
		// missing — tolerated; Status stays zero (JS may omit on early errors)
	case int:
		out.Status = v
	case int64:
		out.Status = int(v)
	case float64:
		out.Status = int(v)
	default:
		return out, fmt.Errorf("browser fetch result: field 'status' not number (was %T)", v)
	}
	if v, ok := m["body"].(string); ok {
		out.Body = v
	}
	if v, ok := m["error"].(string); ok {
		out.Error = v
	}
	return out, nil
}

// browserFetchSnippet returns the JS function body Playwright runs via
// page.Evaluate. The function takes one argument (the URL) — DO NOT
// string-concatenate the URL into the snippet; pass it as an Evaluate arg
// so a hostile place URL cannot break out of the JS context.
//
// The try/catch + typed return gives the Go side exactly one of three
// shapes: (a) fetch succeeded with 200 → OK=true Body=text;
// (b) fetch returned non-200 → OK=false Status=N Body=text (Body may be
// empty);  (c) JS threw → OK=false Status=0 Error=message.
func browserFetchSnippet() string {
	return `async (url) => {
		try {
			const r = await fetch(url, {
				method: 'GET',
				credentials: 'include',
				headers: { 'Accept': '*/*', 'Accept-Language': 'en-US,en;q=0.9' }
			});
			const body = await r.text();
			return { ok: r.ok, status: r.status, body: body, error: '' };
		} catch (e) {
			return { ok: false, status: 0, body: '', error: String(e && e.message || e) };
		}
	}`
}

// ErrBrowserPageClosed is returned when fetchInBrowser is called against
// a page that has already been closed by scrapemate. Callers should
// surface this as a non-retryable signal — the browser path is the only
// path; if it cannot run, the request cannot complete.
var ErrBrowserPageClosed = errors.New("playwright page closed")

// fetchInBrowser performs the listugcposts GET from inside the running
// Playwright page. The browser handles TLS, HTTP/2, cookies, Origin /
// Referer — everything Google requires. The byte-level reproduction
// proving Go-HTTP cannot reproduce this lives in the plan at
// docs/superpowers/plans/2026-05-20-browser-based-review-fetch.md.
//
// The ctx parameter is kept for future tracing hooks; playwright-go
// v0.5700 page.Evaluate does not accept ctx. Scrapemate's per-job ctx
// still bounds the overall page lifetime.
func fetchInBrowser(_ context.Context, page browserPage, url string) ([]byte, error) {
	if page == nil {
		return nil, fmt.Errorf("fetchInBrowser: nil page")
	}
	if page.IsClosed() {
		return nil, ErrBrowserPageClosed
	}
	raw, err := page.Evaluate(browserFetchSnippet(), url)
	if err != nil {
		return nil, fmt.Errorf("page.Evaluate: %w", err)
	}
	res, err := unmarshalBrowserFetchResult(raw)
	if err != nil {
		return nil, err
	}
	if res.Error != "" {
		return nil, fmt.Errorf("browser fetch JS exception: %s", res.Error)
	}
	if res.Status != 200 {
		return nil, fmt.Errorf("browser fetch status %d", res.Status)
	}
	return []byte(res.Body), nil
}
