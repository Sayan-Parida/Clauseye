package main

import "testing"

func TestParseResult(t *testing.T) {
	result, err := parseResult("Example clause", map[string]any{
		"answers": []any{map[string]any{"key": "risk_level", "value": "high"}},
		"confidence": 0.91,
		"reason": "Uncapped liability.",
	})
	if err != nil { t.Fatal(err) }
	if result.RiskLevel != "high" || result.Confidence != 0.91 || result.Reason == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
}
