package main

import (
	"strings"
	"testing"
)

func TestPrepareClausesSkipsShortClauses(t *testing.T) {
	cfg := defaultConfig()
	clauses := []string{
		"1. The Vendor shall indemnify the Client against all claims without limitation of liability.",
		"Notices.",
		"2. Definitions and interpretation.",
		"3. This agreement shall be governed by the laws of England and Wales.",
	}
	eligible, skipped := PrepareClauses(clauses, cfg)

	if len(eligible) != 2 {
		t.Fatalf("expected 2 eligible clauses, got %d", len(eligible))
	}
	if len(skipped) != 2 {
		t.Fatalf("expected 2 skipped clauses, got %d: %+v", len(skipped), skipped)
	}
	for _, s := range skipped {
		if s.Reason != SkipTooShort && s.Reason != SkipNoContractSignal {
			t.Fatalf("unexpected skip reason %q", s.Reason)
		}
		if s.ClauseIndex != 1 && s.ClauseIndex != 2 {
			t.Fatalf("skip reported index %d, want 1 or 2", s.ClauseIndex)
		}
	}
}

func TestPrepareClausesNeverSilentlyDrops(t *testing.T) {
	cfg := defaultConfig()
	clauses := []string{
		"The Vendor shall indemnify the Client against all claims without limitation.",
		"x",
		"Table of contents",
		"This agreement shall be governed by the laws of England and Wales.",
	}
	eligible, skipped := PrepareClauses(clauses, cfg)
	if len(eligible)+len(skipped) != len(clauses) {
		t.Fatalf("accounting mismatch: %d eligible + %d skipped != %d submitted",
			len(eligible), len(skipped), len(clauses))
	}
}

func TestPrepareClausesDistinguishesBoilerplate(t *testing.T) {
	cfg := defaultConfig()
	// Long enough to pass the length gate, but with no contractual language.
	boilerplate := "This document has been prepared for internal discussion only and is not for distribution."
	eligible, skipped := PrepareClauses([]string{boilerplate}, cfg)
	if len(eligible) != 0 {
		t.Fatalf("expected the boilerplate line to be skipped, got %d eligible", len(eligible))
	}
	if len(skipped) != 1 || skipped[0].Reason != SkipNoContractSignal {
		t.Fatalf("expected no_contract_signal, got %+v", skipped)
	}
}

func TestNormaliseClauseStripsNumbering(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"5. The Vendor shall indemnify the Client.", "The Vendor shall indemnify the Client."},
		{"3.1.2 The Vendor shall indemnify the Client.", "The Vendor shall indemnify the Client."},
		{"Section 12 Termination.", "Termination."},
		{"(a) The Vendor shall indemnify the Client.", "The Vendor shall indemnify the Client."},
		{"(iv) The Vendor shall indemnify the Client.", "The Vendor shall indemnify the Client."},
		{"vii. The Vendor shall indemnify the Client.", "The Vendor shall indemnify the Client."},
		{"ARTICLE 4 Liability shall be capped.", "Liability shall be capped."},
	}
	for _, c := range cases {
		if got := normaliseClause(c.in); got != c.want {
			t.Errorf("normaliseClause(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormaliseClauseKeepsRealContentThatLooksNumbered(t *testing.T) {
	// A sentence that legitimately begins with a small word after a period
	// must not have its first word eaten.
	in := "The Vendor shall pay. Fees are due within thirty days."
	got := normaliseClause(in)
	if !strings.HasPrefix(got, "The Vendor shall pay.") {
		t.Fatalf("normaliseClause mangled the sentence: %q", got)
	}
}

func TestNormaliseClauseCollapsesWhitespace(t *testing.T) {
	got := normaliseClause("1.  The   Vendor\n\tshall  indemnify  the Client.  ")
	want := "The Vendor shall indemnify the Client."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWindowTextLeavesShortClausesIntact(t *testing.T) {
	cfg := defaultConfig()
	text := "The Vendor shall indemnify the Client against all claims without limitation."
	windows := windowText(text, cfg)
	if len(windows) != 1 || windows[0] != text {
		t.Fatalf("short clause was windowed: %+v", windows)
	}
}

func TestWindowTextCoversEveryCharacter(t *testing.T) {
	cfg := defaultConfig()
	// Longer than one window so splitting is forced.
	unit := "The Vendor shall indemnify the Client against all claims without limitation of liability. "
	text := strings.Repeat(unit, 40)
	if len(text) <= cfg.WindowChars {
		t.Fatalf("test clause is not long enough to force windows: %d chars", len(text))
	}

	windows := windowText(text, cfg)
	if len(windows) < 2 {
		t.Fatalf("expected multiple windows, got %d", len(windows))
	}
	// No window may exceed the configured budget, or the model would truncate.
	for i, w := range windows {
		if len(w) > cfg.WindowChars {
			t.Fatalf("window %d is %d chars, over the %d budget", i, len(w), cfg.WindowChars)
		}
	}
	// Windows must overlap, otherwise a sentence split across a boundary is
	// only ever seen in halves.
	joined := strings.Join(windows, " ")
	if !strings.Contains(joined, "limitation of liability") {
		t.Fatal("overlap lost the phrase that straddles a window boundary")
	}
}

func TestWindowTextHardSplitsAnOverlongSentence(t *testing.T) {
	cfg := defaultConfig()
	// One sentence far longer than a window: it must be split rather than
	// dropped, because silently losing text would understate the clause.
	text := strings.Repeat("a", cfg.WindowChars*3)
	windows := windowText(text, cfg)
	if len(windows) < 3 {
		t.Fatalf("expected the overlong sentence to be split into at least 3 windows, got %d", len(windows))
	}
	total := 0
	for _, w := range windows {
		total += len(w)
	}
	if total < len(text)-cfg.WindowChars {
		t.Fatalf("hard split lost too much text: %d of %d chars retained", total, len(text))
	}
}

func TestSplitSentencesKeepsTerminalPunctuation(t *testing.T) {
	got := splitSentences("First one. Second one! Third? Fourth")
	want := []string{"First one.", "Second one!", "Third?", "Fourth"}
	if len(got) != len(want) {
		t.Fatalf("got %d sentences %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sentence %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestHasDocumentSignal(t *testing.T) {
	if !HasDocumentSignal([]string{"The party shall indemnify the client and liability is unlimited."}) {
		t.Fatal("a genuine contract clause should pass the document guard")
	}
	if HasDocumentSignal([]string{"Please summarise this paragraph for me, it is quite long and readable."}) {
		t.Fatal("prose that is not contractual should fail the document guard")
	}
}

func TestPrepareClausesReportsWindowCounts(t *testing.T) {
	cfg := defaultConfig()
	long := "The Vendor shall indemnify the Client against all claims without limitation. " +
		strings.Repeat("The Client shall pay all undisputed invoices within thirty days. ", 60)
	eligible, skipped := PrepareClauses([]string{long}, cfg)
	if len(eligible) != 1 {
		t.Fatalf("expected 1 eligible clause, got %d (skipped %+v)", len(eligible), skipped)
	}
	if eligible[0].Windows < 2 {
		t.Fatalf("expected the long clause to report multiple windows, got %d", eligible[0].Windows)
	}
	if eligible[0].Original != strings.TrimSpace(long) {
		t.Fatal("the original clause text must be preserved verbatim for display")
	}
	if strings.HasPrefix(eligible[0].Text, "The Vendor shall") == false {
		t.Fatal("analysis text should retain the clause content")
	}
}

func TestOverlapTailSnapsToWordBoundary(t *testing.T) {
	previous := "termination of this agreement by the company"
	tail := overlapTail(previous, 12)
	if strings.HasPrefix(tail, "y ") || strings.HasPrefix(tail, "ry ") {
		t.Fatalf("overlap began mid-word: %q", tail)
	}
	if !strings.Contains(previous, tail) {
		t.Fatalf("overlap %q is not a substring of %q", tail, previous)
	}
	if overlapTail(previous, 0) != "" {
		t.Fatal("a zero overlap must produce no carry")
	}
}
