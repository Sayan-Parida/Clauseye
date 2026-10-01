package main

import (
	"math"
	"strings"
	"testing"
)

// readings builds a full Readings from a probability map. Categories the test
// does not mention default to 0.0, so expected scores are exact arithmetic
// rather than approximations of a filler baseline.
func readings(p map[string]float64) Readings {
	out := Readings{}
	for _, id := range categoryOrder {
		v := 0.0
		if given, ok := p[id]; ok {
			v = given
		}
		out[id] = ProbabilityPair{Probability: v, Confidence: math.Max(v, 1-v)}
	}
	return out
}

func TestValidateReadingsRejectsBadInput(t *testing.T) {
	cfg := defaultConfig()

	t.Run("missing category", func(t *testing.T) {
		in := readings(nil)
		delete(in, CatAutoRenewal)
		if err := ValidateReadings(in); err == nil {
			t.Fatal("expected an error for a missing category")
		}
	})
	t.Run("unknown category", func(t *testing.T) {
		in := readings(nil)
		in["not_a_category"] = ProbabilityPair{Probability: 0.5, Confidence: 0.5}
		if err := ValidateReadings(in); err == nil {
			t.Fatal("expected an error for an unknown category")
		}
	})
	t.Run("probability out of range", func(t *testing.T) {
		in := readings(nil)
		in[CatAutoRenewal] = ProbabilityPair{Probability: 1.4, Confidence: 0.9}
		if err := ValidateReadings(in); err == nil {
			t.Fatal("expected an error for probability > 1")
		}
	})
	t.Run("negative probability", func(t *testing.T) {
		in := readings(nil)
		in[CatAutoRenewal] = ProbabilityPair{Probability: -0.01, Confidence: 0.9}
		if err := ValidateReadings(in); err == nil {
			t.Fatal("expected an error for a negative probability")
		}
	})
	t.Run("NaN probability", func(t *testing.T) {
		in := readings(nil)
		in[CatAutoRenewal] = ProbabilityPair{Probability: math.NaN(), Confidence: 0.9}
		if err := ValidateReadings(in); err == nil {
			t.Fatal("expected an error for NaN, which the previous silent type assertion accepted as 0")
		}
	})
	t.Run("confidence out of range", func(t *testing.T) {
		in := readings(nil)
		in[CatAutoRenewal] = ProbabilityPair{Probability: 0.9, Confidence: 1.2}
		if err := ValidateReadings(in); err == nil {
			t.Fatal("expected an error for confidence > 1")
		}
	})
	t.Run("empty", func(t *testing.T) {
		if err := ValidateReadings(Readings{}); err == nil {
			t.Fatal("expected an error for empty readings")
		}
	})
	t.Run("valid", func(t *testing.T) {
		if err := ValidateReadings(readings(nil)); err != nil {
			t.Fatalf("valid readings rejected: %v", err)
		}
	})
	_ = cfg
}

func TestClassifyHysteresis(t *testing.T) {
	th := Thresholds{Detect: 0.60, Clear: 0.40}
	cases := []struct {
		p    float64
		want string
	}{
		{0.00, StateClear},
		{0.40, StateClear}, // Clear is inclusive
		{0.41, StateUncertain},
		{0.59, StateUncertain},
		{0.60, StateDetected}, // Detect is inclusive
		{1.00, StateDetected},
	}
	for _, c := range cases {
		if got := classify(c.p, th); got != c.want {
			t.Errorf("classify(%.2f) = %q, want %q", c.p, got, c.want)
		}
	}
}

func TestClearCategoryDoesNotMoveTheScore(t *testing.T) {
	cfg := defaultConfig()

	// 0.39 is below the Clear threshold, so auto-renewal is classified clear and
	// must contribute nothing. Without the "group contributes only when a member
	// is not clear" rule this single clear category would have added
	// 0.10 * 0.39 to the score.
	agg, err := Aggregate(readings(map[string]float64{CatAutoRenewal: 0.39}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if agg.Score != 0 {
		t.Fatalf("a clear category moved the score to %.4f, want 0", agg.Score)
	}
	if len(agg.Contributors) != 0 {
		t.Fatalf("clear category appeared as a contributor: %+v", agg.Contributors)
	}
}

func TestUncertainCategoryContributesButLessThanDetected(t *testing.T) {
	cfg := defaultConfig()

	uncertain, err := Aggregate(readings(map[string]float64{CatAutoRenewal: 0.50}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	detected, err := Aggregate(readings(map[string]float64{CatAutoRenewal: 0.90}), cfg)
	if err != nil {
		t.Fatal(err)
	}

	if uncertain.Score <= 0 {
		t.Fatal("an uncertain category contributed nothing; the band exists to surface ambiguity")
	}
	if !(uncertain.Score < detected.Score) {
		t.Fatalf("uncertain %.4f should score below detected %.4f", uncertain.Score, detected.Score)
	}
	if !uncertain.NeedsReview {
		t.Fatal("an uncertain contributing category must set needs_review")
	}
	if detected.NeedsReview {
		t.Fatal("a confidently detected category must not set needs_review")
	}
}

func TestLiabilityCapDampsUncappedLiability(t *testing.T) {
	// Exact arithmetic is asserted against the un-floored aggregator, where the
	// terms are just weight * group risk.
	cfg := defaultConfig()
	cfg.Aggregator = AggregatorNoisyOR

	bare, err := Aggregate(readings(map[string]float64{CatUncappedLiability: 0.90}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	capped, err := Aggregate(readings(map[string]float64{
		CatUncappedLiability:     0.90,
		CatLimitationOfLiability: 0.90,
	}), cfg)
	if err != nil {
		t.Fatal(err)
	}

	if capped.Score >= bare.Score {
		t.Fatalf("a liability cap did not reduce the score: bare %.4f, capped %.4f", bare.Score, capped.Score)
	}
	// 0.90 * (1 - 0.5 * 0) = 0.90, weighted 0.30 -> term 0.27.
	if math.Abs(bare.Score-0.27) > 1e-6 {
		t.Fatalf("uncapped liability alone: got %.4f, want 0.27", bare.Score)
	}
	// 0.90 * (1 - 0.5 * 0.90) = 0.495, weighted 0.30 -> term 0.1485.
	if math.Abs(capped.Score-0.1485) > 1e-6 {
		t.Fatalf("capped uncapped liability: got %.4f, want 0.1485", capped.Score)
	}

	// The damping must still hold under the floored aggregator, which is the
	// default: the score is the floor here, and the floor scales with group risk.
	cfg.Aggregator = AggregatorNoisyORFloored
	flooredBare, err := Aggregate(readings(map[string]float64{CatUncappedLiability: 0.90}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	flooredCapped, err := Aggregate(readings(map[string]float64{
		CatUncappedLiability:     0.90,
		CatLimitationOfLiability: 0.90,
	}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if flooredCapped.Score >= flooredBare.Score {
		t.Fatalf("the cap stopped damping under the floored aggregator: %.4f vs %.4f",
			flooredCapped.Score, flooredBare.Score)
	}
}

func TestOneSidedIndemnityDominatesPlainIndemnity(t *testing.T) {
	cfg := defaultConfig()

	plain, err := Aggregate(readings(map[string]float64{CatIndemnification: 0.90}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	oneSided, err := Aggregate(readings(map[string]float64{CatOneSidedIndemnity: 0.90}), cfg)
	if err != nil {
		t.Fatal(err)
	}

	if oneSided.Score <= plain.Score {
		t.Fatalf("one-sided indemnity %.4f should outrank plain indemnity %.4f", oneSided.Score, plain.Score)
	}
	// Plain is scaled by 0.5, so it must not double-count on top of one-sided.
	both, err := Aggregate(readings(map[string]float64{
		CatIndemnification:   0.90,
		CatOneSidedIndemnity: 0.90,
	}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(both.Score-oneSided.Score) > 1e-9 {
		t.Fatalf("adding plain indemnity on top of one-sided changed the score: %.4f vs %.4f", both.Score, oneSided.Score)
	}
}

func TestUnilateralTerminationRequiresATerminationRight(t *testing.T) {
	cfg := defaultConfig()

	// No termination right at all: the model says the clause creates a
	// unilateral right but not a termination right. min() makes the group risk
	// small, so the clause is not treated as a serious termination hazard.
	inconsistent, err := Aggregate(readings(map[string]float64{
		CatUnilateralTermination: 0.95,
		CatTerminationRight:      0.05,
	}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Both present: a genuine unilateral termination hazard.
	consistent, err := Aggregate(readings(map[string]float64{
		CatUnilateralTermination: 0.95,
		CatTerminationRight:      0.95,
	}), cfg)
	if err != nil {
		t.Fatal(err)
	}

	if inconsistent.Score >= consistent.Score {
		t.Fatalf("unilateral termination without a termination right scored %.4f, at or above the consistent case %.4f",
			inconsistent.Score, consistent.Score)
	}
}

func TestSeverityFloorStopsASingleFindingBeingDilutedAway(t *testing.T) {
	// Regression test for a defect found by running the real pipeline: a textbook
	// unilateral-termination clause scored 0.15 and was reported "low".
	//
	// Under un-floored absolute noisy-OR the score of a lone group is capped at
	// that group's weight, so with weights summing to 1.0 across six groups four
	// of the nine categories can never be reported above "low" on their own.
	//
	// The floor is (weight * group risk) / largest weight, so each category
	// reaches exactly its own relative severity. That is the point: the floor
	// removes the dilution without flattening the ordering.
	cfg := defaultConfig()

	certainty := []struct {
		category  string
		wantScore float64
		wantLevel string
	}{
		{CatUncappedLiability, 1.0, "critical"},
		// A plain, balanced indemnity is deliberately scaled to half strength by
		// PlainIndemnityFactor, because a one-sided indemnity is the worse case.
		// So a certain plain indemnity reads medium while a certain one-sided
		// indemnity reads critical. That asymmetry is intended.
		{CatIndemnification, 0.25 * 0.5 / 0.30, "medium"},
		{CatOneSidedIndemnity, 0.25 / 0.30, "critical"},
		{CatUnilateralTermination, 0.20 / 0.30, "high"},
		{CatAutoRenewal, 0.10 / 0.30, "medium"},
		{CatUnusualObligation, 0.10 / 0.30, "medium"},
		// Jurisdiction carries the lowest weight on purpose: a domestic
		// governing-law clause is close to boilerplate, so even a certain one
		// reading is genuinely low risk. The floor must not promote it.
		{CatJurisdictionRelated, 0.05 / 0.30, "low"},
	}

	for _, c := range certainty {
		probabilities := map[string]float64{c.category: 1.0}
		if c.category == CatUnilateralTermination {
			probabilities[CatTerminationRight] = 1.0 // the soft-AND guard
		}
		out, err := Aggregate(readings(probabilities), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(out.Score-c.wantScore) > 1e-9 {
			t.Errorf("%s at certainty scored %.4f, want %.4f", c.category, out.Score, c.wantScore)
		}
		if out.Level != c.wantLevel {
			t.Errorf("%s at certainty read %q, want %q", c.category, out.Level, c.wantLevel)
		}
	}
}

func TestSevereCategoriesCannotBeReportedLow(t *testing.T) {
	// The defect in one assertion: a confirmed risk with real financial
	// consequence must never be presented as "low", whatever the weights are.
	cfg := defaultConfig()
	severe := []string{CatUncappedLiability, CatIndemnification, CatOneSidedIndemnity, CatUnilateralTermination}
	for _, id := range severe {
		probabilities := map[string]float64{id: 1.0}
		if id == CatUnilateralTermination {
			probabilities[CatTerminationRight] = 1.0
		}
		out, err := Aggregate(readings(probabilities), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if out.Level == "low" {
			t.Errorf("%s at certainty was reported LOW", id)
		}

		// And confirm the old behaviour really did fail this.
		cfg.Aggregator = AggregatorNoisyOR
		unfloored, err := Aggregate(readings(probabilities), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if id != CatUncappedLiability && unfloored.Level == "low" {
			t.Logf("note: without the floor, %s at certainty did read LOW (%.4f), which is the defect",
				id, unfloored.Score)
		}
		cfg.Aggregator = AggregatorNoisyORFloored
	}
}

func TestSeverityFloorPreservesSeverityOrdering(t *testing.T) {
	// The floor must not simply promote everything to critical. Jurisdiction is
	// the least severe category in the taxonomy, and a certain, purely domestic
	// jurisdiction clause genuinely is low risk.
	cfg := defaultConfig()

	jurisdiction, err := Aggregate(readings(map[string]float64{CatJurisdictionRelated: 1.0}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if jurisdiction.Level != "low" {
		t.Fatalf("a certain jurisdiction-only clause scored %.4f (%s), want low",
			jurisdiction.Score, jurisdiction.Level)
	}

	liability, err := Aggregate(readings(map[string]float64{CatUncappedLiability: 1.0}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if liability.Score <= jurisdiction.Score {
		t.Fatalf("liability %.4f did not outrank jurisdiction %.4f", liability.Score, jurisdiction.Score)
	}
	if liability.Level == jurisdiction.Level {
		t.Fatalf("a certain uncapped liability and a certain jurisdiction clause both read %q; the floor flattened the ordering",
			liability.Level)
	}
}

func TestSeverityFloorOnlyRaisesTheScore(t *testing.T) {
	// The floor is a maximum, not a replacement, so it can never lower a score
	// that noisy-OR already computed correctly.
	cfg := defaultConfig()
	for _, ids := range []map[string]float64{
		{CatUncappedLiability: 0.30, CatOneSidedIndemnity: 0.30, CatAutoRenewal: 0.30},
		{CatUncappedLiability: 0.55, CatOneSidedIndemnity: 0.50},
		{CatJurisdictionRelated: 0.5, CatAutoRenewal: 0.5, CatUnusualObligation: 0.5},
	} {
		cfg.Aggregator = AggregatorNoisyOR
		unfloored, err := Aggregate(readings(ids), cfg)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Aggregator = AggregatorNoisyORFloored
		floored, err := Aggregate(readings(ids), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if floored.Score < unfloored.Score-1e-9 {
			t.Errorf("floored score %.4f is below unfloored %.4f for %v", floored.Score, unfloored.Score, ids)
		}
	}
}

func TestEveryAggregatorIsBoundedAndHonoursTheGroupGate(t *testing.T) {
	for _, aggregator := range []string{AggregatorNoisyORFloored, AggregatorNoisyOR, AggregatorWeightedSum} {
		cfg := defaultConfig()
		cfg.Aggregator = aggregator

		clean, err := Aggregate(readings(nil), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if clean.Score != 0 || clean.Level != "low" {
			t.Errorf("%s: a clean clause scored %.4f (%s)", aggregator, clean.Score, clean.Level)
		}

		for _, ids := range []map[string]float64{
			{CatUncappedLiability: 1, CatOneSidedIndemnity: 1, CatUnilateralTermination: 1,
				CatTerminationRight: 1, CatAutoRenewal: 1, CatJurisdictionRelated: 1, CatUnusualObligation: 1},
			{CatAutoRenewal: 0.39},
		} {
			out, err := Aggregate(readings(ids), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if out.Score < 0 || out.Score > 1 {
				t.Errorf("%s: score %.6f outside [0,1] for %v", aggregator, out.Score, ids)
			}
		}
	}
}

func TestMaxGroupWeightTracksTheWeights(t *testing.T) {
	cfg := defaultConfig()
	if got := maxGroupWeight(cfg); got != cfg.GroupWeights.Liability {
		t.Fatalf("maxGroupWeight = %.3f, want the liability weight %.3f", got, cfg.GroupWeights.Liability)
	}
	cfg.GroupWeights = GroupWeights{Liability: 0.5, Indemnity: 0.5, Termination: 0, Renewal: 0, Obligation: 0, Jurisdiction: 0}
	if got := maxGroupWeight(cfg); got != 0.5 {
		t.Fatalf("maxGroupWeight = %.3f, want 0.5", got)
	}
}

func TestScoreIsAlwaysBounded(t *testing.T) {
	cfg := defaultConfig()
	cases := []map[string]float64{
		nil,
		{CatAutoRenewal: 1},
		{CatUncappedLiability: 1, CatOneSidedIndemnity: 1, CatUnilateralTermination: 1,
			CatTerminationRight: 1, CatAutoRenewal: 1, CatJurisdictionRelated: 1, CatUnusualObligation: 1},
		{CatLimitationOfLiability: 0, CatUncappedLiability: 0},
	}
	for i, c := range cases {
		for _, agg := range []string{AggregatorNoisyOR, AggregatorWeightedSum} {
			cfg.Aggregator = agg
			out, err := Aggregate(readings(c), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if out.Score < 0 || out.Score > 1 {
				t.Fatalf("case %d with %s produced score %.6f outside [0,1]", i, agg, out.Score)
			}
			if out.Level != riskLevelNames[0] && out.Score >= cfg.Thresholds.Critical && out.Level != "critical" {
				t.Fatalf("case %d with %s: score %.4f should be critical, got %q", i, agg, out.Score, out.Level)
			}
		}
	}
}

func TestScoreIsMonotonicInEachCategory(t *testing.T) {
	cfg := defaultConfig()
	base, err := Aggregate(readings(nil), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Raising one risk category must never lower the score.
	for _, id := range categoryOrder {
		cat, _ := LookupCategory(id)
		if cat.Polarity != PolarityRisk {
			continue
		}
		for _, p := range []float64{0.45, 0.65, 0.85, 1.0} {
			raised, err := Aggregate(readings(map[string]float64{id: p}), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if raised.Score < base.Score-1e-12 {
				t.Fatalf("%s at %.2f lowered the score from %.4f to %.4f", id, p, base.Score, raised.Score)
			}
		}
	}
}

func TestRiskLevelBoundaries(t *testing.T) {
	cfg := defaultConfig()
	cases := []struct {
		score float64
		want  string
	}{
		{0.0, "low"},
		{0.249, "low"},
		{0.25, "medium"},
		{0.499, "medium"},
		{0.50, "high"},
		{0.749, "high"},
		{0.75, "critical"},
		{1.0, "critical"},
	}
	for _, c := range cases {
		if got := riskLevel(c.score, cfg.Thresholds); got != c.want {
			t.Errorf("riskLevel(%.3f) = %q, want %q", c.score, got, c.want)
		}
	}
}

func TestConfidenceIsTheMinimumAcrossContributors(t *testing.T) {
	cfg := defaultConfig()
	cfg.Thresholds.ReviewConfidenceFloor = 0.0 // isolate the min rule from the floor

	in := readings(map[string]float64{
		CatUncappedLiability: 0.92, // confidence 0.92
		CatOneSidedIndemnity: 0.99, // confidence 0.99
	})
	in[CatUncappedLiability] = ProbabilityPair{Probability: 0.92, Confidence: 0.92}
	in[CatOneSidedIndemnity] = ProbabilityPair{Probability: 0.99, Confidence: 0.99}

	agg, err := Aggregate(in, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The weaker of the two, not the stronger: one confident category must not
	// vouch for another.
	if math.Abs(agg.Confidence-0.92) > 1e-9 {
		t.Fatalf("confidence = %.4f, want the minimum 0.92", agg.Confidence)
	}
	if agg.NeedsReview {
		t.Fatal("needs_review should be false when the floor is zero and both are detected")
	}
}

func TestNeedsReviewBelowConfidenceFloor(t *testing.T) {
	cfg := defaultConfig()
	in := readings(map[string]float64{CatAutoRenewal: 0.70})
	// Model is only moderately sure.
	in[CatAutoRenewal] = ProbabilityPair{Probability: 0.70, Confidence: 0.55}

	agg, err := Aggregate(in, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !agg.NeedsReview {
		t.Fatalf("confidence %.2f is below the floor %.2f, so needs_review must be set",
			agg.Confidence, cfg.Thresholds.ReviewConfidenceFloor)
	}
}

func TestConfidenceWhenNothingContributes(t *testing.T) {
	cfg := defaultConfig()
	in := Readings{}
	for _, id := range categoryOrder {
		in[id] = ProbabilityPair{Probability: 0.02, Confidence: 0.98}
	}
	agg, err := Aggregate(in, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(agg.Contributors) != 0 {
		t.Fatal("nothing should have contributed")
	}
	if math.Abs(agg.Confidence-0.98) > 1e-9 {
		t.Fatalf("confidence = %.4f, want 0.98 (certainty that nothing applies)", agg.Confidence)
	}
	if agg.NeedsReview {
		t.Fatal("a clean clause confidently assessed must not need review")
	}
}

func TestProtectiveCategoryIsNeverAContributor(t *testing.T) {
	cfg := defaultConfig()
	agg, err := Aggregate(readings(map[string]float64{CatLimitationOfLiability: 0.99}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range agg.Contributors {
		if c.Polarity != PolarityRisk {
			t.Fatalf("a %s category appeared as a contributor", c.Polarity)
		}
	}
}

func TestMergeReadingsTakesTheMaximum(t *testing.T) {
	// A liability carve-out seen in only one window must survive the merge
	// rather than being averaged away by the windows that did not see it.
	first := readings(map[string]float64{CatUncappedLiability: 0.95})
	second := readings(map[string]float64{CatUncappedLiability: 0.10, CatAutoRenewal: 0.80})

	merged := mergeReadings([]Readings{first, second})
	if merged[CatUncappedLiability].Probability != 0.95 {
		t.Fatalf("uncapped liability = %.2f, want the window maximum 0.95", merged[CatUncappedLiability].Probability)
	}
	if merged[CatAutoRenewal].Probability != 0.80 {
		t.Fatalf("auto-renewal = %.2f, want 0.80", merged[CatAutoRenewal].Probability)
	}
}

func TestEveryCategoryIsWeightedInExactlyOneGroup(t *testing.T) {
	cfg := defaultConfig()
	seen := map[string][]string{}
	for _, id := range categoryOrder {
		cat, _ := LookupCategory(id)
		seen[cat.Group] = append(seen[cat.Group], id)
		if cat.Polarity == PolarityRisk && groupWeightOf(cfg, cat.Group) == 0 {
			t.Fatalf("risk category %s sits in group %s which has zero weight", id, cat.Group)
		}
	}
	for _, group := range []string{GroupLiability, GroupIndemnity, GroupTermination, GroupRenewal, GroupJurisdiction, GroupObligation} {
		if len(seen[group]) == 0 {
			t.Fatalf("group %s has no members", group)
		}
	}
	if math.Abs(cfg.GroupWeights.Sum()-1) > 1e-9 {
		t.Fatalf("group weights sum to %.4f, want 1.0", cfg.GroupWeights.Sum())
	}
}

func TestTaxonomyEvidenceRulesCompileAndAreOrdered(t *testing.T) {
	for _, cat := range taxonomy {
		if cat.Rationale == "" {
			t.Errorf("category %s has no rationale; every explanation is composed from it", cat.ID)
		}
		if len(cat.Evidence) == 0 {
			t.Errorf("category %s has no evidence rules, so no explanation could ever cite the clause", cat.ID)
		}
		for _, r := range cat.Evidence {
			if r.Re == nil {
				t.Errorf("category %s rule %q did not compile", cat.ID, r.Label)
			}
		}
	}
}

func TestNoDuplicateCategoryIDs(t *testing.T) {
	if len(categoryByID) != len(taxonomy) {
		t.Fatalf("taxonomy has %d entries but %d unique ids", len(taxonomy), len(categoryByID))
	}
	if len(CategoryIDs()) != 9 {
		t.Fatalf("expected the nine documented categories, got %d", len(CategoryIDs()))
	}
}

func TestExplainNeverInventsAQuote(t *testing.T) {
	cat, _ := LookupCategory(CatUnilateralTermination)
	explanation := composeExplanation(cat, "")

	if explanation != cat.Rationale {
		t.Fatal("with no evidence the explanation must be the rationale alone")
	}
	// A clause with no matching language must not produce a quotation.
	evidence, label := findEvidence("The parties shall meet quarterly to review delivery metrics.", cat)
	if evidence != "" {
		t.Fatalf("evidence %q (%s) was fabricated from a clause that does not contain it", evidence, label)
	}
}

func TestEvidenceIsAVerbatimSubstring(t *testing.T) {
	clause := "The Company may terminate this Agreement at any time without notice to the Supplier."
	cat, _ := LookupCategory(CatUnilateralTermination)
	evidence, _ := findEvidence(clause, cat)
	if evidence == "" {
		t.Fatal("expected evidence to be found in a clause that plainly contains it")
	}
	if !strings.Contains(clause, evidence) {
		t.Fatalf("evidence %q is not a verbatim substring of the clause", evidence)
	}
}
