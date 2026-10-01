package main

import (
	"regexp"
	"sort"
)

// Category identifiers. These are the wire names: they appear in the API
// response, in sessionStorage on the client, and in the question ids the model
// is asked. Renaming one is a breaking API change.
const (
	CatLimitationOfLiability = "limitation_of_liability"
	CatUncappedLiability     = "uncapped_liability"
	CatIndemnification       = "indemnification"
	CatOneSidedIndemnity     = "one_sided_indemnity"
	CatTerminationRight      = "termination_right"
	CatUnilateralTermination = "unilateral_termination"
	CatAutoRenewal           = "auto_renewal"
	CatJurisdictionRelated   = "jurisdiction_related"
	CatUnusualObligation     = "unusual_obligation"
)

// Risk groups. Categories are combined within a group before the groups are
// weighted, so that nested categories do not double-count.
const (
	GroupLiability    = "liability"
	GroupIndemnity    = "indemnity"
	GroupTermination  = "termination"
	GroupRenewal      = "renewal"
	GroupJurisdiction = "jurisdiction"
	GroupObligation   = "obligation"
)

// Polarity says which way a category cuts.
type Polarity string

const (
	// PolarityRisk raises the risk score.
	PolarityRisk Polarity = "risk"
	// PolarityProtective lowers it. A liability cap is protective, so its
	// presence must damp the score rather than add to it.
	PolarityProtective Polarity = "protective"
	// PolarityContext is not itself a risk. It exists to gate another category:
	// termination is only a hazard when a termination right exists at all.
	PolarityContext Polarity = "context"
)

// Reliability records how well a category performed against the golden set.
//
// PROVISIONAL. These values come from scripts/validate_taxonomy.py run over
// testdata/clauses_golden.json on CPU, and that set's labels were written by the
// author of this system, so they measure agreement with one reader rather than
// accuracy against the law. They are still worth carrying, because the relative
// ordering is stark: some categories separate cleanly and others barely fire at
// all. A category that fired on 25% of the clauses where it should have been
// clear is not a category that should reach a lawyer behind a confident badge.
type Reliability string

const (
	// ReliabilityMeasured: recall 1.00 on the golden set, false-positive rate at
	// or below 0.38. Usable as a finding.
	ReliabilityMeasured Reliability = "measured"
	// ReliabilityWeak: fires on clauses where it should be clear, or rarely
	// fires where it should. Reported, and flagged in the UI.
	ReliabilityWeak Reliability = "weak"
)

// EvidenceRule is a deterministic language pattern used to corroborate a model
// detection by quoting the clause back.
//
// These regexes run in Go's RE2 engine: no lookaround, no backreferences.
// A match is a verbatim substring of the clause, never a generated paraphrase,
// so an explanation can never misquote a contract.
type EvidenceRule struct {
	Label string
	Re    *regexp.Regexp
}

// Category is one risk dimension the system evaluates.
type Category struct {
	ID       string
	Label    string
	Polarity Polarity
	Group    string
	// Reliability is surfaced to the client so a weak category is not presented
	// as though it were as trustworthy as a measured one.
	Reliability Reliability
	// Rationale is the static legal explanation. It is the human-reviewable
	// source of every explanation string the API emits.
	Rationale string
	Evidence  []EvidenceRule
}

func rule(label, pattern string) EvidenceRule {
	return EvidenceRule{Label: label, Re: regexp.MustCompile(pattern)}
}

// taxonomy is the single source of truth for the nine categories.
//
// ORDER MATTERS: it defines the order of issues in every API response and of
// probability vectors in the golden set. Append only; do not reorder, because
// that silently changes the meaning of stored probability fixtures.
//
// The Reliability values below are measured, not assumed. See
// Reliability above, and scripts/validate_taxonomy.py to reproduce them.
var taxonomy = []Category{
	{
		ID:          CatLimitationOfLiability,
		Label:       "Limitation of liability",
		Polarity:    PolarityProtective,
		Group:       GroupLiability,
		Reliability: ReliabilityWeak,
		Rationale: "A monetary cap on liability, or an exclusion of indirect loss, " +
			"bounds how much can be claimed when things go wrong. Its presence generally " +
			"reduces this clause's risk rather than adding to it.",
		Evidence: []EvidenceRule{
			rule("liability ceiling", `(?i)\bnot\s+(?:be\s+|in\s+any\s+event\s+(?:be\s+)?)?exceed\b`),
			rule("aggregate liability", `(?i)\b(?:aggregate|total)\s+liability\b`),
			rule("no indirect loss", `(?i)\bin\s+no\s+event\s+shall\b`),
			rule("consequential loss excluded", `(?i)\bconsequential\s+(?:damages|losses|loss)\b`),
			rule("sole remedy", `(?i)\bsole\s+(?:and\s+exclusive\s+)?remed(?:y|ies)\b`),
			rule("liability cap", `(?i)\b(?:cap|capped|capping)\s+(?:on\s+|of\s+)?liabilit`),
		},
	},
	{
		ID:          CatUncappedLiability,
		Label:       "Uncapped liability",
		Polarity:    PolarityRisk,
		Group:       GroupLiability,
		Reliability: ReliabilityMeasured,
		Rationale: "Liability with no monetary ceiling leaves the amount of loss open-ended, " +
			"so the exposure cannot be quantified, priced or reserved for in advance.",
		Evidence: []EvidenceRule{
			rule("unlimited", `(?i)\bunlimited\b|\bwithout\s+(?:any\s+)?(?:ceiling|cap|limit)`),
			rule("without limitation", `(?i)\bwithout\s+(?:any\s+)?limitation\b`),
			rule("all losses", `(?i)\ball\s+(?:losses|claims|damages)\s+howsoever\s+arising\b`),
			rule("no cap", `(?i)\bno\s+(?:limit|maximum|ceiling)\s+(?:of|on|to)?\s*liabilit`),
			rule("whatsoever", `(?i)\bwhatsoever\s+(?:loss|claim|damage)`),
		},
	},
	{
		ID:          CatIndemnification,
		Label:       "Indemnification",
		Polarity:    PolarityRisk,
		Group:       GroupIndemnity,
		Reliability: ReliabilityMeasured,
		Rationale: "An indemnity shifts responsibility for third-party claims onto one party, " +
			"typically covering defence costs, settlements and judgements as well as the " +
			"underlying loss.",
		Evidence: []EvidenceRule{
			rule("indemnity", `(?i)\bindemnif(?:y|ies|ied|ication|ications)\b`),
			rule("hold harmless", `(?i)\bhold\s+harmless\b`),
			rule("defend and indemnify", `(?i)\bdefend\s+(?:and\s+)?(?:indemnify|hold\s+harmless)`),
			rule("defend any claim", `(?i)\bdefend\s+(?:any|all|such)\s+(?:third[- ]party\s+)?(?:claim|loss|damage|suit|action)`),
		},
	},
	{
		ID:          CatOneSidedIndemnity,
		Label:       "One-sided indemnity",
		Polarity:    PolarityRisk,
		Group:       GroupIndemnity,
		Reliability: ReliabilityWeak,
		Rationale: "The indemnity obliges one party to cover the other with no reciprocal " +
			"obligation in return, so the whole of the third-party risk sits on one side " +
			"of the agreement.",
		Evidence: []EvidenceRule{
			rule("asymmetry denied", `(?i)\bno\s+(?:reciprocal|corresponding|mutual|like)\s+(?:indemnit|obligation|liabilit|remed)`),
			rule("sole expense", `(?i)\b(?:at|for)\s+(?:its|their)\s+sole\s+(?:expense|cost)`),
			rule("indemnifies counterparty", `(?i)\bindemnif(?:y|ies)\b[^.]{0,140}\b(?:client|customer|purchaser|buyer|consumer)\b`),
			rule("indemnifies vendor", `(?i)\bindemnif(?:y|ies)\b[^.]{0,140}\b(?:vendor|supplier|provider|contractor|seller)\b`),
			rule("all claims", `(?i)\bagainst\s+all\s+claims\b`),
		},
	},
	{
		ID:          CatTerminationRight,
		Label:       "Termination right",
		Polarity:    PolarityContext,
		Group:       GroupTermination,
		Reliability: ReliabilityWeak,
		Rationale: "This clause creates or defines a right to end the agreement. It is " +
			"context rather than risk in itself: the hazard lies in whether that right is " +
			"exercisable by one party alone.",
		Evidence: []EvidenceRule{
			rule("right to terminate", `(?i)\bright\s+to\s+terminate\b`),
			rule("termination for breach", `(?i)\b(?:material|substantial)\s+breach\b`),
			rule("may terminate", `(?i)\bmay\s+terminate\b`),
			rule("terminate this agreement", `(?i)\bterminate\s+this\s+agreement\b`),
		},
	},
	{
		ID:          CatUnilateralTermination,
		Label:       "Unilateral termination",
		Polarity:    PolarityRisk,
		Group:       GroupTermination,
		Reliability: ReliabilityWeak,
		Rationale: "One party can end the agreement on its own initiative, without a breach " +
			"having occurred and without giving notice, so the other party's investment and " +
			"reliance have no protection.",
		Evidence: []EvidenceRule{
			rule("no notice", `(?i)\bat\s+any\s+time\s+(?:and\s+(?:without\s+(?:any\s+)?notice|with\s+or\s+without\s+notice)|without\s+(?:any\s+)?notice)\b`),
			rule("sole discretion", `(?i)\bsole\s+(?:and\s+(?:absolute\s+)?)?discretion\b`),
			rule("for convenience", `(?i)\bfor\s+convenience\b`),
			rule("without cause", `(?i)\bwithout\s+cause\b`),
			rule("immediately", `(?i)\bimmediately\s+terminate\b`),
		},
	},
	{
		ID:          CatAutoRenewal,
		Label:       "Auto-renewal",
		Polarity:    PolarityRisk,
		Group:       GroupRenewal,
		Reliability: ReliabilityMeasured,
		Rationale: "The agreement renews itself unless a party acts in time, so a missed " +
			"notice deadline extends the term and the fees attached to it without any " +
			"fresh agreement.",
		Evidence: []EvidenceRule{
			rule("automatic renewal", `(?i)\bautomatic(?:ally)?\s+renew`),
			rule("successive terms", `(?i)\bsuccessive\s+(?:one[- ]year|term|renewal|period)`),
			rule("unless notice", `(?i)\bunless\s+(?:either|each|both)?\s*(?:party|parties)?\s*(?:gives?|provides?|delivers?|serves?)\s+[^.]{0,40}?notice`),
			rule("evergreen", `(?i)\bevergreen\b`),
			rule("renewal term", `(?i)\brenewal\s+term\b`),
		},
	},
	{
		ID:          CatJurisdictionRelated,
		Label:       "Jurisdiction and governing law",
		Polarity:    PolarityRisk,
		Group:       GroupJurisdiction,
		Reliability: ReliabilityMeasured,
		Rationale: "The clause fixes which law applies and where disputes are heard, which " +
			"determines cost, enforceability, and how far the other party must travel to " +
			"contest a claim.",
		Evidence: []EvidenceRule{
			rule("governing law", `(?i)\bgoverning\s+law\b`),
			rule("exclusive jurisdiction", `(?i)\bexclusive\s+jurisdiction\b`),
			rule("courts", `(?i)\bcourts\s+of\s+(?:the\s+)?[A-Z]`),
			rule("arbitration", `(?i)\barbitrat`),
			rule("venue", `(?i)\bvenue\s+(?:shall|will|is|for)\b`),
			rule("jurisdiction clause", `(?i)\bjurisdiction\b`),
		},
	},
	{
		ID:          CatUnusualObligation,
		Label:       "Unusual or one-sided obligation",
		Polarity:    PolarityRisk,
		Group:       GroupObligation,
		Reliability: ReliabilityWeak,
		Rationale: "The obligation is one-sided or falls outside normal market practice, so it " +
			"can require performance beyond what a balanced contract would ask of either party.",
		Evidence: []EvidenceRule{
			rule("sole discretion", `(?i)\bsole\s+(?:and\s+(?:absolute\s+)?)?discretion\b`),
			rule("best endeavours", `(?i)\bbest\s+endeavou?rs\b`),
			rule("at all times", `(?i)\bat\s+all\s+times\b`),
			rule("unlimited", `(?i)\bunlimited\b`),
			rule("non-refundable", `(?i)\bnon-refundable\b|\bnon-cancellable\b`),
		},
	},
}

var (
	categoryByID = map[string]*Category{}
	// categoryOrder preserves taxonomy order for stable output.
	categoryOrder []string
)

func init() {
	for i := range taxonomy {
		categoryByID[taxonomy[i].ID] = &taxonomy[i]
		categoryOrder = append(categoryOrder, taxonomy[i].ID)
	}
}

// CategoryIDs returns the category identifiers in taxonomy order.
func CategoryIDs() []string {
	out := make([]string, len(categoryOrder))
	copy(out, categoryOrder)
	return out
}

// LookupCategory returns the category with the given id.
func LookupCategory(id string) (*Category, bool) {
	c, ok := categoryByID[id]
	return c, ok
}

// GroupsInWeightOrder lists the six risk groups ordered by descending weight,
// then by id for stability. Used to render a deterministic "what drove this
// score" list.
func (c Config) GroupsInWeightOrder() []string {
	type weighted struct {
		id     string
		weight float64
	}
	groups := []weighted{
		{GroupLiability, c.GroupWeights.Liability},
		{GroupIndemnity, c.GroupWeights.Indemnity},
		{GroupTermination, c.GroupWeights.Termination},
		{GroupRenewal, c.GroupWeights.Renewal},
		{GroupObligation, c.GroupWeights.Obligation},
		{GroupJurisdiction, c.GroupWeights.Jurisdiction},
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].weight != groups[j].weight {
			return groups[i].weight > groups[j].weight
		}
		return groups[i].id < groups[j].id
	})
	out := make([]string, len(groups))
	for i, g := range groups {
		out[i] = g.id
	}
	return out
}

// riskLevelNames are the four levels, ascending.
var riskLevelNames = []string{"low", "medium", "high", "critical"}
