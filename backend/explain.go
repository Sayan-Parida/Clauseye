package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Explanation generation is deterministic.
//
// The Laya checkpoint is a bidirectional encoder with a decision head that
// emits one probability per masked option. It has no autoregressive decode path
// and cannot emit prose, so asking it to explain a finding is a category error.
// Every string in this file is therefore composed, never generated:
//
//	rationale (static, human-written, legally reviewable)
//	  + " In this clause: %q." (a verbatim substring of the clause)
//
// The consequences are worth stating plainly: no hallucination is possible, no
// extra latency or cost is incurred, and a lawyer can review the text of every
// explanation the product can emit before it ships.

// maxEvidenceChars bounds a quoted evidence span so a pathological clause cannot
// echo an entire paragraph back through the API.
const maxEvidenceChars = 240

var whitespaceRun = regexp.MustCompile(`\s+`)

// buildIssue turns one reading into an API issue, including its explanation and
// the evidence corroborating it.
func buildIssue(r CategoryReading, clause string, cfg Config) Issue {
	cat, ok := LookupCategory(r.Category)
	if !ok {
		// Unreachable via Aggregate, which iterates the taxonomy. Guarded so an
		// unknown id can never produce an empty explanation silently.
		return Issue{Category: r.Category, State: r.State, Detected: r.State == StateDetected,
			Probability: r.Probability, Confidence: r.Confidence}
	}

	issue := Issue{
		Category:    r.Category,
		Label:       cat.Label,
		Polarity:    cat.Polarity,
		Reliability: cat.Reliability,
		State:       r.State,
		Detected:    r.State == StateDetected,
		Probability: r.Probability,
		Confidence:  r.Confidence,
		GroupRisk:   r.GroupRisk,
		Contributes: r.GroupContributes && cat.Polarity == PolarityRisk && r.State != StateClear,
	}

	if cat.Polarity == PolarityRisk {
		issue.Weight = groupWeightOf(cfg, cat.Group)
	}

	// A clear category carries no explanation. buildIssues normally filters these
	// out, but when IncludeClearIssues is on they are reported as bare states
	// rather than padded with rationale text nobody asked for.
	if r.State == StateClear {
		return issue
	}

	evidence, label := findEvidence(clause, cat)
	issue.Evidence = evidence
	issue.EvidenceLabel = label
	issue.EvidenceFound = evidence != ""
	issue.Explanation = composeExplanation(cat, evidence)
	return issue
}

// composeExplanation builds the per-issue explanation. It never invents a
// quotation: with no matching pattern the rationale stands alone and
// EvidenceFound stays false.
func composeExplanation(cat *Category, evidence string) string {
	var b strings.Builder
	b.WriteString(cat.Rationale)
	if evidence != "" {
		fmt.Fprintf(&b, " In this clause: %q.", evidence)
	}
	return b.String()
}

// findEvidence returns the first rule that matches the clause, along with a
// cleaned verbatim span. Rules are ordered most specific first so a targeted
// phrase wins over a generic one.
func findEvidence(clause string, cat *Category) (string, string) {
	if clause == "" {
		return "", ""
	}
	for _, r := range cat.Evidence {
		loc := r.Re.FindStringIndex(clause)
		if loc == nil {
			continue
		}
		return cleanEvidence(clause[loc[0]:loc[1]]), r.Label
	}
	return "", ""
}

// cleanEvidence collapses whitespace and bounds the span length.
func cleanEvidence(span string) string {
	span = strings.TrimSpace(whitespaceRun.ReplaceAllString(span, " "))
	if span == "" {
		return ""
	}
	if len(span) > maxEvidenceChars {
		span = span[:maxEvidenceChars]
		// Do not end mid-word.
		if idx := strings.LastIndexByte(span, ' '); idx > maxEvidenceChars/2 {
			span = span[:idx]
		}
		span += "..."
	}
	return span
}

// buildIssues renders the API issues for an aggregation, in taxonomy order.
//
// Categories that are clear are omitted unless IncludeClearIssues is set, so
// the common case carries only the findings a reviewer needs to look at.
func buildIssues(agg Aggregation, clause string, cfg Config) []Issue {
	issues := make([]Issue, 0, len(agg.Readings))
	for _, r := range agg.Readings {
		if r.State == StateClear && !cfg.IncludeClearIssues {
			continue
		}
		issues = append(issues, buildIssue(r, clause, cfg))
	}
	return issues
}

// composeNarrative builds the clause-level explanation and the one-line reason.
//
// Both are assembled from the same deterministic parts, ordered by group weight
// so the most consequential finding leads.
func composeNarrative(agg Aggregation, _ Config) (explanation string, reason string) {
	if len(agg.Contributors) == 0 {
		if agg.NeedsReview {
			return ("No risk category was detected above the review threshold, but the model " +
					"was not confident enough to rule the clause out. It should be read."),
				"low: no category detected, confidence below review floor"
		}
		return ("No risk category was detected above the review threshold for this clause."),
			"low: no category detected"
	}

	labels := make([]string, 0, len(agg.Contributors))
	ids := make([]string, 0, len(agg.Contributors))
	figures := make([]string, 0, len(agg.Contributors))
	for _, c := range agg.Contributors {
		cat := categoryByID[c.Category]
		labels = append(labels, fmt.Sprintf("%s (probability %.2f)", cat.Label, c.Probability))
		ids = append(ids, cat.ID)
		figures = append(figures, fmt.Sprintf("%s p=%.2f", cat.ID, c.Probability))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s risk: %s this score: %s.",
		capitalise(agg.Level),
		plural(len(labels), "finding drives", "findings drive"),
		joinAnd(labels))
	if agg.NeedsReview {
		b.WriteString(" At least one category sits in the uncertain band, or the model was not " +
			"confident enough, so this clause should be read by a person.")
	}

	return b.String(), fmt.Sprintf("%s: %s", agg.Level, strings.Join(figures, ", "))
}

// capitalise upper-cases the first letter so a level reads as a sentence.
func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// joinAnd renders a human list: "a", "a and b", "a, b and c".
func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	default:
		return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return strconv.Itoa(n) + " " + one
	}
	return strconv.Itoa(n) + " " + many
}
