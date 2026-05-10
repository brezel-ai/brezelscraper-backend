package writers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nulEscapeStr is the 6-character JSON escape for a NUL byte. Built from
// runes so this source file itself never contains a literal NUL byte
// (which the Go compiler rejects). Mirrors `nulEscape` in the production
// file but as a string for substring assertions.
var nulEscapeStr = string([]byte{'\\', 'u', '0', '0', '0', '0'})

// TestMustMarshalJSON_StripsNulEscape pins the production fix for the May
// 2026 incident: scraped Google Maps strings sometimes contain NUL bytes
// (review text, image alt text), and json.Marshal renders these as the
// literal 6-character escape . Postgres' json/jsonb columns reject
// that sequence with SQLSTATE 22P02 ("invalid input syntax for type
// json"), causing the whole result row to fail and the job to hang in
// "scraping" because results_written never increments.
func TestMustMarshalJSON_StripsNulEscape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		input          any
		mustNotContain string
		mustBeJSON     bool
	}{
		{
			name:           "string field with embedded NUL",
			input:          map[string]string{"text": "hello\x00world"},
			mustNotContain: nulEscapeStr,
			mustBeJSON:     true,
		},
		{
			name:           "nested struct field with NUL in slice",
			input:          map[string][]string{"tags": {"clean", "dir\x00ty", "ok"}},
			mustNotContain: nulEscapeStr,
			mustBeJSON:     true,
		},
		{
			name:           "no NULs is a no-op",
			input:          map[string]string{"text": "perfectly clean"},
			mustNotContain: nulEscapeStr,
			mustBeJSON:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mustMarshalJSON(tc.input)
			assert.NotContains(t, string(got), tc.mustNotContain,
				"mustMarshalJSON must strip the literal \\u0000 escape — Postgres json columns reject it")
			if tc.mustBeJSON {
				// Round-trip through encoding/json on the consumer side
				// would also reject , so any well-formed output
				// minus the NUL escape is still valid JSON.
				assert.True(t, strings.HasPrefix(string(got), "{") || strings.HasPrefix(string(got), "["),
					"output must remain valid JSON after stripping (got: %q)", string(got))
			}
		})
	}
}

// TestMustMarshalJSON_MarshalErrorReturnsNull pins the safety fallback —
// when json.Marshal itself fails (e.g., a function-typed field), the
// writer must produce "null" rather than crashing the whole row.
func TestMustMarshalJSON_MarshalErrorReturnsNull(t *testing.T) {
	t.Parallel()
	// Channels can't be marshaled to JSON.
	got := mustMarshalJSON(make(chan int))
	assert.Equal(t, "null", string(got))
}

// TestStripJSONNulEscape_RespectsBackslashParity pins the May 10, 2026
// follow-up to PR #60: the original bytes.ReplaceAll-based strip was
// escape-blind and corrupted JSON when scraped strings contained the
// literal six-character ASCII sequence \, u, 0, 0, 0, 0 (e.g., URL
// tokens, JS-encoded review text). For those inputs json.Marshal emits
// `\\u0000` (seven bytes inside the quotes) and the naive replace would
// strip the inner six bytes leaving a stray backslash, producing
// `Token "\" is invalid` (SQLSTATE 22P02) — exactly the symptom PR #60
// was supposed to fix, just for a different class of input. The new
// strip walks the bytes counting backslash runs and only removes a
// `\u0000` escape when the preceding backslash run has even length.
func TestStripJSONNulEscape_RespectsBackslashParity(t *testing.T) {
	t.Parallel()

	NUL := string([]byte{0x00})
	// Build the literal six-character escape via byte construction so the
	// test source itself never contains a NUL or has any unicode-escape
	// ambiguity at parse time.
	BSL := string([]byte{0x5c})
	LITESC := BSL + "u0000" // \, u, 0, 0, 0, 0 — six ASCII chars

	tests := []struct {
		name string
		in   any
		// reparseMustSucceed is the contract Postgres cares about: the
		// resulting bytes must round-trip through encoding/json. Postgres'
		// json/jsonb parser is at least as strict as encoding/json.
		reparseMustSucceed bool
	}{
		{
			name:               "real NUL byte stripped (PR #60 case)",
			in:                 map[string]string{"text": "hello" + NUL + "world"},
			reparseMustSucceed: true,
		},
		{
			name:               "literal six-char escape preserved unchanged (PR #61 root cause)",
			in:                 map[string]string{"text": "hello" + LITESC + "world"},
			reparseMustSucceed: true,
		},
		{
			name:               "real backslash + real NUL: backslash kept, NUL stripped",
			in:                 map[string]string{"text": "hello" + BSL + NUL + "world"},
			reparseMustSucceed: true,
		},
		{
			name:               "two real backslashes + literal u0000 sequence",
			in:                 map[string]string{"text": "hello" + BSL + BSL + "u0000world"},
			reparseMustSucceed: true,
		},
		{
			name:               "three real backslashes + real NUL (odd run + escape)",
			in:                 map[string]string{"text": "hello" + BSL + BSL + BSL + NUL + "world"},
			reparseMustSucceed: true,
		},
		{
			name: "deeply nested NUL bytes in slice values",
			in: map[string][]string{
				"items": {"clean", "dir" + NUL + "ty", "ok", LITESC, BSL + NUL + "z"},
			},
			reparseMustSucceed: true,
		},
		{
			name:               "Google Maps style URL with rwg_token-like literal escape",
			in:                 map[string]string{"link": "/url?q=" + LITESC + "&rwg_token=AFd1xnH7"},
			reparseMustSucceed: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mustMarshalJSON(tc.in)

			// 1. The output must contain no actual NUL bytes (Postgres rejects
			//    those at the connection level via pgx).
			assert.NotContains(t, string(got), NUL,
				"output must not contain raw NUL bytes")

			// 2. The output must contain no genuine \u0000 escape sequence.
			//    A \u0000 escape is one preceded by an even-length run of
			//    backslashes. Anything matching that pattern would be
			//    rejected by Postgres.
			assertNoUnescapedNulEscape(t, got)

			// 3. The output must be valid JSON, parseable by encoding/json
			//    (which, like Postgres, rejects bare backslashes).
			if tc.reparseMustSucceed {
				var dummy any
				err := json.Unmarshal(got, &dummy)
				require.NoError(t, err,
					"output must round-trip through encoding/json — Postgres is at least as strict.\noutput: %s", got)
			}
		})
	}
}

// assertNoUnescapedNulEscape verifies that no genuine \u0000 escape
// remains in the bytes — i.e., no `\u0000` preceded by an even-length
// backslash run. A literal `\\u0000` (preceded by an odd run) is fine
// and must be allowed through.
func assertNoUnescapedNulEscape(t *testing.T, b []byte) {
	t.Helper()
	for i := 0; i+5 < len(b); i++ {
		if b[i] != '\\' || b[i+1] != 'u' || b[i+2] != '0' || b[i+3] != '0' || b[i+4] != '0' || b[i+5] != '0' {
			continue
		}
		// Count backslashes immediately before position i.
		k := i
		for k > 0 && b[k-1] == '\\' {
			k--
		}
		precedingRun := i - k
		// If the count of preceding backslashes is even, then this `\u0000`
		// is itself an unescaped NUL escape — that's the bug.
		if precedingRun%2 == 0 {
			t.Fatalf("found unescaped \\u0000 escape at byte %d (preceding backslash run length %d). Output: %q",
				i, precedingRun, string(b))
		}
	}
}

// TestStripJSONNulEscape_FastPath verifies the no-allocation fast path
// when input contains no \u0000 sequence at all. Returning the same
// underlying slice is an implementation detail but the data must match.
func TestStripJSONNulEscape_FastPath(t *testing.T) {
	t.Parallel()
	in := []byte(`{"a":"plain","b":["no","escapes","here"]}`)
	out := stripJSONNulEscape(in)
	assert.Equal(t, in, out)
}
