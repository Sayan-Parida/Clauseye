package main

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The Jev engine is development-only, but its response parser is the one place
// where a silent type-assertion failure once turned a malformed provider
// response into a confident-looking zero. These tests keep it strict.

func TestParseJevResultNormalisesTheScore(t *testing.T) {
	// The rubric has four levels, so the raw score is 0..3 and is divided by 3.
	outcome, err := parseJevResult(map[string]any{
		"answers": map[string]any{
			"risk_level": map[string]any{"score": 2.31, "confidence": 0.91},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(outcome.Score-2.31/3) > 1e-9 {
		t.Fatalf("score = %.6f, want %.6f", outcome.Score, 2.31/3)
	}
	if outcome.Confidence != 0.91 {
		t.Fatalf("confidence = %.4f, want 0.91", outcome.Confidence)
	}
	if outcome.HasCategories() {
		t.Fatal("the Jev engine cannot answer per category and must not claim to")
	}
}

func TestParseJevResultMapsToALevel(t *testing.T) {
	// The rubric is 0..3 and the raw score is divided by 3 before being compared
	// against the level thresholds, so the expected levels follow from the
	// normalised value rather than the raw one.
	cases := map[float64]string{
		0.0:  "low",      // 0.0000
		0.6:  "low",      // 0.2000
		0.75: "medium",   // 0.2500, exactly at the medium threshold
		1.2:  "medium",   // 0.4000
		1.5:  "high",     // 0.5000, exactly at the high threshold
		2.24: "high",     // 0.7467
		2.25: "critical", // 0.7500, exactly at the critical threshold
		3.0:  "critical", // 1.0000
	}
	for raw, want := range cases {
		outcome, err := parseJevResult(map[string]any{
			"answers": map[string]any{"risk_level": map[string]any{"score": raw}},
		})
		if err != nil {
			t.Fatalf("score %.2f: %v", raw, err)
		}
		if outcome.Label != want {
			t.Errorf("raw %.2f (normalised %.4f) -> level %q, want %q", raw, outcome.Score, outcome.Label, want)
		}
	}
}

func TestParseJevResultRejectsMalformedResponses(t *testing.T) {
	cases := []struct {
		name string
		in   any
	}{
		{"not an object", "nope"},
		{"no answers", map[string]any{}},
		{"no risk_level", map[string]any{"answers": map[string]any{}}},
		{"score missing", map[string]any{"answers": map[string]any{"risk_level": map[string]any{}}}},
		{"score not numeric", map[string]any{"answers": map[string]any{"risk_level": map[string]any{"score": "high"}}}},
		{"score above rubric", map[string]any{"answers": map[string]any{"risk_level": map[string]any{"score": 4.0}}}},
		{"score negative", map[string]any{"answers": map[string]any{"risk_level": map[string]any{"score": -1.0}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := parseJevResult(c.in); err == nil {
				t.Fatal("expected a malformed response to be rejected")
			}
		})
	}
}

func TestParseJevResultDefaultsMissingConfidenceToZero(t *testing.T) {
	// TypeSafe documents choice/score confidence in providerMetadata rather than
	// the answer object, so the answer frequently has none. It must be reported
	// as absent rather than invented.
	outcome, err := parseJevResult(map[string]any{
		"answers": map[string]any{"risk_level": map[string]any{"score": 1.0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Confidence != 0 {
		t.Fatalf("confidence = %.4f, want 0 when the provider omitted it", outcome.Confidence)
	}
}

func TestParseJevResultRejectsOutOfRangeConfidence(t *testing.T) {
	outcome, err := parseJevResult(map[string]any{
		"answers": map[string]any{"risk_level": map[string]any{"score": 1.0, "confidence": 5.0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Confidence != 0 {
		t.Fatalf("confidence = %.4f, want an out-of-range value clamped to 0", outcome.Confidence)
	}
}

// ---------------------------------------------------------------- transport

func TestWriteErrorCodeIncludesAMachineCode(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErrorCode(rec, http.StatusTooManyRequests, "rate_limited", "slow down")

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"code":"rate_limited"`) {
		t.Fatalf("response has no machine code: %s", body)
	}
	if !strings.Contains(body, `"error":"slow down"`) {
		t.Fatalf("response has no message: %s", body)
	}
}

func TestWriteJSONSetsNoSniff(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, map[string]string{"status": "ok"})
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
}

func TestCountNonEmpty(t *testing.T) {
	sets := [][]ClauseOutcome{nil, {{Categories: readings(nil)}}, nil, {{}, {}}}
	if got := countNonEmpty(sets); got != 2 {
		t.Fatalf("countNonEmpty = %d, want 2", got)
	}
	if got := countNonEmpty([][]ClauseOutcome{nil, nil}); got != 0 {
		t.Fatalf("countNonEmpty = %d for an all-empty set, want 0", got)
	}
}
