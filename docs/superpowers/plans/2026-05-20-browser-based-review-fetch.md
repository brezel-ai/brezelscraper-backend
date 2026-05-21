# Browser-Based Review Fetch Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the `listugcposts` RPC call from Go's `net/http` client into the already-running Playwright page via `page.Evaluate`, and **delete the Go-HTTP review-fetch path entirely** (no fallback). The Go path has been proven across multiple production runs and standalone A/B tests to receive the 33-byte unauthenticated stub from Google regardless of cookies, SAPISIDHASH headers, proxy, or TLS impersonation — keeping it would only hide failures.

**Architecture:** Single path. `fetchReviewPage` calls `fetchInBrowser`, which runs a small JS snippet inside the existing Playwright page. If the page is nil or closed, the function returns an error and the place is reported with zero reviews — no silent fallback. The cookies file remains relevant for `InjectCookiesIntoPage` (the browser's own session needs them); the Go-HTTP cookie-header machinery is dead and removed.

**Tech Stack:** Go 1.26, `github.com/playwright-community/playwright-go` v0.5700 (already on `fetchReviewsParams.page`), existing scrapemate worker pool. Proxypool unchanged — the browser uses the assigned proxy via scrapemate config.

---

## Background — Why this plan exists

PR #84 added `SAPISIDHASH` + `Origin`/`Referer`/`X-Goog-*` headers to the Go-HTTP review fetch path. Live verification (2026-05-20) proved:

- Headers are demonstrably on the wire (`authorization_len=204`, `cookie_len=3968` in debug logs).
- Google still returns the 33-byte `)]}'\n[null,null,null,null,null,1]` stub.
- The stub is byte-identical across: 3 Decodo proxy IPs, direct egress from our home IP, `crypto/tls` stdlib, `uTLS Firefox-120`, `uTLS Chrome-120`.
- Chrome with the **same Decodo proxy** loads reviews fine for the same place, both signed-in and signed-out.

Upstream `gosom/google-maps-scraper` shipped this exact fix as PR #209 (commit `99eb31d`, 2025-12-26):

> Direct HTTP requests to Google's RPC API lack the necessary browser session cookies for authentication. The API returns empty responses `([null,null,null,null,null,1])` without proper authentication. Solution: Implement browser-based RPC fetching using Playwright's `page.Evaluate()` to make fetch requests from within the browser context.

Our fork's last merge from upstream was 2025-07-08 — 5 months before PR #209 — so we never pulled it. Our commit `ce71f75` (2026-02-16, "feat(gmaps): enable authenticated scraping with Google cookies") added a Go-HTTP cookies path **independently** of upstream's fix, choosing a path Google does not honor. PRs #81–#84 layered observability/proxy/SAPISIDHASH onto that wrong path. **This plan deletes that path and replaces it with upstream's approach, adapted to our code shape.**

---

## File structure — what changes

| File | Action | Note |
|---|---|---|
| `gmaps/reviews_browser.go` | **Create** | Hosts `browserPage` interface, `browserFetchResult`, `unmarshalBrowserFetchResult`, `browserFetchSnippet`, `fetchInBrowser`, `ErrBrowserPageClosed`. Single home for "browser path" code. |
| `gmaps/reviews_browser_test.go` | **Create** | Unit tests for everything in `reviews_browser.go`, using a `fakePage` (no Playwright runtime). |
| `gmaps/reviews_dispatch_test.go` | **Create** | Contract test: `fetchReviewPage` MUST call `fetchInBrowser`; error path if page nil/closed. |
| `gmaps/reviews.go` | **Modify** | Rewrite `fetchReviewPage` to call `fetchInBrowser` only. Remove `fetchWithCookies`, `newCookieFetchClient`, `cookieFetchClient` field, `fetchReviewsParams.proxyURL` field, and unused imports (`net/http`, `net/url`, `io`, possibly `time`). |
| `gmaps/cookies.go` | **Modify** | Remove `GetCookieHeader` and `cookieHeaderFromEntries`. Keep `LoadGoogleCookies`, `InjectCookiesIntoPage`, `SetCookiesFile`, hot-reload — browser still needs the cookies for `InjectCookiesIntoPage`. |
| `gmaps/google_auth.go` | **Delete** | SAPISIDHASH + Authorization-header builder. All callers removed by this plan. |
| `gmaps/google_auth_test.go` | **Delete** | Tests for deleted code. |
| `gmaps/reviews_proxy_test.go` | **Delete** | Tests the Go-HTTP proxy-routing behavior of `fetchWithCookies` / `newCookieFetchClient`. Both deleted. |
| `gmaps/reviews_ctxcancel_test.go` | **Delete** | Tests context-cancel behavior of `fetchWithCookies`. Browser-fetch cancel semantics are different and covered by integration tests. |
| `scripts/debug_review_rpc/` | **Delete** | Diagnostic that manually replays the SAPISIDHASH/cookies path. Now obsolete. |
| `runner/jobs.go` | **Modify** | Update comments that reference `fetchWithCookies` (the proxyURL plumbing into PlaceJob remains — it's still emitted in telemetry). |
| `runner/webrunner/webrunner.go` | **Modify** | Update one comment referencing `fetchWithCookies`. No behavior change. |
| `runner/webrunner/proxy_pick_test.go` | **Modify** | Update one comment referencing `newCookieFetchClient`. |
| `docs/observability/proxy-pool-smoke-test.md` | **Modify** | Add "browser-fetch success signal" section. |

**Unchanged**: `proxypool/*`, `runner/webrunner/webrunner.go` (lease/acquire logic), `gmaps/place.go` (review-fetch entry point), all cookies hot-reload functionality from PR #84, `gmaps/cookies_reload_test.go`.

---

## Risk register (read before starting)

1. **Page lifecycle.** After place extraction completes in `BrowserActions`, scrapemate may close the page before we paginate. Mitigation: `fetchInBrowser` checks `page.IsClosed()` and returns the typed `ErrBrowserPageClosed`. Place-level result emits zero reviews + a structured warning rather than crashing.
2. **JS exceptions.** `await fetch(...)` inside `page.Evaluate` can throw on network error or CORS. Mitigation: the JS snippet wraps in try/catch and returns a typed `{ok, status, body, error}` object so Go-side classification is deterministic.
3. **Body size.** A 500-review place returns ~1 MB JSON per page × N pages, serialized through CDP. Acceptable: alternative is no reviews at all. Each scrapemate worker has its own page so cross-worker contention is not a concern.
4. **CLI mode.** Standalone CLI runs without a Playwright page. `f.params.page` is nil. After this PR, CLI mode cannot fetch reviews — only places. **This is an intentional behavior change**, surfaced via a one-time WARN log at startup if `extra_reviews` is requested without a page.
5. **No fallback to Go HTTP.** Mandated by the user. Operator visibility: every failure path now emits `review_fetch_failed reason=...` so the failure cause is explicit.

---

## Chunk 1: Browser fetch primitives

### Task 1: `browserFetchResult` + unmarshal

**Files:**
- Create: `gmaps/reviews_browser.go`
- Create: `gmaps/reviews_browser_test.go`

- [ ] **Step 1: Write the failing tests**

Create `gmaps/reviews_browser_test.go`:

```go
package gmaps

import (
	"strings"
	"testing"
)

// browserFetchResult is the typed JS-return contract: page.Evaluate must
// return exactly four fields so the Go side has deterministic classification
// of (a) network failure, (b) HTTP non-200, (c) successful body.
func TestUnmarshalBrowserFetchResult_Success(t *testing.T) {
	raw := map[string]any{
		"ok":     true,
		"status": float64(200),
		"body":   ")]}'\n[\"real\",\"reviews\",\"here\"]",
		"error":  "",
	}
	got, err := unmarshalBrowserFetchResult(raw)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !got.OK {
		t.Errorf("OK = false, want true")
	}
	if got.Status != 200 {
		t.Errorf("Status = %d, want 200", got.Status)
	}
	if !strings.Contains(got.Body, "real") {
		t.Errorf("Body did not propagate, got %q", got.Body)
	}
}

func TestUnmarshalBrowserFetchResult_JSException(t *testing.T) {
	raw := map[string]any{
		"ok":     false,
		"status": float64(0),
		"body":   "",
		"error":  "TypeError: Failed to fetch",
	}
	got, err := unmarshalBrowserFetchResult(raw)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.OK {
		t.Errorf("expected ok=false")
	}
	if got.Error != "TypeError: Failed to fetch" {
		t.Errorf("Error did not propagate, got %q", got.Error)
	}
}

func TestUnmarshalBrowserFetchResult_WrongShape(t *testing.T) {
	if _, err := unmarshalBrowserFetchResult("oops"); err == nil {
		t.Fatal("expected error for non-map input")
	}
	if _, err := unmarshalBrowserFetchResult(map[string]any{"ok": "not-a-bool"}); err == nil {
		t.Fatal("expected error for wrong field type")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./gmaps/ -run TestUnmarshalBrowserFetchResult -v`
Expected: FAIL with "undefined: unmarshalBrowserFetchResult"

- [ ] **Step 3: Create the implementation file**

Create `gmaps/reviews_browser.go`. Import block intentionally minimal; Tasks 2 and 3 extend the SAME single block (Go does not allow a second `import (…)` statement after declarations).

```go
package gmaps

import (
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
	if v, ok := m["status"].(float64); ok {
		out.Status = int(v)
	} else if m["status"] != nil {
		return out, fmt.Errorf("browser fetch result: field 'status' not number (was %T)", m["status"])
	}
	if v, ok := m["body"].(string); ok {
		out.Body = v
	}
	if v, ok := m["error"].(string); ok {
		out.Error = v
	}
	return out, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./gmaps/ -run TestUnmarshalBrowserFetchResult -v`
Expected: PASS (3 sub-tests)

- [ ] **Step 5: Commit**

```bash
git add gmaps/reviews_browser.go gmaps/reviews_browser_test.go
git commit -m "feat(gmaps): browser fetch result unmarshal + tests"
```

---

### Task 2: JS snippet builder

**Files:**
- Modify: `gmaps/reviews_browser.go` (append to existing import block + add function)
- Modify: `gmaps/reviews_browser_test.go` (append test)

- [ ] **Step 1: Append failing test**

Append to `gmaps/reviews_browser_test.go`:

```go
func TestBrowserFetchSnippet_Shape(t *testing.T) {
	js := browserFetchSnippet()
	required := []string{
		"async (url) =>",
		"fetch(url",
		"credentials: 'include'",
		"return { ok:",
		"status:",
		"body:",
		"error:",
		"catch (e)",
	}
	for _, want := range required {
		if !strings.Contains(js, want) {
			t.Errorf("snippet missing required fragment %q", want)
		}
	}
	// Defense: snippet must NOT interpolate user data — the URL is passed as
	// an Evaluate arg, not concatenated into the JS source. Place URLs come
	// from Google search results and could contain quotes.
	if strings.Contains(js, "%s") || strings.Contains(js, "+ url") {
		t.Errorf("snippet appears to interpolate URL — must use Evaluate arg, not string concat")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./gmaps/ -run TestBrowserFetchSnippet_Shape -v`
Expected: FAIL with "undefined: browserFetchSnippet"

- [ ] **Step 3: Implement**

Append to `gmaps/reviews_browser.go` (after existing declarations — imports stay as in Task 1):

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./gmaps/ -run TestBrowserFetchSnippet_Shape -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add gmaps/reviews_browser.go gmaps/reviews_browser_test.go
git commit -m "feat(gmaps): JS snippet builder for page.Evaluate review fetch"
```

---

### Task 3: `fetchInBrowser` end-to-end

**Files:**
- Modify: `gmaps/reviews_browser.go` (add `context`, `errors` to the SINGLE existing import block; add `ErrBrowserPageClosed`; add `fetchInBrowser`)
- Modify: `gmaps/reviews_browser_test.go` (extend SINGLE existing import block with `context`, `errors`; add fakePage + 4 tests)

- [ ] **Step 1: Extend test imports + write failing tests**

In `gmaps/reviews_browser_test.go`, **edit the existing import block** (do not add a second one) so it reads:

```go
import (
	"context"
	"errors"
	"strings"
	"testing"
)
```

Then append:

```go
// fakePage is a minimal stand-in for the playwright.Page surface that
// fetchInBrowser actually touches. Defined here (not in production code)
// because it is test-only — the production interface is browserPage in
// reviews_browser.go.
type fakePage struct {
	closed    bool
	gotURL    string
	gotJS     string
	returnRaw any
	returnErr error
}

func (p *fakePage) IsClosed() bool { return p.closed }
func (p *fakePage) Evaluate(js string, args ...any) (any, error) {
	p.gotJS = js
	if len(args) > 0 {
		p.gotURL, _ = args[0].(string)
	}
	return p.returnRaw, p.returnErr
}

func TestFetchInBrowser_ReturnsBodyOnSuccess(t *testing.T) {
	p := &fakePage{
		returnRaw: map[string]any{
			"ok": true, "status": float64(200),
			"body": ")]}'\n[\"reviews-here\"]", "error": "",
		},
	}
	body, err := fetchInBrowser(context.Background(), p, "https://www.google.com/maps/rpc/listugcposts?x=1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(body) != ")]}'\n[\"reviews-here\"]" {
		t.Fatalf("body = %q", string(body))
	}
	if p.gotURL != "https://www.google.com/maps/rpc/listugcposts?x=1" {
		t.Errorf("URL not passed as arg: %q", p.gotURL)
	}
}

func TestFetchInBrowser_PageClosed(t *testing.T) {
	p := &fakePage{closed: true}
	_, err := fetchInBrowser(context.Background(), p, "https://x")
	if err == nil || !errors.Is(err, ErrBrowserPageClosed) {
		t.Fatalf("want ErrBrowserPageClosed, got %v", err)
	}
}

func TestFetchInBrowser_NilPage(t *testing.T) {
	_, err := fetchInBrowser(context.Background(), nil, "https://x")
	if err == nil {
		t.Fatal("want error for nil page, got nil")
	}
}

func TestFetchInBrowser_JSException(t *testing.T) {
	p := &fakePage{returnRaw: map[string]any{
		"ok": false, "status": float64(0), "body": "", "error": "TypeError",
	}}
	_, err := fetchInBrowser(context.Background(), p, "https://x")
	if err == nil || !strings.Contains(err.Error(), "TypeError") {
		t.Fatalf("want JS-exception error, got %v", err)
	}
}

func TestFetchInBrowser_Non200(t *testing.T) {
	p := &fakePage{returnRaw: map[string]any{
		"ok": false, "status": float64(429), "body": "rate limited", "error": "",
	}}
	_, err := fetchInBrowser(context.Background(), p, "https://x")
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("want 429 error, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./gmaps/ -run TestFetchInBrowser -v`
Expected: FAIL with undefined identifiers (`fetchInBrowser`, `ErrBrowserPageClosed`).

- [ ] **Step 3: Extend production imports + implement**

In `gmaps/reviews_browser.go`, **edit the existing single import block** (do not add a second) so it reads:

```go
import (
	"context"
	"errors"
	"fmt"

	"github.com/playwright-community/playwright-go"
)
```

Then append (the declarations from Tasks 1 and 2 stay above):

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./gmaps/ -run TestFetchInBrowser -v`
Expected: PASS (5 sub-tests)

- [ ] **Step 5: Commit**

```bash
git add gmaps/reviews_browser.go gmaps/reviews_browser_test.go
git commit -m "feat(gmaps): fetchInBrowser via page.Evaluate, with typed errors"
```

---

## Chunk 1 review checkpoint

Dispatch `plan-document-reviewer` (template at `/Users/yasseen/.claude/plugins/cache/superpowers-marketplace/superpowers/3.6.2/skills/writing-plans/plan-document-reviewer-prompt.md`) with Chunk 1 contents + path to this file. Iterate until approved.

---

## Chunk 2: Switch `fetchReviewPage` to browser-only

### Task 4: Change `fetchReviewsParams.page` type to `browserPage`

**Files:**
- Modify: `gmaps/reviews.go`

This task changes one field type. It compiles standalone because `playwright.Page` already satisfies `browserPage` (compile-time assertion in `reviews_browser.go`). The existing caller in `gmaps/place.go:638` (`page: page`) needs no change.

- [ ] **Step 1: Confirm current signatures**

```bash
grep -nE "fetchReviewsParams|page\s+playwright\.Page" gmaps/reviews.go
```

Expected: `page playwright.Page` on the `fetchReviewsParams` struct (~line 25).

- [ ] **Step 2: Change the field type**

In `gmaps/reviews.go`, change:

```diff
 type fetchReviewsParams struct {
-	page        playwright.Page
+	page        browserPage  // satisfied by playwright.Page — see reviews_browser.go
 	mapURL      string
```

- [ ] **Step 3: Verify build**

Run: `go build ./gmaps/...`
Expected: build clean. The `playwright` import in `reviews.go` may now be unused (only used by the deleted concrete type). If so, the build fails — remove the import.

- [ ] **Step 4: Verify existing tests still pass**

Run: `go test ./gmaps/ -count=1 -run "TestUnmarshalBrowserFetchResult|TestBrowserFetchSnippet|TestFetchInBrowser"`
Expected: PASS — no new failures from the type change.

- [ ] **Step 5: Commit**

```bash
git add gmaps/reviews.go
git commit -m "refactor(gmaps): typed fetchReviewsParams.page as browserPage interface"
```

---

### Task 5: Rewrite `fetchReviewPage` to browser-only

**Files:**
- Modify: `gmaps/reviews.go` (function `fetchReviewPage` only — the rest of this file is gutted in Task 6)
- Create: `gmaps/reviews_dispatch_test.go`

- [ ] **Step 1: Write the failing contract test**

Create `gmaps/reviews_dispatch_test.go`:

```go
package gmaps

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestFetchReviewPage_CallsBrowserFetch locks the dispatch contract: the
// review fetcher MUST go through fetchInBrowser. There is no fallback —
// previous attempts to fall back to Go HTTP returned the 33-byte stub
// and masked the real failure.
func TestFetchReviewPage_CallsBrowserFetch(t *testing.T) {
	called := false
	orig := fetchInBrowserFn
	t.Cleanup(func() { fetchInBrowserFn = orig })
	fetchInBrowserFn = func(_ context.Context, p browserPage, url string) ([]byte, error) {
		called = true
		if p == nil {
			t.Errorf("nil page passed through")
		}
		return []byte("real-body"), nil
	}

	f := &fetcher{params: fetchReviewsParams{page: &fakePage{}}}
	body, err := f.fetchReviewPage(context.Background(), "https://x")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !called {
		t.Fatal("fetchInBrowserFn was not called")
	}
	if string(body) != "real-body" {
		t.Fatalf("body = %q", string(body))
	}
}

func TestFetchReviewPage_ReturnsErrorWhenPageNil(t *testing.T) {
	f := &fetcher{params: fetchReviewsParams{page: nil}}
	_, err := f.fetchReviewPage(context.Background(), "https://x")
	if err == nil {
		t.Fatal("want error when page is nil, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "page") {
		t.Errorf("error should mention page, got %v", err)
	}
}

func TestFetchReviewPage_ReturnsErrorWhenPageClosed(t *testing.T) {
	orig := fetchInBrowserFn
	t.Cleanup(func() { fetchInBrowserFn = orig })
	fetchInBrowserFn = func(_ context.Context, p browserPage, _ string) ([]byte, error) {
		return nil, ErrBrowserPageClosed
	}
	f := &fetcher{params: fetchReviewsParams{page: &fakePage{closed: true}}}
	_, err := f.fetchReviewPage(context.Background(), "https://x")
	if err == nil || !errors.Is(err, ErrBrowserPageClosed) {
		t.Fatalf("want ErrBrowserPageClosed, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./gmaps/ -run TestFetchReviewPage -v`
Expected: FAIL — `fetchInBrowserFn` undefined.

- [ ] **Step 3: Add dispatch indirection + rewrite `fetchReviewPage` in `reviews.go`**

At package level in `gmaps/reviews.go`, **near the top of the file** (after imports), add:

```go
// fetchInBrowserFn is a test seam. Production callers go through
// fetchReviewPage which calls this var; tests reassign it via t.Cleanup
// so the dispatch can be verified without launching a real browser. The
// explicit function type catches signature drift in fetchInBrowser at the
// declaration line, where the fix is obvious.
var fetchInBrowserFn func(context.Context, browserPage, string) ([]byte, error) = fetchInBrowser
```

Then replace the entire body of `fetchReviewPage` with:

```go
// fetchReviewPage requests one paginated page of listugcposts JSON from
// inside the running Playwright page. There is no fallback: if the page
// is nil or closed, the request fails — Go-HTTP cannot reproduce a real
// browser well enough for Google to honor this endpoint, regardless of
// cookies or SAPISIDHASH headers.
//
// See docs/superpowers/plans/2026-05-20-browser-based-review-fetch.md
// for the byte-level reproduction across multiple TLS clients, proxies,
// and SAPISIDHASH variants — all returning the 33-byte stub.
func (f *fetcher) fetchReviewPage(ctx context.Context, u string) ([]byte, error) {
	if f.params.page == nil {
		return nil, errors.New("no playwright page in scope for review fetch")
	}
	body, err := fetchInBrowserFn(ctx, f.params.page, u)
	if err != nil {
		return nil, fmt.Errorf("browser review fetch: %w", err)
	}
	scrapemate.GetLoggerFromContext(ctx).Debug(
		"review_fetch_succeeded",
		"fetch_via", "browser",
		"bytes", len(body),
	)
	return body, nil
}
```

- [ ] **Step 4: Run all tests in the package**

Run: `go test ./gmaps/ -run "TestFetchReviewPage|TestFetchInBrowser|TestUnmarshalBrowserFetchResult|TestBrowserFetchSnippet" -count=1 -v`
Expected: PASS — 3 new dispatch tests + 7 existing browser tests.

Run: `go build ./...`
Expected: build clean. If `fetchWithCookies` or `newCookieFetchClient` are still referenced anywhere, the build will succeed (they exist) — Task 6 deletes them next.

- [ ] **Step 5: Commit**

```bash
git add gmaps/reviews.go gmaps/reviews_dispatch_test.go
git commit -m "refactor(gmaps): fetchReviewPage uses browser path only — no fallback"
```

---

## Chunk 2 review checkpoint

Dispatch `plan-document-reviewer` with Chunk 2. Iterate until approved.

---

## Chunk 3: Delete dead code

This chunk removes everything the browser path made obsolete. Order matters: delete callers before callees so the build never breaks mid-chunk.

### Task 6: Delete `fetchWithCookies`, `newCookieFetchClient`, related struct fields

**Files:**
- Modify: `gmaps/reviews.go` (delete functions, delete struct field, delete `cookieFetchClient`, delete `proxyURL`, delete `newReviewFetcher` cookieClient call)

- [ ] **Step 1: Confirm nothing outside `reviews.go` calls these**

```bash
grep -rn "fetchWithCookies\|newCookieFetchClient\|cookieFetchClient" \
  --include="*.go" \
  /Users/yasseen/Documents/brezel.ai/BrezelScraper/brezelscraper-backend | \
  grep -v "_test.go\|reviews.go" | head -10
```

Expected: zero hits (a comment in `runner/jobs.go` and `webrunner.go` is acceptable — those are pure-comment references the next task updates). If there are real code-callers outside `reviews.go`, STOP and reconsider — this plan assumes the only callers are inside `reviews.go` itself.

- [ ] **Step 2: Locate everything via grep (not line numbers — they drift)**

```bash
grep -nE "func fetchWithCookies|func newCookieFetchClient|cookieFetchClient|proxyURL\s+string" gmaps/reviews.go
```

Note each reported line number from THIS grep — they are authoritative for the next step.

- [ ] **Step 3: Delete in `gmaps/reviews.go`**

Remove all of these (in any order — the file won't build after this step until Task 7 finishes):
- Entire `func fetchWithCookies(...)`
- Entire `func newCookieFetchClient(...)`
- The `cookieFetchClient *http.Client` field on the `fetcher` struct
- The `cookieClient, err := newCookieFetchClient(params.proxyURL); ...; cookieFetchClient: cookieClient` block in `newReviewFetcher` — replace with the direct struct literal `return &fetcher{params: params}, nil`
- The `proxyURL string` field on `fetchReviewsParams` and the long comment block above it

Then let goimports clean up the import block — Go's toolchain decides what's unused:

```bash
goimports -w gmaps/reviews.go
```

If `goimports` is not installed: `go install golang.org/x/tools/cmd/goimports@latest`.

- [ ] **Step 4: Update `gmaps/place.go` — remove the proxyURL field assignment**

`place.go` sets `proxyURL: j.ProxyURL` when constructing the `fetchReviewsParams` literal (verify with the grep below). It MUST be removed — the field no longer exists.

```bash
grep -n "proxyURL:" gmaps/place.go
```

Delete that line. The `PlaceJob.ProxyURL` field itself stays — it is still emitted in telemetry log lines (e.g. `proxy_used` in `review_extraction_failed`).

- [ ] **Step 5: Confirm the expected broken state**

```bash
go build ./... 2>&1 | head -10
```

Expected: build FAILS with references to the deleted symbols from `reviews_proxy_test.go`, `reviews_ctxcancel_test.go`, and `google_auth_test.go` (the test files we delete in Task 7). This is the intermediate state — do NOT fix by editing tests; Task 7 deletes them.

- [ ] **Step 6: Commit (or proceed to Task 7 without committing)**

The build is intentionally broken at this point — dead tests still reference the deleted symbols. Task 7 fixes this in the next commit.

Do NOT use `--no-verify` to bypass a failing pre-commit hook. If the hook blocks this intermediate commit because it runs `go build`, take this path instead: **skip committing now**, proceed directly to Task 7's deletes, and commit Tasks 6 + 7 together as a single squashed commit. Reviewing the squash will be slightly harder; preserving git hygiene is more important.

If the pre-commit hook does NOT run `go build` (or there is no such hook), commit the intermediate:

```bash
git add gmaps/reviews.go gmaps/place.go
git commit -m "refactor(gmaps): delete Go-HTTP review fetch — intermediate, build broken until Task 7"
```

---

### Task 7: Delete dead test files + `google_auth.go` + diagnostic scripts

**Files (deletes):**
- `gmaps/google_auth.go`
- `gmaps/google_auth_test.go`
- `gmaps/reviews_proxy_test.go`
- `gmaps/reviews_ctxcancel_test.go`
- `scripts/debug_review_rpc/` (entire directory)
- `scripts/tls_fingerprint_test/` (entire directory — diagnostic from SAPISIDHASH debugging, no production value)
- `scripts/tls_google_ab/` (entire directory — diagnostic from SAPISIDHASH debugging, no production value)

- [ ] **Step 1: Confirm `google_auth.go` symbols have no remaining callers**

```bash
grep -rn "applyGoogleAuthHeaders\|googleSAPISIDHash\|buildGoogleAuthorization\|sapisidLabelByCookie\|googleAuthOrigin" \
  --include="*.go" \
  .
```

Expected: matches only in `gmaps/google_auth.go` and `gmaps/google_auth_test.go` (both deleted in this task).

- [ ] **Step 2: Confirm diagnostic scripts don't import anything else of value**

```bash
head -30 scripts/tls_fingerprint_test/main.go scripts/tls_google_ab/main.go
```

Expected: standalone scripts that import only stdlib + uTLS. Safe to delete.

- [ ] **Step 3: Delete the files**

```bash
rm gmaps/google_auth.go gmaps/google_auth_test.go
rm gmaps/reviews_proxy_test.go gmaps/reviews_ctxcancel_test.go
rm -r scripts/debug_review_rpc scripts/tls_fingerprint_test scripts/tls_google_ab
```

- [ ] **Step 4: Build + test**

```bash
go build ./...
go test ./gmaps/ ./runner/webrunner/ ./proxypool/ -count=1
```

Expected: build clean, all tests pass.

- [ ] **Step 5: Stage explicitly — DO NOT `git add -A`**

The working tree has unrelated untracked files (`.playwright-mcp/`, `.DS_Store`, CSV scratch files, sibling `brezelscraper-secrets/` etc. — see `git status`). `git add -A` would sweep them in.

Stage only the deleted files (`git add` handles deletes when given the path):

```bash
git add gmaps/google_auth.go gmaps/google_auth_test.go \
        gmaps/reviews_proxy_test.go gmaps/reviews_ctxcancel_test.go \
        scripts/debug_review_rpc scripts/tls_fingerprint_test scripts/tls_google_ab
git status --short
```

Expected `git status` output: only `D ` (deleted) entries for the seven paths above. If anything else appears, unstage with `git restore --staged <path>`.

- [ ] **Step 6: Commit**

```bash
git commit -m "refactor(gmaps): delete google_auth.go + Go-HTTP tests + obsolete diagnostic scripts"
```

---

### Task 8: Remove `GetCookieHeader` + `cookieHeaderFromEntries` from `cookies.go`

**Files:**
- Modify: `gmaps/cookies.go`

- [ ] **Step 1: Confirm no remaining callers**

```bash
grep -rn "GetCookieHeader\|cookieHeaderFromEntries" --include="*.go" \
  /Users/yasseen/Documents/brezel.ai/BrezelScraper/brezelscraper-backend
```

Expected: zero hits after Task 7. If any remain in `_test.go`, they were part of the deleted suites — re-check Task 7 completed.

- [ ] **Step 2: Delete the two functions**

In `gmaps/cookies.go`, remove `GetCookieHeader` and `cookieHeaderFromEntries`. Keep `LoadGoogleCookies`, `InjectCookiesIntoPage`, `SetCookiesFile`, `resetCookiesCacheForTest`, the `CookieEntry` struct, and the cache state.

- [ ] **Step 3: Build + tests**

```bash
go build ./...
go test ./gmaps/ -run TestLoadGoogleCookies -count=1
```

Expected: build clean; cookie-hot-reload tests still pass.

- [ ] **Step 4: Commit**

```bash
git add gmaps/cookies.go
git commit -m "refactor(gmaps): remove GetCookieHeader (Go-HTTP path is gone)"
```

---

### Task 9: Update stale comments anywhere they reference deleted code

**Files:** Anywhere a comment still references `fetchWithCookies`, `newCookieFetchClient`, `cookieFetchClient`, `cookieHeader`, `cookieClient`, `GetCookieHeader`, or `cookieHeaderFromEntries` — likely `runner/jobs.go`, `runner/webrunner/webrunner.go`, `runner/webrunner/proxy_pick_test.go`, possibly others.

- [ ] **Step 1: Find every stale reference (grep — line numbers drift)**

```bash
grep -rnE "fetchWithCookies|newCookieFetchClient|cookieFetchClient|cookieHeader|cookieClient|GetCookieHeader|cookieHeaderFromEntries" \
  --include="*.go" \
  runner/ web/ gmaps/
```

Every hit is either:
- A comment that needs updating (most of them, in `runner/jobs.go` and `webrunner.go`)
- A test-file comment (e.g. `runner/webrunner/proxy_pick_test.go`)
- A leftover real reference (means earlier tasks missed something — STOP and audit)

- [ ] **Step 2: Update each comment in place**

For each hit, rewrite to reflect the new reality. The proxy URL is now used by the browser via scrapemate config; the Go-HTTP path is gone. Sample rewrite:

- Before: `// Forwarded to GmapJob.ProxyURL → PlaceJob.ProxyURL → reviews.go's fetchWithCookies so the cookie-authenticated review-RPC request egresses through the per-job proxy.`
- After:  `// Forwarded to GmapJob.ProxyURL → PlaceJob.ProxyURL for telemetry. The browser uses the proxy via scrapemate's per-job config (not via this field).`

Use your judgement — the goal is "no comment lies about the architecture." Don't invent new comments where the right action is to delete one that's now redundant.

- [ ] **Step 3: Verify no stale references survive**

Re-run the grep from Step 1. Expected: zero hits in `*.go` files.

- [ ] **Step 4: Build + tests**

```bash
go build ./...
go test ./runner/... ./gmaps/ -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit (explicit paths only)**

```bash
git add runner/ gmaps/
git status --short
```

If `git status` shows anything other than the comment-edited files, unstage with `git restore --staged <path>`.

```bash
git commit -m "docs: update stale comments referencing deleted Go-HTTP review path"
```

---

## Chunk 3 review checkpoint

Dispatch `plan-document-reviewer` with Chunk 3. Iterate until approved.

---

## Chunk 4: Observability + live verification

### Task 10: Add `fetch_via` to existing review-error log lines

**Files:**
- Modify: `gmaps/place.go` (review-related emit sites)

- [ ] **Step 1: Locate emit sites**

```bash
grep -nE 'review_api_empty_response|review_circuit_breaker_open|review_extraction_failed|review_fetch_succeeded' gmaps/place.go
```

- [ ] **Step 2: Tag with `fetch_via=browser`**

The browser is now the only path. Add `slog.String("fetch_via", "browser")` to each emit site so existing Grafana dashboards (which match on `fetch_via=browser`) start working immediately and so any future second path forces an explicit signal change.

- [ ] **Step 3: Build + tests**

```bash
go build ./...
go test ./gmaps/ -count=1
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add gmaps/place.go
git commit -m "obs(gmaps): tag review telemetry with fetch_via=browser"
```

---

### Task 11: Update smoke-test runbook

**Files:**
- Modify: `docs/observability/proxy-pool-smoke-test.md`

- [ ] **Step 1: Add a "Step 3a — Confirm browser fetch is in use" section**

Insert after the existing Step 3 block:

````markdown
### Step 3a — Confirm browser-fetch is in use

After Step 3 logs `proxy_outcome_reported outcome=success`, run on prod:

```bash
sudo journalctl -u brezel-backend --since="10 minutes ago" | grep review_fetch_succeeded | head -3
```

Expected:

```
review_fetch_succeeded fetch_via=browser bytes=NNNNN
```

Where `bytes` is **>33** (usually low-thousands). If you see
`review_api_empty_response` with `response_bytes=33` AND no
`review_fetch_succeeded` lines, the browser path was not taken (the
Playwright page was nil or closed). Investigate `BrowserActions` lifecycle
in `gmaps/place.go` — the page must outlive review pagination.

If you see a panic or "no playwright page in scope for review fetch"
error, the runner is running without a browser (CLI mode). Web mode
MUST use the scrapemate browser; CLI mode no longer fetches reviews —
this is intentional. The full rationale lives in the PR that
implements this plan; cross-reference the merge commit in `git log`
for the file `gmaps/reviews_browser.go`.
````

- [ ] **Step 2: Commit**

```bash
git add docs/observability/proxy-pool-smoke-test.md
git commit -m "docs(obs): add browser-fetch success signal to smoke runbook"
```

---

### Task 12: Local end-to-end verification

This produces evidence the PR works against real Google. No code change — the artifact is log excerpts pasted into the PR description.

- [ ] **Step 1: Stop existing local backend**

```bash
pkill -f './tmp/server -web' || true
```

Wait briefly, then verify with `lsof -i :8080 -sTCP:LISTEN`. The port should be free.

- [ ] **Step 2: Rebuild + start with proxy + cookies**

Set `DECODO_PROXY_URL` and `BREZEL_API_KEY` in your local shell from `brezelscraper-secrets/` (or `.env`) — DO NOT put real credentials in this committed plan or in the commit history. The required env-var names:

- `DECODO_PROXY_URL` — full URL, e.g. `http://USER:PASS@gate.decodo.com:10001`
- `BREZEL_API_KEY` — your local-test API key starting with `bscraper_`

```bash
# Source credentials from your local secrets store. The exact file may
# differ; the credentials must NEVER be pasted into this plan.
source ~/.brezel/local-test.env   # or wherever you keep DECODO_PROXY_URL / BREZEL_API_KEY

go build -o ./tmp/server .
mkdir -p ./tmp/logs ./tmp/gmapsdata
DSN="postgres://scraper:strongpassword@localhost:5432/google_maps_scraper?sslmode=disable" \
GOOGLE_COOKIES_FILE="$(pwd)/google_cookies.json" \
PROXIES="$DECODO_PROXY_URL" \
DATA_FOLDER="$(pwd)/tmp/gmapsdata" \
LOG_LEVEL=debug \
./tmp/server -web > ./tmp/logs/server.log 2>&1 &
sleep 4
lsof -i :8080 -sTCP:LISTEN
grep -E "proxy_pool_initialized|google_cookies_configured" ./tmp/logs/server.log
```

- [ ] **Step 3: Submit a small job**

```bash
curl -s -X POST http://127.0.0.1:8080/api/v1/jobs \
  -H "X-API-Key: $BREZEL_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"browser-fetch-verify","keywords":["coffee mitte berlin"],"language":"en","depth":1,"max_results":1,"max_reviews":10}'
```

- [ ] **Step 4: Stream key events**

Use the Monitor tool with:

```bash
tail -f -n 0 ./tmp/logs/server.log | \
  grep --line-buffered -E "review_fetch_succeeded|review_api_empty_response|review_extraction_failed|job_scrape_succeeded|job_scrape_failed|panic|FATAL"
```

**Pass criteria:**
- Multiple `review_fetch_succeeded fetch_via=browser bytes=N` events with `N > 1000`
- Zero `review_api_empty_response` events
- `job_scrape_succeeded` fires with `result_count >= 1` and reviews present in the output CSV

**Fail criteria:**
- Any `review_api_empty_response` — the browser path didn't fire; investigate page lifecycle
- Any `no playwright page in scope` warning — web runner is misconfigured
- panic — JS snippet drifted or type-unmarshal failed

- [ ] **Step 5: Validate the CSV**

Identify the newest CSV — it belongs to the job just run — and inspect:

```bash
LATEST_CSV=$(ls -t ./tmp/gmapsdata/*.csv 2>/dev/null | head -1)
echo "latest: $LATEST_CSV"
head -5 "$LATEST_CSV" | cut -c1-300
```

Expected: at least one row with non-empty review content (column name depends on the writer — `reviews_json`, `extra_reviews`, etc.; check the CSV header).

- [ ] **Step 6: Commit any docs corrections discovered during verification**

If Step 4 reveals a real operational gotcha (e.g. page closing too early), add a one-paragraph note to `docs/observability/proxy-pool-smoke-test.md` and commit it.

```bash
git add docs/observability/proxy-pool-smoke-test.md
git commit -m "docs(obs): note <gotcha-found-during-verification>"
```

If verification was clean, no commit is needed for this step.

---

### Task 13: Final lint + vet sweep

This catches any leftover unused imports, unused variables, or dead identifiers from the deletion tasks.

- [ ] **Step 1: Run vet**

```bash
go vet ./...
```

Expected: clean. Any output is a real issue — fix before continuing.

- [ ] **Step 2: Run staticcheck / golangci-lint if available**

```bash
golangci-lint run ./gmaps/... ./runner/... 2>&1 | head -40
```

Expected: clean. If `golangci-lint` is not installed, skip — `go vet` is the minimum bar.

- [ ] **Step 3: Final full-package test**

```bash
go test ./... -count=1 -timeout 5m 2>&1 | tail -20
```

Expected: all packages pass (or skip-marked, e.g. integration tests that require Playwright browsers if not installed).

- [ ] **Step 4: Commit any fixes from Steps 1-3**

If Steps 1-3 surfaced anything that needed code fixes:

```bash
git add <specific files>
git commit -m "chore: post-cleanup vet/lint fixes"
```

If nothing needed fixing, no commit.

---

## Chunk 4 review checkpoint

Dispatch `plan-document-reviewer` with Chunk 4. Iterate until approved.

---

## Execution handoff

Plan complete and saved to `docs/superpowers/plans/2026-05-20-browser-based-review-fetch.md`.

Branch: `fix/browser-based-review-fetch` off of `develop` (NOT stacked on PR #84, because Chunk 3 deletes code that PR #84 introduced — easier to land this PR with PR #84 either merged-or-closed first to avoid conflict resolution mid-stack).

PR title: `fix(gmaps): pull gosom #209 — fetch reviews from inside the Playwright page; delete Go-HTTP path`

PR description must include:
1. Timeline table from the Background section showing when we diverged from upstream.
2. Byte-level reproduction from the SAPISIDHASH PR's live verification (uTLS Firefox-120 also returns 33 bytes through Decodo — only the browser succeeds).
3. The architecture before/after: one Go-HTTP arrow becomes one `page.Evaluate` arrow.
4. Live verification log excerpt from Task 12.
5. Explicit callout that **CLI mode no longer fetches reviews** — by design.
