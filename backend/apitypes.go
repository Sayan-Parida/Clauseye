package main

// Schema v2 wire types.
//
// Version 2 keeps every field version 1 emitted (clause, risk_level,
// confidence, reason) so an older client keeps rendering, and adds the
// per-category findings, the numeric score and the skip/meta blocks.
//
// Two fields exist specifically to prevent a specific misreading. For a `noul`
// question the model returns `noul` = P(true) and `confidence` = max(p, 1-p),
// so a category the system did NOT flag can still carry confidence 0.94. A
// client that renders confidence next to detected=false tells a lawyer "94%
// risky" about a finding the system considers clear. Probability and
// confidence are therefore always separate fields, and Issue.Detected is
// derived from Probability, never from Confidence.

// SchemaVersion is the version of the analyze response contract.
const SchemaVersion = 2

// Clause states.
const (
	StateDetected  = "detected"
	StateUncertain = "uncertain"
	StateClear     = "clear"
)

// Skip reasons.
const (
	SkipTooShort         = "too_short"
	SkipNoContractSignal = "no_contract_signal"
)

// Issue is one risk category evaluated against one clause.
type Issue struct {
	Category string   `json:"category"`
	Label    string   `json:"label"`
	Polarity Polarity `json:"polarity"`
	// Reliability carries how well this category separated on the golden set.
	// "weak" means it fires on clauses where it should be clear, or rarely
	// fires where it should. The client is expected to show that rather than
	// presenting a weak category as though it were as trustworthy as a measured
	// one.
	Reliability Reliability `json:"reliability"`
	// State is detected, uncertain or clear.
	State    string `json:"state"`
	Detected bool   `json:"detected"`
	// Probability is P(the category applies to this clause). This is the number
	// that means "how likely is this a risk".
	Probability float64 `json:"probability"`
	// Confidence is the model's certainty about its own answer. It is high for
	// a confident "no" and must not be read as risk likelihood.
	Confidence float64 `json:"confidence"`
	// Weight is this category's group weight, exposed so a client can explain
	// why one finding outranks another.
	Weight float64 `json:"weight"`
	// GroupRisk is the guarded group value this category fed into, after
	// damping and conjunction. Equal for members of the same group.
	GroupRisk float64 `json:"group_risk"`
	// Contributes reports whether this category raised the score.
	Contributes bool `json:"contributes"`
	// Explanation is a deterministic composition of the category rationale and,
	// when found, a verbatim quote from the clause. Never generated.
	Explanation string `json:"explanation"`
	// Evidence is the verbatim clause substring that corroborates the finding.
	Evidence string `json:"evidence,omitempty"`
	// EvidenceLabel names which pattern matched.
	EvidenceLabel string `json:"evidence_label,omitempty"`
	// EvidenceFound is false when the model flagged the category but no
	// corroborating language pattern was found in the clause. The UI surfaces
	// this rather than hiding it, because it means the finding rests on the
	// model alone.
	EvidenceFound bool `json:"evidence_found"`
}

// ClauseLocation identifies the source clause.
type ClauseLocation struct {
	ClauseIndex int `json:"clause_index"`
}

// AnalysisResult is one analysed clause.
type AnalysisResult struct {
	Clause      string  `json:"clause"`
	ClauseIndex int     `json:"clause_index"`
	RiskLevel   string  `json:"risk_level"`
	RiskScore   float64 `json:"risk_score"`
	Confidence  float64 `json:"confidence"`
	NeedsReview bool    `json:"needs_review"`
	// Explanation is the document-level narrative for this clause.
	Explanation string `json:"explanation"`
	// Reason is the single-line summary. Present since schema v1; clients that
	// only know "reason" now get readable text instead of a bare score echo.
	Reason   string          `json:"reason"`
	Issues   []Issue         `json:"issues"`
	Location *ClauseLocation `json:"location,omitempty"`
}

// SkippedClause reports a clause that was not analysed, and why. Silently
// dropping a clause would understate the coverage of the report.
type SkippedClause struct {
	ClauseIndex int    `json:"clause_index"`
	Reason      string `json:"reason"`
	Detail      string `json:"detail,omitempty"`
}

// ResponseMeta describes how the result was produced.
type ResponseMeta struct {
	Engine          string `json:"engine"`
	Model           string `json:"model,omitempty"`
	Aggregator      string `json:"aggregator"`
	Calibration     string `json:"calibration"`
	ClausesReceived int    `json:"clauses_received"`
	ClausesAnalyzed int    `json:"clauses_analyzed"`
	ClausesSkipped  int    `json:"clauses_skipped"`
	WindowsAnalyzed int    `json:"windows_analyzed"`
	DurationMS      int64  `json:"duration_ms"`
	// Partial is true when some chunks failed but others succeeded.
	Partial bool `json:"partial"`
}

// AnalyzeResponse is the schema v2 response body.
type AnalyzeResponse struct {
	SchemaVersion int              `json:"schema_version"`
	Results       []AnalysisResult `json:"results"`
	Skipped       []SkippedClause  `json:"skipped"`
	Meta          ResponseMeta     `json:"meta"`
	Warnings      []string         `json:"warnings,omitempty"`
}
