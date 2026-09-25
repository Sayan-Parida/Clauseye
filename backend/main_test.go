package main

import "testing"

func TestParseResult(t *testing.T) {
	result, err := parseResult("Example clause", map[string]any{
		"answers": map[string]any{
			"risk_level": map[string]any{
				"score":      2.0,
				"confidence": 0.91,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RiskLevel != "high" || result.Confidence != 0.91 || result.Reason == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
}
