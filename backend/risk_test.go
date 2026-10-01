package main

import (
	"math"
	"os"
	"strings"
	"testing"
)

// Phase 1 tests for the explanation composition and configuration loading.

func TestBuildIssuesOmitsClearCategoriesByDefault(t *testing.T) {
	cfg := defaultConfig()
	agg, err := Aggregate(readings(map[string]float64{
		CatUncappedLiability: 0.90,
		CatAutoRenewal:       0.95,
	}), cfg)
	if err != nil {
		t.Fatal(err)
	}

	issues := buildIssues(agg, "The Vendor's liability shall be unlimited under this Agreement.", cfg)
	if len(issues) != 2 {
		t.Fatalf("expected only the 2 detected categories, got %d: %+v", len(issues), issues)
	}
	for _, issue := range issues {
		if !issue.Detected {
			t.Fatalf("a clear category was emitted: %+v", issue)
		}
		if issue.Explanation == "" {
			t.Fatalf("category %s was emitted with no explanation", issue.Category)
		}
	}
}

func TestBuildIssuesIncludesClearWhenConfigured(t *testing.T) {
	cfg := defaultConfig()
	cfg.IncludeClearIssues = true
	agg, err := Aggregate(readings(nil), cfg)
	if err != nil {
		t.Fatal(err)
	}

	issues := buildIssues(agg, "A clause.", cfg)
	if len(issues) != len(categoryOrder) {
		t.Fatalf("expected all %d categories, got %d", len(categoryOrder), len(issues))
	}
	for _, issue := range issues {
		if issue.Detected {
			t.Fatalf("a clear category was marked detected: %+v", issue)
		}
		if issue.Contributes {
			t.Fatalf("a clear category was marked as contributing: %+v", issue)
		}
	}
}

func TestBuildIssuesOrdersCategoriesByTaxonomy(t *testing.T) {
	cfg := defaultConfig()
	cfg.IncludeClearIssues = true
	agg, err := Aggregate(readings(nil), cfg)
	if err != nil {
		t.Fatal(err)
	}
	issues := buildIssues(agg, "A clause.", cfg)
	for i, issue := range issues {
		if issue.Category != categoryOrder[i] {
			t.Fatalf("issue %d is %s, want %s (taxonomy order must be stable)", i, issue.Category, categoryOrder[i])
		}
	}
}

func TestDetectedIsDerivedFromProbabilityNotConfidence(t *testing.T) {
	cfg := defaultConfig()
	// The classic reporting trap: a confidently clear category carries
	// confidence 0.99 while its probability is 0.01. If Detected were derived
	// from confidence the UI would tell a lawyer this is 99% risky.
	in := Readings{}
	for _, id := range categoryOrder {
		in[id] = ProbabilityPair{Probability: 0.01, Confidence: 0.99}
	}
	agg, err := Aggregate(in, cfg)
	if err != nil {
		t.Fatal(err)
	}
	issues := buildIssues(agg, "A clause.", cfg)
	if len(issues) != 0 {
		t.Fatalf("expected no issues, got %d", len(issues))
	}
	if agg.Score != 0 {
		t.Fatalf("score = %.4f, want 0 for a confidently clean clause", agg.Score)
	}
}

func TestProtectiveIssueIsEmittedButDoesNotContribute(t *testing.T) {
	cfg := defaultConfig()
	agg, err := Aggregate(readings(map[string]float64{
		CatLimitationOfLiability: 0.95,
		CatUncappedLiability:     0.10,
	}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	issues := buildIssues(agg, "Liability shall not exceed the fees paid in the preceding twelve months.", cfg)

	var protective *Issue
	for i := range issues {
		if issues[i].Category == CatLimitationOfLiability {
			protective = &issues[i]
		}
	}
	if protective == nil {
		t.Fatalf("the liability cap was not reported; got %+v", issues)
	}
	if protective.Polarity != PolarityProtective {
		t.Fatalf("polarity = %q, want %q", protective.Polarity, PolarityProtective)
	}
	if protective.Contributes {
		t.Fatal("a protective category was marked as contributing to the risk score")
	}
}

func TestComposeNarrativeWithFindings(t *testing.T) {
	cfg := defaultConfig()
	agg, err := Aggregate(readings(map[string]float64{
		CatOneSidedIndemnity: 0.94,
		CatUncappedLiability: 0.88,
	}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	explanation, reason := composeNarrative(agg, cfg)

	// The level in the narrative must be the level that was actually computed,
	// not a hardcoded expectation.
	if want := capitalise(agg.Level) + " risk"; !strings.Contains(explanation, want) {
		t.Fatalf("narrative does not state the computed level %q: %q", want, explanation)
	}
	if !strings.Contains(explanation, "One-sided indemnity") || !strings.Contains(explanation, "Uncapped liability") {
		t.Fatalf("narrative does not name the findings: %q", explanation)
	}
	if strings.ContainsAny(explanation, ":.; ") && strings.Contains(explanation, "drive.") {
		t.Fatalf("narrative has malformed punctuation: %q", explanation)
	}
	// The narrative must not echo a bare score the way the previous engine did.
	if strings.Contains(explanation, "Jev score") {
		t.Fatalf("narrative still echoes the old score-only reason: %q", explanation)
	}
	if !strings.Contains(reason, agg.Level+":") {
		t.Fatalf("reason is not machine-parseable: %q", reason)
	}
	for _, id := range []string{CatOneSidedIndemnity, CatUncappedLiability} {
		if !strings.Contains(reason, id) {
			t.Fatalf("reason %q omits %s", reason, id)
		}
	}
}

func TestComposeNarrativeWithoutFindings(t *testing.T) {
	cfg := defaultConfig()
	agg, err := Aggregate(readings(nil), cfg)
	if err != nil {
		t.Fatal(err)
	}
	explanation, reason := composeNarrative(agg, cfg)
	if !strings.Contains(explanation, "No risk category") {
		t.Fatalf("a clean clause should say so plainly: %q", explanation)
	}
	if reason == "" {
		t.Fatal("reason must never be empty")
	}
}

func TestComposeNarrativeMentionsUncertainty(t *testing.T) {
	cfg := defaultConfig()
	in := readings(map[string]float64{CatAutoRenewal: 0.50})
	in[CatAutoRenewal] = ProbabilityPair{Probability: 0.50, Confidence: 0.85}

	agg, err := Aggregate(in, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !agg.NeedsReview {
		t.Fatal("expected needs_review for an uncertain contributor")
	}
	explanation, _ := composeNarrative(agg, cfg)
	if !strings.Contains(explanation, "uncertain") {
		t.Fatalf("narrative does not flag the uncertainty: %q", explanation)
	}
}

func TestNarrativeCountsAreCorrect(t *testing.T) {
	cfg := defaultConfig()
	one, err := Aggregate(readings(map[string]float64{CatAutoRenewal: 0.90}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if expl, _ := composeNarrative(one, cfg); !strings.Contains(expl, "1 finding drives") {
		t.Fatalf("singular phrasing wrong: %q", expl)
	}

	two, err := Aggregate(readings(map[string]float64{
		CatAutoRenewal:         0.90,
		CatJurisdictionRelated: 0.90,
	}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if expl, _ := composeNarrative(two, cfg); !strings.Contains(expl, "2 findings drive") {
		t.Fatalf("plural phrasing wrong: %q", expl)
	}
}

func TestEvidenceIsCleanedAndBounded(t *testing.T) {
	cat, _ := LookupCategory(CatAutoRenewal)
	clause := "The Agreement shall automatically\n\n   renew   unless notice is given."
	evidence, _ := findEvidence(clause, cat)
	if strings.ContainsAny(evidence, "\n") || strings.Contains(evidence, "   ") {
		t.Fatalf("evidence was not whitespace-normalised: %q", evidence)
	}
	if evidence == "" {
		t.Fatal("expected evidence from a clause that plainly contains renewal language")
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	unsetConfigEnv(t)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("default configuration rejected: %v", err)
	}
	if cfg.Engine != EngineLaya {
		t.Fatalf("default engine = %q, want %q", cfg.Engine, EngineLaya)
	}
	if cfg.Aggregator != AggregatorNoisyORFloored {
		t.Fatalf("default aggregator = %q, want %q", cfg.Aggregator, AggregatorNoisyORFloored)
	}
	if math.Abs(cfg.GroupWeights.Sum()-1) > 1e-9 {
		t.Fatalf("default weights sum to %.4f", cfg.GroupWeights.Sum())
	}
}

func TestLoadConfigParsesOverrides(t *testing.T) {
	unsetConfigEnv(t)
	t.Setenv("RISK_ENGINE", "jev")
	t.Setenv("RISK_AGGREGATOR", AggregatorWeightedSum)
	t.Setenv("RISK_DETECT_THRESHOLD", "0.7")
	t.Setenv("RISK_CLEAR_THRESHOLD", "0.3")
	t.Setenv("RISK_LEVEL_HIGH", "0.55")
	t.Setenv("RISK_WEIGHT_LIABILITY", "0.4")
	t.Setenv("RISK_WEIGHT_INDEMNITY", "0.25")
	t.Setenv("RISK_WEIGHT_TERMINATION", "0.15")
	t.Setenv("RISK_WEIGHT_RENEWAL", "0.08")
	t.Setenv("RISK_WEIGHT_OBLIGATION", "0.07")
	t.Setenv("RISK_WEIGHT_JURISDICTION", "0.05")
	t.Setenv("LAYA_BATCH_CLAUSES", "40")
	t.Setenv("LAYA_TIMEOUT", "90s")
	t.Setenv("RISK_INCLUDE_CLEAR_ISSUES", "true")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("overrides rejected: %v", err)
	}
	if cfg.Engine != EngineJev || cfg.Aggregator != AggregatorWeightedSum {
		t.Fatalf("enum overrides not applied: %+v", cfg)
	}
	if cfg.Thresholds.Detect != 0.7 || cfg.Thresholds.Clear != 0.3 || cfg.Thresholds.High != 0.55 {
		t.Fatalf("threshold overrides not applied: %+v", cfg.Thresholds)
	}
	if cfg.GroupWeights.Liability != 0.4 {
		t.Fatalf("weight override not applied: %+v", cfg.GroupWeights)
	}
	if cfg.LayaBatchClauses != 40 || cfg.LayaTimeout.Seconds() != 90 {
		t.Fatalf("batch/timeout overrides not applied: %+v", cfg)
	}
	if !cfg.IncludeClearIssues {
		t.Fatal("boolean override not applied")
	}
}

func TestLoadConfigRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"unknown engine", map[string]string{"RISK_ENGINE": "gpt"}},
		{"unknown aggregator", map[string]string{"RISK_AGGREGATOR": "magic"}},
		{"aggregator not recognized", map[string]string{"RISK_AGGREGATOR": "noisy-or-v2"}},
		{"detect below clear", map[string]string{"RISK_DETECT_THRESHOLD": "0.2", "RISK_CLEAR_THRESHOLD": "0.5"}},
		{"levels out of order", map[string]string{"RISK_LEVEL_MEDIUM": "0.8"}},
		{"weights not summing to one", map[string]string{"RISK_WEIGHT_LIABILITY": "0.9"}},
		{"probability out of range", map[string]string{"RISK_DETECT_THRESHOLD": "1.5"}},
		{"non-numeric", map[string]string{"LAYA_BATCH_CLAUSES": "many"}},
		{"non-boolean", map[string]string{"RISK_INCLUDE_CLEAR_ISSUES": "sometimes"}},
		{"bad duration", map[string]string{"LAYA_TIMEOUT": "soon"}},
		{"overlap exceeds window", map[string]string{"WINDOW_OVERLAP_CHARS": "5000"}},
		{"laya engine without url", map[string]string{"RISK_ENGINE": "laya", "LAYA_URL": ""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			unsetConfigEnv(t)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			if _, err := LoadConfig(); err == nil {
				t.Fatalf("invalid configuration was accepted: %v", c.env)
			}
		})
	}
}

// unsetConfigEnv removes every variable LoadConfig reads and restores them after
// the test. It cannot use t.Setenv to blank them, because the loader
// deliberately treats "set to empty" as a misconfiguration -- so the helper
// would trip the very validation it exists to exercise.
func unsetConfigEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"RISK_ENGINE", "RISK_AGGREGATOR", "LAYA_URL", "LAYA_API_KEY",
		"LAYA_BATCH_CLAUSES", "LAYA_ATTEMPTS", "LAYA_TIMEOUT",
		"RISK_BREAKER_THRESHOLD", "RISK_BREAKER_COOLDOWN",
		"MAX_CLAUSES", "MAX_REQUEST_BYTES", "MIN_CLAUSE_CHARS",
		"DOCUMENT_LEGAL_TERM_MIN", "WINDOW_CHARS", "WINDOW_OVERLAP_CHARS",
		"RISK_INCLUDE_CLEAR_ISSUES", "RISK_DOCUMENT_GATE",
		"RISK_DETECT_THRESHOLD", "RISK_CLEAR_THRESHOLD",
		"RISK_LEVEL_MEDIUM", "RISK_LEVEL_HIGH", "RISK_LEVEL_CRITICAL",
		"RISK_REVIEW_CONFIDENCE_FLOOR",
		"RISK_WEIGHT_LIABILITY", "RISK_WEIGHT_INDEMNITY", "RISK_WEIGHT_TERMINATION",
		"RISK_WEIGHT_RENEWAL", "RISK_WEIGHT_OBLIGATION", "RISK_WEIGHT_JURISDICTION",
		"RISK_LIABILITY_DAMPING", "RISK_PLAIN_INDEMNITY_FACTOR",
	} {
		if original, existed := os.LookupEnv(name); existed {
			t.Cleanup(func() { os.Setenv(name, original) })
		} else {
			t.Cleanup(func() { os.Unsetenv(name) })
		}
		os.Unsetenv(name)
	}
}

func TestCalibrationIsMarkedProvisional(t *testing.T) {
	// The weights in config.go are unvalidated guesses. The constant that says
	// so must exist and must not equal the validated marker, so that a future
	// change to validated values is a deliberate, visible edit.
	if CalibrationProvisional == CalibrationValidated {
		t.Fatal("the provisional marker must differ from the validated marker")
	}
	if CalibrationProvisional == "" {
		t.Fatal("the calibration marker must not be empty")
	}
}
