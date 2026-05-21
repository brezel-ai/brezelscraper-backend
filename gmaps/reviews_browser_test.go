package gmaps

import (
	"context"
	"errors"
	"strings"
	"testing"
)

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

// fakePage records the JS and URL passed to Evaluate so dispatch tests can
// assert the call contract without launching a real browser.
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
	if !errors.Is(err, ErrBrowserPageNil) {
		t.Fatalf("want ErrBrowserPageNil, got %v", err)
	}
}

// page.Evaluate's own error (e.g., the CDP connection dropped) must wrap
// through to the caller so the error chain remains inspectable.
func TestFetchInBrowser_EvaluateError(t *testing.T) {
	p := &fakePage{returnErr: errors.New("cdp connection lost")}
	_, err := fetchInBrowser(context.Background(), p, "https://x")
	if err == nil {
		t.Fatal("want error from Evaluate, got nil")
	}
	if !strings.Contains(err.Error(), "page.Evaluate") {
		t.Errorf("error should be wrapped with page.Evaluate prefix, got %v", err)
	}
	if !strings.Contains(err.Error(), "cdp connection lost") {
		t.Errorf("underlying error not preserved, got %v", err)
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

// playwright-go marshals JS numbers as int, int64, or float64 — all three
// must be accepted. Regression for an early bug where only float64 worked.
func TestUnmarshalBrowserFetchResult_StatusNumericTypes(t *testing.T) {
	cases := map[string]any{
		"int":     int(200),
		"int64":   int64(200),
		"float64": float64(200),
	}
	for name, status := range cases {
		t.Run(name, func(t *testing.T) {
			raw := map[string]any{"ok": true, "status": status, "body": "", "error": ""}
			got, err := unmarshalBrowserFetchResult(raw)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got.Status != 200 {
				t.Errorf("Status = %d, want 200", got.Status)
			}
		})
	}
}
