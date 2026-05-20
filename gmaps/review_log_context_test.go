package gmaps

import (
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
