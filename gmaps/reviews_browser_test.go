package gmaps

import (
	"context"
	"errors"
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

// TestUnmarshalBrowserFetchResult_AcceptsIntStatus is the regression
// test for the bug found during live verification: playwright-go returns
// numeric JS values as Go int, not float64. The original implementation
// only accepted float64 and rejected every real-world response with
// "field 'status' not number (was int)".
func TestUnmarshalBrowserFetchResult_AcceptsIntStatus(t *testing.T) {
	raw := map[string]any{
		"ok":     true,
		"status": int(200),
		"body":   ")]}'\n[\"reviews-here\"]",
		"error":  "",
	}
	got, err := unmarshalBrowserFetchResult(raw)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Status != 200 {
		t.Errorf("Status = %d, want 200", got.Status)
	}
}
