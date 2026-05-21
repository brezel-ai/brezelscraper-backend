package gmaps

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/gosom/scrapemate"
)

const maxReviewPages = 500

type fetchReviewsParams struct {
	page        browserPage // satisfied by playwright.Page — see reviews_browser.go
	mapURL      string
	reviewCount int
	maxReviews  int    // Maximum number of reviews to fetch
	langCode    string // Language code for the review API (e.g., "en", "de")

	// Logging context — populated by the caller in place.go. Optional in CLI
	// scrapes where these fields are unknown; emit helpers (see
	// userArgsFromParams) omit user_id/job_id entirely when empty to avoid
	// polluting per-user Grafana queries with empty-string buckets.
	placeJobID  string
	searchJobID string
	placeName   string
	// userID and userJobID are propagated from PlaceJob.UserID / PlaceJob.UserJobID.
	// They are emitted explicitly in all reviews.go log calls because
	// scrapemate replaces the ctx-bound logger per job (scrapemate.go:312),
	// stripping the .With(job_id, user_id) attributes set by the webrunner.
	userID    string
	userJobID string
}

// fetchInBrowserFn is a test seam. Production calls go through fetchReviewPage
// which invokes this var; tests reassign it via t.Cleanup so dispatch can be
// verified without launching a real browser. Explicit function type catches
// signature drift in fetchInBrowser at the declaration line.
var fetchInBrowserFn func(context.Context, browserPage, string) ([]byte, error) = fetchInBrowser

// userArgsFromParams returns the user_id/job_id args for a fetchReviewsParams,
// omitting any empty values. See userArgs in place.go for rationale.
func userArgsFromParams(p *fetchReviewsParams) []any {
	args := make([]any, 0, 4)
	if p.userJobID != "" {
		args = append(args, "job_id", p.userJobID)
	}
	if p.userID != "" {
		args = append(args, "user_id", p.userID)
	}
	return args
}

type fetchReviewsResponse struct {
	pages [][]byte
}

type fetcher struct {
	params fetchReviewsParams
}

func newReviewFetcher(params fetchReviewsParams) (*fetcher, error) {
	return &fetcher{params: params}, nil
}

func (f *fetcher) langForURL() string {
	if f.params.langCode != "" {
		return f.params.langCode
	}
	return "en"
}

func (f *fetcher) fetch(ctx context.Context) (fetchReviewsResponse, error) {
	requestIDForSession, err := generateRandomID(21)
	if err != nil {
		return fetchReviewsResponse{}, fmt.Errorf("failed to generate session request ID: %v", err)
	}

	// Calculate page size - don't fetch more than we need
	pageSize := 20
	if f.params.maxReviews > 0 && f.params.maxReviews < pageSize {
		pageSize = f.params.maxReviews
	}

	reviewURL, err := f.generateURL(f.params.mapURL, "", pageSize, requestIDForSession)
	if err != nil {
		args := userArgsFromParams(&f.params)
		args = append(args,
			"place_job_id", f.params.placeJobID,
			"search_job_id", f.params.searchJobID,
			"place_url", f.params.mapURL,
			"place_name", f.params.placeName,
			"next_page_token", "",
			"error", err,
		)
		scrapemate.GetLoggerFromContext(ctx).Error("reviews_generate_url_failed", args...)
		return fetchReviewsResponse{}, fmt.Errorf("failed to generate initial URL: %v", err)
	}

	currentPageBody, err := f.fetchReviewPage(ctx, reviewURL)
	if err != nil {
		return fetchReviewsResponse{}, fmt.Errorf("failed to fetch initial review page: %v", err)
	}

	ans := fetchReviewsResponse{}
	ans.pages = append(ans.pages, currentPageBody)

	// Count reviews collected so far (approximate)
	reviewsCollected := pageSize

	nextPageToken := extractNextPageToken(currentPageBody)
	pageCount := 1

	for nextPageToken != "" {
		select {
		case <-ctx.Done():
			return ans, ctx.Err()
		default:
		}

		// Hard upper limit on pages to prevent unbounded fetching
		if pageCount >= maxReviewPages {
			break
		}

		// Stop if we've reached the limit
		if f.params.maxReviews > 0 && reviewsCollected >= f.params.maxReviews {
			break
		}

		// Adjust page size for remaining reviews when a limit is set
		currentPageSize := pageSize
		if f.params.maxReviews > 0 {
			remainingNeeded := f.params.maxReviews - reviewsCollected
			if remainingNeeded <= 0 {
				break
			}
			if remainingNeeded < pageSize {
				currentPageSize = remainingNeeded
			}
		}

		reviewURL, err = f.generateURL(f.params.mapURL, nextPageToken, currentPageSize, requestIDForSession)
		if err != nil {
			args := userArgsFromParams(&f.params)
			args = append(args,
				"place_job_id", f.params.placeJobID,
				"search_job_id", f.params.searchJobID,
				"place_url", f.params.mapURL,
				"place_name", f.params.placeName,
				"next_page_token", nextPageToken,
				"error", err,
			)
			scrapemate.GetLoggerFromContext(ctx).Error("reviews_generate_url_failed", args...)
			break
		}

		currentPageBody, err = f.fetchReviewPage(ctx, reviewURL)
		if err != nil {
			args := userArgsFromParams(&f.params)
			args = append(args,
				"place_job_id", f.params.placeJobID,
				"search_job_id", f.params.searchJobID,
				"place_url", f.params.mapURL,
				"place_name", f.params.placeName,
				"next_page_token", nextPageToken,
				"review_url", reviewURL,
				"error", err,
			)
			scrapemate.GetLoggerFromContext(ctx).Error("reviews_fetch_page_failed", args...)
			break
		}

		ans.pages = append(ans.pages, currentPageBody)
		reviewsCollected += currentPageSize
		pageCount++
		nextPageToken = extractNextPageToken(currentPageBody)
	}

	return ans, nil
}

// Note the added 'requestID' parameter
func (f *fetcher) generateURL(mapURL, pageToken string, pageSize int, requestID string) (string, error) {
	placeIDRegex := regexp.MustCompile(`!1s([^!]+)`)

	placeIDMatch := placeIDRegex.FindStringSubmatch(mapURL)
	if len(placeIDMatch) < 2 {
		return "", fmt.Errorf("could not extract place ID from URL: %s", mapURL)
	}

	rawPlaceID, err := url.QueryUnescape(placeIDMatch[1])
	if err != nil {
		rawPlaceID = placeIDMatch[1]
	}

	encodedPlaceID := url.QueryEscape(rawPlaceID)

	encodedPageToken := url.QueryEscape(pageToken)

	pbComponents := []string{
		fmt.Sprintf("!1m6!1s%s", encodedPlaceID),
		"!6m4!4m1!1e1!4m1!1e3",
		fmt.Sprintf("!2m2!1i%d!2s%s", pageSize, encodedPageToken),
		fmt.Sprintf("!5m2!1s%s!7e81", requestID),
		"!8m9!2b1!3b1!5b1!7b1",
		"!12m4!1b1!2b1!4m1!1e1!11m0!13m1!1e1",
	}

	fullURL := fmt.Sprintf(
		"https://www.google.com/maps/rpc/listugcposts?authuser=0&hl=%s&pb=%s",
		f.langForURL(), strings.Join(pbComponents, ""),
	)

	return fullURL, nil
}

// fetchReviewPage requests one paginated page of listugcposts JSON from
// inside the running Playwright page. There is no fallback: if the page
// is nil or closed, the request fails — Go-HTTP cannot reproduce a real
// browser well enough for Google to honor this endpoint, regardless of
// cookies, SAPISIDHASH headers, or TLS-fingerprint impersonation.
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

func extractNextPageToken(data []byte) string {
	text := string(data)
	prefix := ")]}'\n"
	text = strings.TrimPrefix(text, prefix)

	var result []interface{}

	err := json.Unmarshal([]byte(text), &result)
	if err != nil {
		return ""
	}

	if len(result) < 2 || result[1] == nil {
		return ""
	}

	token, ok := result[1].(string)
	if !ok {
		return ""
	}

	return token
}

func generateRandomID(length int) (string, error) {
	numBytes := (length*6 + 7) / 8
	if numBytes < 16 {
		numBytes = 16
	}

	b := make([]byte, numBytes)

	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}

	encoded := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(b)
	if len(encoded) >= length {
		return encoded[:length], nil
	}

	return "", errors.New("generated ID is shorter than expected")
}
