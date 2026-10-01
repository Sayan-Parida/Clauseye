package main

import (
	"fmt"
	"regexp"
	"strings"
)

// Clause preparation: eligibility filtering, numbering removal and sentence
// windowing.
//
// All of this runs on already-PII-redacted text and inside the Go process, so
// it never adds a network hop.

var (
	// clauseNumbering matches a leading structural marker. It mirrors the
	// CLAUSE_START pattern the frontend splits on, so the two stay aligned.
	//
	// The marker is stripped before analysis. "5." or "(a)" consumes model
	// budget and can be read as content; the clause text is what matters.
	clauseNumbering = regexp.MustCompile(`(?i)^\s*(?:(?:section|article|clause)\s+\d+(?:\.\d+)*|\d+(?:\.\d+)*\.?|\([a-z0-9]{1,4}\)|[ivx]+\.)\s+`)
	// sentenceBoundary matches the separator that follows a sentence: terminal
	// punctuation plus trailing closing quotes and the whitespace after it. The
	// punctuation is part of the match so the sentence slice can include it;
	// only the trailing whitespace is dropped.
	sentenceBoundary = regexp.MustCompile(`(?:[.!?]+[)"'”’]*)\s+|\n+`)
	// legalSignals are the tokens that mark a span as contractual prose. Used
	// for eligibility, not for risk: a clause lacking any of them is boilerplate
	// or a heading, and scoring it produces noise.
	legalSignals = []string{
		"shall", "agreement", "party", "parties", "indemnif", "terminat",
		"liabilit", "obligat", "warrant", "covenant", "breach", "governing law",
		"jurisdiction", "confidential", "payment", "fees", "notice", "consent",
		"assign", "subcontract", "comply", "procurement", "vendor", "supplier",
		"client", "licence", "license", "confidentiality", "damages", "indemnity",
	}
)

// ClauseInput is a clause that passed eligibility.
type ClauseInput struct {
	Index int
	// Text is the analysis-ready clause: numbering stripped, whitespace
	// collapsed. This is what gets redacted and sent to the model.
	Text string
	// Original is what the client sent, returned verbatim in the response so
	// the user reads their own contract, not a normalised copy.
	Original string
	// Windows is how many analysis windows Text splits into. Normally 1.
	Windows int
}

// PrepareClauses filters, normalises and windows the submitted clauses.
//
// Every rejection is reported in skipped[] rather than dropped, so the report
// states how much of the contract was actually assessed.
func PrepareClauses(clauses []string, cfg Config) (eligible []ClauseInput, skipped []SkippedClause) {
	eligible = make([]ClauseInput, 0, len(clauses))
	skipped = make([]SkippedClause, 0)

	for i, raw := range clauses {
		original := strings.TrimSpace(raw)
		text := normaliseClause(original)

		if len(text) < cfg.MinClauseChars {
			skipped = append(skipped, SkippedClause{
				ClauseIndex: i,
				Reason:      SkipTooShort,
				Detail:      fmt.Sprintf("%d characters after removing the clause number", len(text)),
			})
			continue
		}
		if countLegalSignals(text) == 0 {
			skipped = append(skipped, SkippedClause{
				ClauseIndex: i,
				Reason:      SkipNoContractSignal,
				Detail:      "no contractual language found",
			})
			continue
		}

		windows := windowText(text, cfg)
		eligible = append(eligible, ClauseInput{
			Index:    i,
			Text:     text,
			Original: original,
			Windows:  len(windows),
		})
	}
	return eligible, skipped
}

// normaliseClause strips the structural numbering and collapses whitespace.
func normaliseClause(raw string) string {
	text := clauseNumbering.ReplaceAllString(strings.TrimSpace(raw), "")
	text = whitespaceRun.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

func countLegalSignals(lower string) int {
	count := 0
	for _, signal := range legalSignals {
		if strings.Contains(lower, signal) {
			count++
		}
	}
	return count
}

// HasDocumentSignal reports whether the submission looks like a contract at
// all, which is an abuse guard rather than a risk judgement. It is a
// process-wide CPU guard: the model is free, but the CPU is not.
func HasDocumentSignal(clauses []string) bool {
	lowered := make([]string, len(clauses))
	for i, c := range clauses {
		lowered[i] = strings.ToLower(c)
	}
	joined := strings.Join(lowered, " ")
	matches := 0
	for _, term := range legalTerms {
		if strings.Contains(joined, term) {
			matches++
			if matches >= 2 {
				return true
			}
		}
	}
	return matches >= 2
}

// legalTerms is the document-level guard set, matching the pre-existing check.
var legalTerms = []string{
	"shall", "agreement", "party", "parties", "clause", "indemnify",
	"terminate", "liability", "jurisdiction", "obligation",
}

// windowText splits a clause into analysis windows.
//
// This exists because the Laya checkpoint is configured with max_len=512 and
// build_sequence silently right-truncates the state once the question head has
// taken its share of the budget. A long clause would lose its tail, and the
// tail is where liability carve-outs and termination mechanics live. Windows
// keep every token in view, and the overlap keeps a sentence that straddles a
// boundary from being cut in half.
func windowText(text string, cfg Config) []string {
	if len(text) <= cfg.WindowChars {
		return []string{text}
	}

	sentences := splitSentences(text)
	var windows []string
	var current strings.Builder

	flush := func() {
		if current.Len() == 0 {
			return
		}
		windows = append(windows, strings.TrimSpace(current.String()))
		current.Reset()
	}

	for _, sentence := range sentences {
		// A single sentence longer than the window is hard-split rather than
		// dropped, so no text is ever silently skipped.
		if len(sentence) > cfg.WindowChars {
			flush()
			for start := 0; start < len(sentence); start += cfg.WindowChars {
				end := start + cfg.WindowChars
				if end > len(sentence) {
					end = len(sentence)
				}
				windows = append(windows, strings.TrimSpace(sentence[start:end]))
			}
			continue
		}
		if current.Len() > 0 && current.Len()+1+len(sentence) > cfg.WindowChars {
			flush()
			// Overlap: carry the tail of the previous window forward so a
			// sentence split across the boundary is still seen whole by at
			// least one window.
			if carry := overlapTail(current.String(), cfg.WindowOverlapChars); carry != "" {
				current.WriteString(carry)
				current.WriteString(" ")
			}
		}
		if current.Len() > 0 {
			current.WriteString(" ")
		}
		current.WriteString(sentence)
	}
	flush()

	if len(windows) == 0 {
		return []string{text}
	}
	return windows
}

// overlapTail returns the trailing text of the window just flushed, snapped
// forward to the next word boundary so the overlap does not open mid-word.
//
// A half-word at the start of an overlap window is cosmetically poor but not
// harmful, because the complete word was fully present in the window it came
// from. Snapping forward loses a little more context than necessary; keeping
// the raw cut would sometimes begin with a fragment. Forward is chosen.
func overlapTail(previous string, n int) string {
	if n <= 0 || previous == "" {
		return ""
	}
	tail := previous
	if len(tail) > n {
		tail = tail[len(tail)-n:]
	}
	if idx := strings.IndexByte(tail, ' '); idx >= 0 {
		tail = tail[idx+1:]
	}
	return strings.TrimSpace(tail)
}

// splitSentences breaks text into sentences, keeping terminal punctuation
// attached to the sentence it ends.
//
// It slices on boundary offsets rather than using regexp.Split, which would
// consume the punctuation as part of the delimiter and drop it. Losing the
// full stop matters: evidence patterns rely on sentence structure, and the
// clause text sent to the model should read the way the contract was written.
func splitSentences(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	bounds := sentenceBoundary.FindAllStringIndex(text, -1)
	out := make([]string, 0, len(bounds)+1)

	start := 0
	emit := func(end int) {
		sentence := strings.TrimSpace(text[start:end])
		if sentence != "" {
			out = append(out, sentence)
		}
	}

	for _, b := range bounds {
		// The match spans the terminal punctuation and the whitespace after it,
		// so slicing to b[1] keeps the full stop. TrimSpace drops the trailing
		// whitespace and any newline the alternative branch matched.
		emit(b[1])
		start = b[1]
	}
	emit(len(text))

	return out
}
