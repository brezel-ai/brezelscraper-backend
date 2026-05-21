package gmaps

import (
	"context"
	"errors"
	"fmt"

	"github.com/playwright-community/playwright-go"
)

// browserPage is the subset of playwright.Page used by fetchInBrowser.
// Defined as an interface so unit tests can substitute a fake without
// pulling in a real browser.
type browserPage interface {
	IsClosed() bool
	Evaluate(expression string, arg ...any) (any, error)
}

// Compile-time assertion: real playwright.Page must satisfy browserPage.
var _ browserPage = (playwright.Page)(nil)

// browserFetchResult is the typed return value of the page.Evaluate JS snippet.
// All four fields must be present; unmarshalBrowserFetchResult rejects any
// missing or wrong-typed field.
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
	// playwright-go marshals JS numbers as int, int64, or float64 depending on
	// the Chromium DevTools Protocol path — accept all three.
	switch v := m["status"].(type) {
	case nil:
		// JS may omit status when the catch branch fires before fetch resolves;
		// callers detect that case via res.Error != "".
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

// browserFetchSnippet returns the JS function body passed to page.Evaluate.
// The URL is sent as an Evaluate arg — never string-concatenated into the
// snippet — so a hostile place URL cannot break out of the JS context.
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

// ErrBrowserPageClosed is returned when the Playwright page closed before
// fetchInBrowser could run. Treat as non-retryable.
var ErrBrowserPageClosed = errors.New("playwright page closed")

// ErrBrowserPageNil is returned when fetchInBrowser is called with a nil page.
// Indicates a wiring bug in the caller (CLI mode, missing scrapemate setup).
var ErrBrowserPageNil = errors.New("browser page is nil")

// fetchInBrowser performs a listugcposts GET from inside the running Playwright
// page. The browser provides the TLS fingerprint, HTTP/2 framing, and cookie
// context that Go-HTTP cannot faithfully reproduce — Google's RPC endpoint
// returns an anonymous 33-byte stub to clients it does not recognize as a
// browser.
//
// The IsClosed guard is defense-in-depth: a page can still close between the
// check and Evaluate, in which case Evaluate returns an error that wraps
// through to the caller via %w. ctx is threaded for future tracing;
// page.Evaluate does not currently accept it.
func fetchInBrowser(_ context.Context, page browserPage, url string) ([]byte, error) {
	if page == nil {
		return nil, ErrBrowserPageNil
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
		// res.Error is a JS string from the catch branch, not a Go error — %s
		// is deliberate; %w would be wrong.
		return nil, fmt.Errorf("browser fetch JS exception: %s", res.Error)
	}
	if res.Status != 200 {
		return nil, fmt.Errorf("browser fetch status %d", res.Status)
	}
	return []byte(res.Body), nil
}
