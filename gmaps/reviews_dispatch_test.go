package gmaps

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fetchReviewPage must route through the per-fetcher browserFetch hook —
// there is no Go-HTTP fallback.
func TestFetchReviewPage_CallsBrowserFetch(t *testing.T) {
	called := false
	f := &fetcher{
		params: fetchReviewsParams{page: &fakePage{}},
		browserFetch: func(_ context.Context, p browserPage, _ string) ([]byte, error) {
			called = true
			if p == nil {
				t.Errorf("nil page passed through")
			}
			return []byte("real-body"), nil
		},
	}
	body, err := f.fetchReviewPage(context.Background(), "https://x")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !called {
		t.Fatal("browserFetch was not called")
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

// ErrBrowserPageClosed must propagate through fetchReviewPage's %w wrap so
// callers can errors.Is-check it for retry policy.
func TestFetchReviewPage_ReturnsErrorWhenPageClosed(t *testing.T) {
	f := &fetcher{
		params: fetchReviewsParams{page: &fakePage{closed: true}},
		browserFetch: func(_ context.Context, _ browserPage, _ string) ([]byte, error) {
			return nil, ErrBrowserPageClosed
		},
	}
	_, err := f.fetchReviewPage(context.Background(), "https://x")
	if err == nil || !errors.Is(err, ErrBrowserPageClosed) {
		t.Fatalf("want ErrBrowserPageClosed, got %v", err)
	}
}
