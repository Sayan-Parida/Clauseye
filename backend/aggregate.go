package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// CategoryReading is one category's raw model output plus its derived state.
type CategoryReading struct {
	Category    string
	Probability float64
	Confidence  float64
	State       string
	Group       string
	Polarity    Polarity
	// GroupRisk is the guarded group value, shared by all members of a group.
	GroupRisk float64
	// GroupContributes is true when at least one member of the group is not
	// clear, so the group is allowed to move the score.
	GroupContributes bool
}

// Aggregation is the deterministic result of combining the nine readings.
type Aggregation struct {
	Score       float64
	Level       string
	Confidence  float64
	NeedsReview bool
	Readings    []CategoryReading
	// Contributors lists the risk-polarity categories that drove the score, in
	// descending group weight then descending probability.
	Contributors []CategoryReading
	// Groups lists the groups that contributed, in descending weight.
	Groups []GroupContribution
}

// GroupContribution records one guarded group's contribution to the score.
type GroupContribution struct {
	Group  string  `json:"group"`
	Weight float64 `json:"weight"`
	Risk   float64 `json:"risk"`
	// Term is weight * risk, the amount actually fed into the combination.
	Term float64 `json:"term"`
}

// Readings is a validated set of per-category model outputs keyed by category id.
type Readings map[string]ProbabilityPair

// ProbabilityPair is one category's model answer.
type ProbabilityPair struct {
	Probability float64
	Confidence  float64
}

// ValidateReadings checks that every category is present exactly once and that
// every value is a finite probability.
//
// This replaces the previous `score, _ := risk["score"].(float64)` pattern,
// which turned a malformed or absent field into a confident-looking zero and
// was the reason a provider schema change could silently reclassify every
// clause in the product.
func ValidateReadings(in Readings) error {
	if len(in) == 0 {
		return errors.New("no readings supplied")
	}
	for _, id := range categoryOrder {
		pair, ok := in[id]
		if !ok {
			return fmt.Errorf("readings omitted %s", id)
		}
		if err := checkProbability("probability", id, pair.Probability); err != nil {
			return err
		}
		if err := checkProbability("confidence", id, pair.Confidence); err != nil {
			return err
		}
	}
	for id := range in {
		if _, ok := categoryByID[id]; !ok {
			return fmt.Errorf("readings contain unknown category %q", id)
		}
	}
	return nil
}

func checkProbability(field, id string, v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return fmt.Errorf("category %s has non-finite %s", id, field)
	}
	if v < 0 || v > 1 {
		return fmt.Errorf("category %s has %s %.4f outside [0,1]", id, field, v)
	}
	return nil
}

// Aggregate combines validated readings into a score, level and confidence.
//
// The shape of the computation is:
//
//  1. per-category state with hysteresis, so "confidently not a risk" and
//     "cannot tell" stay distinguishable;
//  2. guarded group risks, so nested categories combine by max and conjunctive
//     ones by min instead of double-counting;
//  3. a bounded combination across groups.
func Aggregate(in Readings, cfg Config) (Aggregation, error) {
	if err := ValidateReadings(in); err != nil {
		return Aggregation{}, err
	}

	readings := make([]CategoryReading, 0, len(categoryOrder))
	for _, id := range categoryOrder {
		cat := categoryByID[id]
		pair := in[id]
		readings = append(readings, CategoryReading{
			Category:    id,
			Probability: pair.Probability,
			Confidence:  pair.Confidence,
			State:       classify(pair.Probability, cfg.Thresholds),
			Group:       cat.Group,
			Polarity:    cat.Polarity,
		})
	}

	groupRisk, groupContributes := groupRisks(readings, cfg)
	for i := range readings {
		readings[i].GroupRisk = groupRisk[readings[i].Group]
		readings[i].GroupContributes = groupContributes[readings[i].Group]
	}

	groups := contributingGroups(readings, cfg)
	score := combine(groups, cfg.Aggregator, maxGroupWeight(cfg))
	level := riskLevel(score, cfg.Thresholds)
	confidence, needsReview := overallConfidence(readings, cfg.Thresholds)

	contributors := make([]CategoryReading, 0, len(readings))
	for _, r := range readings {
		if r.Polarity == PolarityRisk && r.State != StateClear && r.GroupContributes {
			contributors = append(contributors, r)
		}
	}
	sort.SliceStable(contributors, func(i, j int) bool {
		wi := groupWeightOf(cfg, contributors[i].Group)
		wj := groupWeightOf(cfg, contributors[j].Group)
		if wi != wj {
			return wi > wj
		}
		return contributors[i].Probability > contributors[j].Probability
	})

	return Aggregation{
		Score:        score,
		Level:        level,
		Confidence:   confidence,
		NeedsReview:  needsReview,
		Readings:     readings,
		Contributors: contributors,
		Groups:       groups,
	}, nil
}

// classify applies the hysteresis band. Clear < Detect, so a probability below
// Clear is unambiguously clear, above Detect unambiguously detected, and the
// band between them is genuinely uncertain and is reported as such.
func classify(p float64, t Thresholds) string {
	switch {
	case p >= t.Detect:
		return StateDetected
	case p <= t.Clear:
		return StateClear
	default:
		return StateUncertain
	}
}

func groupWeightOf(cfg Config, group string) float64 {
	switch group {
	case GroupLiability:
		return cfg.GroupWeights.Liability
	case GroupIndemnity:
		return cfg.GroupWeights.Indemnity
	case GroupTermination:
		return cfg.GroupWeights.Termination
	case GroupRenewal:
		return cfg.GroupWeights.Renewal
	case GroupJurisdiction:
		return cfg.GroupWeights.Jurisdiction
	case GroupObligation:
		return cfg.GroupWeights.Obligation
	default:
		return 0
	}
}

// groupRisks computes one value per group.
//
//	liability:   uncapped, damped by any liability cap present
//	indemnity:   one-sided dominates a plain indemnity
//	termination: only risky when a termination right exists at all
//	renewal, jurisdiction, obligation: single-category pass-through
func groupRisks(readings []CategoryReading, cfg Config) (map[string]float64, map[string]bool) {
	p := map[string]float64{}
	state := map[string]string{}
	for _, r := range readings {
		p[r.Category] = r.Probability
		state[r.Category] = r.State
	}

	risk := map[string]float64{}
	contributes := map[string]bool{}

	risk[GroupLiability] = p[CatUncappedLiability] * (1 - cfg.GroupFactors.LiabilityDamping*p[CatLimitationOfLiability])
	risk[GroupIndemnity] = math.Max(p[CatIndemnification]*cfg.GroupFactors.PlainIndemnityFactor, p[CatOneSidedIndemnity])
	risk[GroupTermination] = math.Min(p[CatTerminationRight], p[CatUnilateralTermination])
	risk[GroupRenewal] = p[CatAutoRenewal]
	risk[GroupJurisdiction] = p[CatJurisdictionRelated]
	risk[GroupObligation] = p[CatUnusualObligation]

	// A group contributes only when at least one of its members is not clear.
	// Without this, a probability of 0.39 would still shift the score even
	// though the category was classified as clear.
	for group, members := range map[string][]string{
		GroupLiability:    {CatLimitationOfLiability, CatUncappedLiability},
		GroupIndemnity:    {CatIndemnification, CatOneSidedIndemnity},
		GroupTermination:  {CatTerminationRight, CatUnilateralTermination},
		GroupRenewal:      {CatAutoRenewal},
		GroupJurisdiction: {CatJurisdictionRelated},
		GroupObligation:   {CatUnusualObligation},
	} {
		any := false
		for _, m := range members {
			if state[m] != StateClear {
				any = true
				break
			}
		}
		contributes[group] = any
		if !any {
			risk[group] = 0
		}
	}
	return risk, contributes
}

// contributingGroups returns the groups allowed to move the score, ordered by
// descending weight then by group id.
func contributingGroups(readings []CategoryReading, cfg Config) []GroupContribution {
	byGroup := map[string]bool{}
	for _, r := range readings {
		if r.GroupContributes {
			byGroup[r.Group] = true
		}
	}
	var out []GroupContribution
	for _, group := range cfg.GroupsInWeightOrder() {
		if !byGroup[group] {
			continue
		}
		var risk float64
		for _, r := range readings {
			if r.Group == group {
				risk = r.GroupRisk
				break
			}
		}
		weight := groupWeightOf(cfg, group)
		out = append(out, GroupContribution{
			Group:  group,
			Weight: weight,
			Risk:   clamp01(risk),
			Term:   weight * clamp01(risk),
		})
	}
	return out
}

// combine reduces the group contributions to a single score.
//
// noisy-or is the base: 1 - prod(1 - term). It is monotone, stays inside [0,1]
// without a clamp, and gives several moderate findings compounding weight.
//
// The severity floor then raises the score when a single severe finding would
// otherwise be diluted below its own weight. See AggregatorNoisyORFloored for
// why that dilution is a defect and not a preference.
func combine(groups []GroupContribution, aggregator string, maxWeight float64) float64 {
	if len(groups) == 0 {
		return 0
	}

	// The strongest single contribution, rescaled by the largest group weight so
	// the most severe category in the taxonomy can reach 1.0 on its own.
	floor := 0.0
	if maxWeight > 0 {
		for _, g := range groups {
			if scaled := g.Term / maxWeight; scaled > floor {
				floor = scaled
			}
		}
	}

	switch aggregator {
	case AggregatorWeightedSum:
		sum := 0.0
		for _, g := range groups {
			sum += g.Term
		}
		return clamp01(sum)
	case AggregatorNoisyOR:
		remaining := 1.0
		for _, g := range groups {
			remaining *= 1 - g.Term
		}
		return clamp01(1 - remaining)
	default:
		remaining := 1.0
		for _, g := range groups {
			remaining *= 1 - g.Term
		}
		return clamp01(math.Max(1-remaining, floor))
	}
}

func riskLevel(score float64, t Thresholds) string {
	switch {
	case score >= t.Critical:
		return "critical"
	case score >= t.High:
		return "high"
	case score >= t.Medium:
		return "medium"
	default:
		return "low"
	}
}

// overallConfidence is the minimum confidence across contributing
// risk-polarity categories, or the maximum across all categories when nothing
// contributed.
//
// The minimum is deliberate. Taking the maximum lets one confident category
// vouch for eight unconfident ones, which is optimistic in exactly the
// wrong direction for a tool whose output a lawyer may rely on.
func overallConfidence(readings []CategoryReading, t Thresholds) (float64, bool) {
	contributors := make([]CategoryReading, 0, len(readings))
	for _, r := range readings {
		if r.Polarity == PolarityRisk && r.State != StateClear && r.GroupContributes {
			contributors = append(contributors, r)
		}
	}

	uncertain := false
	for _, r := range contributors {
		if r.State == StateUncertain {
			uncertain = true
		}
	}

	if len(contributors) == 0 {
		best := 0.0
		for _, r := range readings {
			if r.Confidence > best {
				best = r.Confidence
			}
		}
		// Nothing contributed. Confidence reports how sure the model is that
		// nothing applies, so it is the strongest reading available.
		return best, false
	}

	lowest := 1.0
	for _, r := range contributors {
		if r.Confidence < lowest {
			lowest = r.Confidence
		}
	}
	needsReview := uncertain || lowest < t.ReviewConfidenceFloor
	return lowest, needsReview
}

// maxGroupWeight returns the largest configured group weight, used to rescale
// the severity floor.
func maxGroupWeight(cfg Config) float64 {
	maxWeight := 0.0
	for _, group := range cfg.GroupsInWeightOrder() {
		if w := groupWeightOf(cfg, group); w > maxWeight {
			maxWeight = w
		}
	}
	return maxWeight
}

// mergeReadings folds the readings of several analysis windows for one clause
// into a single set, taking the maximum probability per category.
//
// Merging before aggregation matters: averaging windows would let a clause
// hide a liability carve-out that only one window saw, and would cancel a
// strong signal against a weak one.
func mergeReadings(sets []Readings) Readings {
	merged := Readings{}
	for _, set := range sets {
		for id, pair := range set {
			if existing, ok := merged[id]; !ok || pair.Probability > existing.Probability {
				merged[id] = pair
			}
		}
	}
	return merged
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
