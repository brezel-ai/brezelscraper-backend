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
