package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeEngine satisfies RiskEngine without touching the network or a model, so
// the HTTP surface and the whole pipeline can be tested in milliseconds.
type fakeEngine struct {
	name     string
	model    string
	calls    atomic.Int64
	states   atomic.Int64
	readyErr error

	// failAfter makes every call beyond the first N fail, which is how the
	// partial-degradation path is exercised.
	failAfter int
	// noCategories makes the engine answer without categories, as Jev does.
	noCategories bool
}

func (f *fakeEngine) Name() string  { return f.name }
func (f *fakeEngine) Model() string { return f.model }

func (f *fakeEngine) Ready(context.Context) error { return f.readyErr }

func (f *fakeEngine) Analyze(_ context.Context, states []string) ([]ClauseOutcome, error) {
	n := int(f.calls.Add(1))
	f.states.Add(int64(len(states)))
	if f.failAfter > 0 && n > f.failAfter {
		return nil, engineError(ErrEngineUnavailable, "injected failure")
	}
	out := make([]ClauseOutcome, 0, len(states))
	for range states {
		if f.noCategories {
			out = append(out, ClauseOutcome{Score: 0.6, Confidence: 0.8, Label: "high"})
			continue
		}
		out = append(out, ClauseOutcome{Categories: readings(map[string]float64{
			CatUncappedLiability:     0.9,
			CatLimitationOfLiability: 0.02,
		})})
	}
	return out, nil
}

func newTestServer(t *testing.T, engine RiskEngine, mutate func(*server)) *server {
	t.Helper()
	cfg := defaultConfig()
	cfg.LayaBatchClauses = 2 // small, so chunking and degradation are exercised
	s := &server{
		cfg:       cfg,
		engine:    engine,
		limit:     newRateLimiter(),
		daily:     newDailyCap(1000),
		startedAt: time.Now(),
	}
	if mutate != nil {
		mutate(s)
	}
	return s
}

func do(s *server, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/analyze", strings.NewReader(body))
	req.RemoteAddr = "198.51.100.1:40000"
	rec := httptest.NewRecorder()
	s.analyze(rec, req)
	return rec
}

func decodeResponse(t *testing.T, rec *httptest.ResponseRecorder) AnalyzeResponse {
	t.Helper()
	var response AnalyzeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v\nbody: %s", err, rec.Body.String())
	}
	return response
}

const validClause = "The Vendor shall indemnify the Client against all claims without limitation of liability."

// -------------------------------------------------------------- happy path

func TestAnalyzeHappyPath(t *testing.T) {
	s := newTestServer(t, &fakeEngine{name: "laya", model: "english"}, nil)

	rec := do(s, `{"clauses":["`+validClause+`","`+validClause+`"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	response := decodeResponse(t, rec)
	if response.SchemaVersion != SchemaVersion {
		t.Fatalf("schema_version = %d", response.SchemaVersion)
	}
	if len(response.Results) != 2 {
		t.Fatalf("got %d results for 2 clauses", len(response.Results))
	}
	for i, result := range response.Results {
		if result.ClauseIndex != i {
			t.Errorf("result %d has clause_index %d", i, result.ClauseIndex)
		}
		if result.RiskLevel == "" || result.Explanation == "" {
			t.Errorf("result %d is missing a level or explanation: %+v", i, result)
		}
		if len(result.Issues) == 0 {
			t.Errorf("result %d has no issues", i)
		}
		if result.Explanation == "" && result.Reason == "" {
			t.Errorf("result %d explains nothing", i)
		}
	}

	meta := response.Meta
	if meta.Engine != "laya" || meta.Model != "english" {
		t.Errorf("meta identifies %s/%s", meta.Engine, meta.Model)
	}
	if meta.ClausesReceived != 2 || meta.ClausesAnalyzed != 2 || meta.ClausesSkipped != 0 {
		t.Errorf("meta counts are wrong: %+v", meta)
	}
	if meta.WindowsAnalyzed == 0 {
		t.Error("meta.windows_analyzed was not populated; it used to be hard-coded to 0")
	}
	if meta.Calibration != CalibrationProvisional {
		t.Errorf("meta.calibration = %q, want %q", meta.Calibration, CalibrationProvisional)
	}
	if meta.Partial {
		t.Error("a clean run should not be marked partial")
	}
	if meta.DurationMS <= 0 {
		t.Error("meta.duration_ms was not recorded")
	}
}

// -------------------------------------------------------------- validation

func TestAnalyzeRejectsMalformedAndHostileInput(t *testing.T) {
	// A fresh server per case: the rate limiter budget is 3 per minute per
	// address, so a shared one would 429 before the validation ever ran.
	cases := []struct {
		name string
		body string
		code string
	}{
		{"invalid json", `{`, "invalid_body"},
		{"no clauses", `{"clauses":[]}`, "no_clauses"},
		{"too many clauses", buildBody(make([]string, maxClausesForTest()+1)), "too_many_clauses"},
		{"not a contract", `{"clauses":["` + strings.Repeat("the quick brown fox jumps ", 5) + `"]}`, "not_a_contract"},
		// Passes the document-level gate (shall, party, agreement, obligation all
		// appear across the body) while every individual clause is too short to
		// analyse, which is the branch that reports nothing eligible.
		{"nothing eligible", `{"clauses":["shall party","agreement obligation clause"]}`, "no_eligible_clauses"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newTestServer(t, &fakeEngine{name: "laya"}, nil)
			rec := do(s, c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), c.code) {
				t.Fatalf("body %s does not carry the code %q", rec.Body.String(), c.code)
			}
		})
	}
}

func maxClausesForTest() int { return defaultConfig().MaxClauses }

func buildBody(clauses []string) string {
	parts := make([]string, len(clauses))
	for i, c := range clauses {
		parts[i] = fmt.Sprintf("%q", c)
	}
	return `{"clauses":[` + strings.Join(parts, ",") + `]}`
}

func TestAnalyzeRejectsAnOversizedBody(t *testing.T) {
	s := newTestServer(t, &fakeEngine{name: "laya"}, func(s *server) {
		s.cfg.MaxRequestBytes = 2048
	})
	rec := do(s, buildBody(make([]string, 400)))
	// Either the MaxBytesReader trips or the clause count is refused; both are
	// refusals, and neither may be a 200.
	if rec.Code == http.StatusOK {
		t.Fatal("an oversized request was accepted")
	}
}

// ------------------------------------------------------------------ engine

func TestAnalyzeMapsEngineFailures(t *testing.T) {
	cases := []struct {
		name   string
		engine *fakeEngine
		status int
		code   string
	}{
		{"unavailable", &fakeEngine{name: "laya", failAfter: 0, readyErr: nil}, http.StatusServiceUnavailable, "engine_unavailable"},
	}
	_ = cases

	t.Run("engine down", func(t *testing.T) {
		s := newTestServer(t, &fakeEngine{name: "laya"}, nil)
		s.engine = failingEngine{err: engineError(ErrEngineUnavailable, "down")}

		rec := do(s, `{"clauses":["`+validClause+`"]}`)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503; body = %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "engine_unavailable") {
			t.Fatalf("body %s lacks the code", rec.Body.String())
		}
	})

	t.Run("circuit open", func(t *testing.T) {
		s := newTestServer(t, &fakeEngine{name: "laya"}, nil)
		s.engine = failingEngine{err: ErrEngineOpen}

		rec := do(s, `{"clauses":["`+validClause+`"]}`)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "engine_circuit_open") {
			t.Fatalf("body %s lacks the code", rec.Body.String())
		}
	})

	t.Run("bad engine response", func(t *testing.T) {
		s := newTestServer(t, &fakeEngine{name: "laya"}, nil)
		s.engine = failingEngine{err: engineError(ErrEngineResponse, "malformed")}

		rec := do(s, `{"clauses":["`+validClause+`"]}`)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502; body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("no detail leaked to the caller", func(t *testing.T) {
		s := newTestServer(t, &fakeEngine{name: "laya"}, nil)
		s.engine = failingEngine{err: engineError(ErrEngineUnavailable, "dial tcp 10.0.0.7:8000: connection refused")}

		rec := do(s, `{"clauses":["`+validClause+`"]}`)
		body := rec.Body.String()
		if strings.Contains(body, "10.0.0.7") || strings.Contains(body, "dial tcp") {
			t.Fatalf("internal detail leaked to the client: %s", body)
		}
	})
}

type failingEngine struct{ err error }

func (f failingEngine) Name() string                { return "laya" }
func (f failingEngine) Model() string               { return "english" }
func (f failingEngine) Ready(context.Context) error { return f.err }
func (f failingEngine) Analyze(context.Context, []string) ([]ClauseOutcome, error) {
	return nil, f.err
}

// ---------------------------------------------------------- degradation

func TestAnalyzeDegradesWhenTheEngineFailsPartWay(t *testing.T) {
	// Batch size is 2 in tests, so four clauses means two chunks. The first must
	// succeed and the second fail.
	engine := &fakeEngine{name: "laya", failAfter: 1}
	s := newTestServer(t, engine, nil)

	rec := do(s, buildBody([]string{validClause, validClause, validClause, validClause}))
	if rec.Code != http.StatusOK {
		t.Fatalf("a partial success returned %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	response := decodeResponse(t, rec)
	if !response.Meta.Partial {
		t.Error("meta.partial was not set even though a chunk failed")
	}
	if len(response.Warnings) == 0 {
		t.Error("no warning was emitted for the missing chunk")
	}
	if response.Meta.ClausesAnalyzed != 2 {
		t.Errorf("clauses_analyzed = %d, want 2", response.Meta.ClausesAnalyzed)
	}
	// Every submitted clause keeps its slot so the client's numbering lines up.
	if len(response.Results) != 4 {
		t.Fatalf("got %d results for 4 clauses; slots must be preserved", len(response.Results))
	}
	for i, result := range response.Results {
		if result.NeedsReview && result.Reason == "not analysed" {
			if i < 2 {
				t.Errorf("result %d should have been analysed", i)
			}
			continue
		}
		if i >= 2 {
			t.Errorf("result %d should be marked as not analysed", i)
		}
	}
}

// ---------------------------------------------------------------- lifecycle

func TestHealthAndReady(t *testing.T) {
	engine := &fakeEngine{name: "laya", model: "english"}
	s := newTestServer(t, engine, nil)

	rec := httptest.NewRecorder()
	s.health(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/health status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"version":2`) {
		t.Errorf("/health does not report the schema version: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.ready(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ready status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), CalibrationProvisional) {
		t.Errorf("/ready does not surface the calibration status: %s", rec.Body.String())
	}

	s.engine = failingEngine{err: ErrEngineUnavailable}
	rec = httptest.NewRecorder()
	s.ready(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/ready status = %d when the engine is down, want 503", rec.Code)
	}
}

// ---------------------------------------------------------------- pipeline

// TestRunPipelineRedactsIdentifiersBeforeTheEngineSeesText documents exactly
// what redaction does and, just as importantly, what it does not do.
//
// wuming detects identifiers: national ids, emails, phone numbers, IBANs, card
// numbers, IPs, MACs, URLs. It has no person-name or organisation-name detector,
// so a natural person's name and a company name both pass through intact.
//
// That is not treated as a bug to paper over here. In a contract the party names
// are functionally necessary: masking "Customer" and "Vendor" would destroy the
// very asymmetry that one_sided_indemnity detects. So the privacy posture for
// contracts cannot be "scrub everything"; it has to be "your infrastructure holds
// the text", which is the property running Laya locally provides and sending
// clause text to a gateway would not.
func TestRunPipelineRedactsIdentifiersBeforeTheEngineSeesText(t *testing.T) {
	recorder := &recordingEngine{}
	s := newTestServer(t, recorder, nil)

	clause := "John Smith (SSN 123-45-6789, john@acme.com) shall indemnify Acme Corp against all claims without limitation."
	eligible, _ := PrepareClauses([]string{clause}, s.cfg)
	if len(eligible) != 1 {
		t.Fatalf("clause was not eligible: %+v", eligible)
	}

	if _, _, _, err := s.runPipeline(context.Background(), eligible); err != nil {
		t.Fatal(err)
	}

	sent := recorder.seen()
	if len(sent) != 1 {
		t.Fatalf("engine received %d windows, want 1", len(sent))
	}
	// Structured identifiers must be gone.
	for _, leak := range []string{"123-45-6789", "john@acme.com"} {
		if strings.Contains(sent[0], leak) {
			t.Fatalf("identifier %q reached the inference service: %q", leak, sent[0])
		}
	}
	// And the replacement must be the library's marker, not a silent drop.
	if !strings.Contains(sent[0], "[NATIONAL_ID]") || !strings.Contains(sent[0], "[EMAIL]") {
		t.Fatalf("expected the library's redaction markers, got: %q", sent[0])
	}
	// Names survive. Asserted so the limitation cannot regress unnoticed.
	for _, survives := range []string{"John Smith", "Acme Corp"} {
		if !strings.Contains(sent[0], survives) {
			t.Fatalf("expected %q to survive redaction (documented limitation); got %q. "+
				"If a name detector was added, update the privacy documentation too.", survives, sent[0])
		}
	}
	// Analysis content must be intact.
	if !strings.Contains(sent[0], "indemnify") {
		t.Fatalf("redaction destroyed the clause content: %q", sent[0])
	}
}

type recordingEngine struct {
	mu      sync.Mutex
	windows []string
}

func (r *recordingEngine) Name() string                { return "laya" }
func (r *recordingEngine) Model() string               { return "english" }
func (r *recordingEngine) Ready(context.Context) error { return nil }
func (r *recordingEngine) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.windows))
	copy(out, r.windows)
	return out
}

func (r *recordingEngine) Analyze(_ context.Context, states []string) ([]ClauseOutcome, error) {
	r.mu.Lock()
	r.windows = append(r.windows, states...)
	r.mu.Unlock()
	out := make([]ClauseOutcome, 0, len(states))
	for range states {
		out = append(out, ClauseOutcome{Categories: readings(nil)})
	}
	return out, nil
}

func TestRunPipelineWindowsAnOverlongClause(t *testing.T) {
	recorder := &recordingEngine{}
	cfg := defaultConfig()
	cfg.WindowChars = 300
	s := newTestServer(t, recorder, func(s *server) { s.cfg = cfg })

	long := strings.TrimSpace(validClause + " " + strings.Repeat("The Client shall pay all undisputed invoices. ", 40))
	eligible, _ := PrepareClauses([]string{long}, cfg)
	if len(eligible) != 1 {
		t.Fatalf("clause not eligible")
	}
	if eligible[0].Windows < 2 {
		t.Fatalf("expected the long clause to be windowed, got %d windows", eligible[0].Windows)
	}

	_, _, windows, err := s.runPipeline(context.Background(), eligible)
	if err != nil {
		t.Fatal(err)
	}
	if windows != eligible[0].Windows {
		t.Fatalf("windows_analyzed = %d, want %d", windows, eligible[0].Windows)
	}
	if len(recorder.seen()) < 2 {
		t.Fatalf("engine received %d windows, want at least 2", len(recorder.seen()))
	}
}

func TestSkippedClausesAreReportedNotDropped(t *testing.T) {
	s := newTestServer(t, &fakeEngine{name: "laya"}, nil)
	rec := do(s, buildBody([]string{validClause, "Notices.", validClause}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", rec.Code, rec.Body.String())
	}
	response := decodeResponse(t, rec)
	if len(response.Skipped) != 1 {
		t.Fatalf("got %d skipped clauses, want 1", len(response.Skipped))
	}
	if response.Skipped[0].ClauseIndex != 1 {
		t.Errorf("skipped clause_index = %d, want 1", response.Skipped[0].ClauseIndex)
	}
	if response.Skipped[0].Reason == "" {
		t.Error("skipped clause has no reason")
	}
	if response.Meta.ClausesSkipped != 1 {
		t.Errorf("meta.clauses_skipped = %d", response.Meta.ClausesSkipped)
	}
}

func TestRateLimitAppliesPerAddress(t *testing.T) {
	s := newTestServer(t, &fakeEngine{name: "laya"}, nil)
	body := `{"clauses":["` + validClause + `"]}`

	// The budget is 3 per minute.
	for i := 0; i < rateLimit; i++ {
		req := httptest.NewRequest(http.MethodPost, "/analyze", strings.NewReader(body))
		req.RemoteAddr = "198.51.100.9:40000"
		s.analyze(httptest.NewRecorder(), req)
	}
	req := httptest.NewRequest(http.MethodPost, "/analyze", strings.NewReader(body))
	req.RemoteAddr = "198.51.100.9:40000"
	rec := httptest.NewRecorder()
	s.analyze(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 once the budget is spent", rec.Code)
	}

	// A different address has its own budget.
	req = httptest.NewRequest(http.MethodPost, "/analyze", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.4:40000"
	rec = httptest.NewRecorder()
	s.analyze(rec, req)
	if rec.Code == http.StatusTooManyRequests {
		t.Fatal("an unrelated address was rate limited")
	}
}

func TestEngineWithoutCategoriesIsHandled(t *testing.T) {
	// The Jev engine answers with a single graded score and no categories. The
	// pipeline must produce an explicitly thinner result that says so, rather
	// than failing the request or inventing findings.
	s := newTestServer(t, &fakeEngine{name: "jev", model: "typesafe-ai/jev", noCategories: true}, nil)
	rec := do(s, `{"clauses":["`+validClause+`"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", rec.Code, rec.Body.String())
	}
	response := decodeResponse(t, rec)
	if len(response.Results) != 1 {
		t.Fatalf("got %d results", len(response.Results))
	}
	result := response.Results[0]
	if len(result.Issues) > 0 {
		t.Errorf("issues were invented for an engine that returned none: %+v", result.Issues)
	}
	if !result.NeedsReview {
		t.Error("a result with no per-category findings must be marked needs_review")
	}
	if !strings.Contains(result.Explanation, "single graded score") {
		t.Errorf("the explanation does not disclose the thinner result: %q", result.Explanation)
	}
	if result.RiskLevel == "" {
		t.Error("the engine's own label was dropped")
	}
}

func TestEngineErrorsAreNotSilent(t *testing.T) {
	// Guard the sentinel wiring: a wrapped engine error must still be
	// recognisable with errors.Is after passing through the pipeline.
	wrapped := engineError(ErrEngineUnavailable, "detail")
	if !errors.Is(wrapped, ErrEngineUnavailable) {
		t.Fatal("engineError must wrap its sentinel so errors.Is works")
	}
	if errors.Is(wrapped, ErrEngineResponse) {
		t.Fatal("engineError must not match an unrelated sentinel")
	}
}
