package gmaps

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestExtractJSONPartialAccepted_LogContext verifies that when extractJSON
// gives up and falls back to a partial payload, the warning carries:
//   - job_id        (user-facing, from ctx .With)
//   - user_id       (from ctx .With)
//   - place_job_id  (PlaceJob's internal UUID — renamed from job_id)
//   - search_job_id (GmapJob's internal UUID — renamed from parent_job_id)
//   - place_url
//   - msg == "extract_json_partial_payload_accepted"
func TestExtractJSONPartialAccepted_LogContext(t *testing.T) {
	ctx, buf := newCaptureLogger(t, "USER-JOB-1", "user_TEST")

	pj := &PlaceJob{}
	pj.ID = "PLACE-JOB-1"
	pj.ParentID = "SEARCH-JOB-1"
	pj.URL = "https://www.google.com/maps/place/Test"

	emitPartialPayloadAcceptedWarning(ctx, pj, 18867)

	recs := decodeLogLines(t, buf)
	if len(recs) != 1 {
		t.Fatalf("want 1 record, got %d", len(recs))
	}
	r := recs[0]
	if r["msg"] != "extract_json_partial_payload_accepted" {
		t.Errorf("msg: got %v", r["msg"])
	}
	for _, want := range []string{"job_id", "user_id", "place_job_id", "search_job_id", "place_url", "bytes"} {
		if _, ok := r[want]; !ok {
			t.Errorf("missing field %q in log record", want)
		}
	}
	if !strings.Contains(r["place_url"].(string), "/maps/place/Test") {
		t.Errorf("place_url not propagated: %v", r["place_url"])
	}
}

func TestJSONExtractionFallback_LogContext(t *testing.T) {
	ctx, buf := newCaptureLogger(t, "USER-JOB-Y", "user_TEST")
	pj := &PlaceJob{}
	pj.ID = "PLACE-JOB-Y"
	pj.ParentID = "SEARCH-JOB-Y"
	pj.URL = "https://www.google.com/maps/place/Broken"

	emitJSONExtractionFallback(ctx, pj)
	emitJSONParsingFallback(ctx, pj, errors.New("invalid json"))

	recs := decodeLogLines(t, buf)
	if len(recs) != 2 {
		t.Fatalf("want 2 records, got %d", len(recs))
	}
	for i, want := range []string{"json_extraction_fallback", "json_parsing_fallback"} {
		r := recs[i]
		if r["msg"] != want {
			t.Errorf("rec %d msg: got %v want %v", i, r["msg"], want)
		}
		for _, k := range []string{"job_id", "user_id", "place_job_id", "search_job_id", "place_url"} {
			if _, ok := r[k]; !ok {
				t.Errorf("rec %d missing %q", i, k)
			}
		}
	}
	if recs[1]["error"] != "invalid json" {
		t.Errorf("error field missing or wrong: %v", recs[1]["error"])
	}
}

func TestReviewExtractionLogs_AllCarryUserAndSearchContext(t *testing.T) {
	cases := []struct {
		name string
		emit func(ctx context.Context, j *PlaceJob)
		want string // expected "msg" field
	}{
		{
			name: "circuit_breaker_open",
			emit: emitReviewCircuitBreakerOpen,
			want: "review_circuit_breaker_open",
		},
		{
			name: "extraction_failed",
			emit: func(ctx context.Context, j *PlaceJob) {
				emitReviewExtractionFailed(ctx, j, errors.New("simulated failure"))
			},
			want: "review_extraction_failed",
		},
		{
			name: "api_empty_response",
			emit: func(ctx context.Context, j *PlaceJob) {
				emitReviewAPIEmptyResponse(ctx, j, 271, 33, 1)
			},
			want: "review_api_empty_response",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, buf := newCaptureLogger(t, "USER-JOB-1", "user_TEST")
			pj := &PlaceJob{}
			pj.ID = "PLACE-JOB-1"
			pj.ParentID = "SEARCH-JOB-1"
			pj.URL = "https://www.google.com/maps/place/Test"

			tc.emit(ctx, pj)

			recs := decodeLogLines(t, buf)
			if len(recs) != 1 {
				t.Fatalf("want 1 record, got %d", len(recs))
			}
			r := recs[0]
			if r["msg"] != tc.want {
				t.Errorf("msg: got %v want %v", r["msg"], tc.want)
			}
			for _, k := range []string{"job_id", "user_id", "place_job_id", "search_job_id", "place_url"} {
				if _, ok := r[k]; !ok {
					t.Errorf("missing %q in %s", k, tc.want)
				}
			}
		})
	}
}
