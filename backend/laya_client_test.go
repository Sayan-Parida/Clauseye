package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeLaya is a stand-in for the inference service. It records what it was sent
// and can be told to misbehave in each of the ways the client must handle.
type fakeLaya struct {
	server *httptest.Server

	calls        atomic.Int64
	lastStates   []string
	status       atomic.Int64
	bodyOverride atomic.Pointer[string]
	delay        atomic.Int64 // milliseconds
	readyStatus  atomic.Int64
}

func newFakeLaya(t *testing.T) *fakeLaya {
	t.Helper()
	f := &fakeLaya{}
	f.status.Store(http.StatusOK)
	f.readyStatus.Store(http.StatusOK)

	mux := http.NewServeMux()
	mux.HandleFunc("/analyze-batch", func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if d := f.delay.Load(); d > 0 {
			time.Sleep(time.Duration(d) * time.Millisecond)
		}
		if status := f.status.Load(); status != http.StatusOK {
			w.WriteHeader(int(status))
			_, _ = w.Write([]byte(`{"error":"injected"}`))
			return
		}
		var request layaBatchRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.lastStates = request.States

		if override := f.bodyOverride.Load(); override != nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(*override))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": goodResults(len(request.States))})
	})
	mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(f.readyStatus.Load()))
		_, _ = w.Write([]byte(`{"status":"ready","model":"english","loaded":true}`))
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func goodResults(n int) []map[string]any {
	out := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		answers := map[string]any{}
		for _, id := range categoryOrder {
			answers[id] = map[string]any{"type": "noul", "noul": 0.05, "confidence": 0.95}
		}
		answers[CatUncappedLiability] = map[string]any{"noul": 0.90, "confidence": 0.90}
		out = append(out, map[string]any{"answers": answers, "usage": map[string]any{"input_tokens": 10}})
	}
	return out
}

func (f *fakeLaya) client(t *testing.T) *LayaClient {
	t.Helper()
	cfg := defaultConfig()
	cfg.LayaURL = f.server.URL + "/analyze-batch"
	cfg.LayaAPIKey = "test-key"
	cfg.LayaAttempts = 2
	cfg.LayaTimeout = 5 * time.Second
	cfg.BreakerThreshold = 3
	cfg.BreakerCooldown = 50 * time.Millisecond
	c := NewLayaClient(cfg)
	return c
}

func TestLayaClientHappyPath(t *testing.T) {
	f := newFakeLaya(t)
	c := f.client(t)

	states := []string{"The Vendor shall indemnify the Client without limitation.", "This clause is long enough to be considered here."}
	outcomes, err := c.Analyze(context.Background(), states)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != len(states) {
		t.Fatalf("got %d outcomes for %d states", len(outcomes), len(states))
	}
	for i, outcome := range outcomes {
		if !outcome.HasCategories() {
			t.Fatalf("outcome %d has no categories", i)
		}
		if got := outcome.Categories[CatUncappedLiability].Probability; got != 0.90 {
			t.Fatalf("uncapped_liability = %.2f, want 0.90", got)
		}
	}
	// One request for the whole batch: batching is the throughput mechanism.
	if calls := f.calls.Load(); calls != 1 {
		t.Fatalf("made %d requests for a single batch, want 1", calls)
	}
}

// TestLayaClientSendsBearerToken asserts the credential actually reaches the
// inference service, since an unauthenticated internal service is one
// misconfiguration away from being reachable by anything that can route to it.
func TestLayaClientSendsBearerToken(t *testing.T) {
	var seen atomic.Pointer[string]
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		seen.Store(&auth)
		var request layaBatchRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		_ = json.NewEncoder(w).Encode(map[string]any{"results": goodResults(len(request.States))})
	}))
	defer service.Close()

	cfg := defaultConfig()
	cfg.LayaURL = service.URL + "/analyze-batch"
	cfg.LayaAPIKey = "secret-token"
	cfg.LayaAttempts = 1
	cfg.LayaTimeout = 5 * time.Second

	if _, err := NewLayaClient(cfg).Analyze(context.Background(),
		[]string{"A clause long enough to be analysed here."}); err != nil {
		t.Fatal(err)
	}
	got := ""
	if p := seen.Load(); p != nil {
		got = *p
	}
	if got != "Bearer secret-token" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer secret-token")
	}
}

// TestLayaClientOmitsAuthorizationWhenNoKeyIsConfigured covers local development
// with LAYA_API_KEY unset, which is permitted but warned about at start-up.
func TestLayaClientOmitsAuthorizationWhenNoKeyIsConfigured(t *testing.T) {
	var seen atomic.Pointer[string]
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		seen.Store(&auth)
		var request layaBatchRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		_ = json.NewEncoder(w).Encode(map[string]any{"results": goodResults(len(request.States))})
	}))
	defer service.Close()

	cfg := defaultConfig()
	cfg.LayaURL = service.URL + "/analyze-batch"
	cfg.LayaAPIKey = ""
	cfg.LayaAttempts = 1
	cfg.LayaTimeout = 5 * time.Second

	if _, err := NewLayaClient(cfg).Analyze(context.Background(),
		[]string{"A clause long enough to be analysed here."}); err != nil {
		t.Fatal(err)
	}
	got := ""
	if p := seen.Load(); p != nil {
		got = *p
	}
	if got != "" {
		t.Fatalf("Authorization = %q, want it absent", got)
	}
}

func TestLayaClientRetriesTransientFailures(t *testing.T) {
	f := newFakeLaya(t)
	c := f.client(t)

	f.status.Store(http.StatusInternalServerError)
	go func() {
		time.Sleep(100 * time.Millisecond)
		f.status.Store(http.StatusOK)
	}()

	outcomes, err := c.Analyze(context.Background(), []string{"A clause that is long enough to be analysed here."})
	if err != nil {
		t.Fatalf("expected the retry to recover, got %v", err)
	}
	if len(outcomes) != 1 {
		t.Fatalf("got %d outcomes, want 1", len(outcomes))
	}
	if calls := f.calls.Load(); calls < 2 {
		t.Fatalf("made %d calls, expected a retry", calls)
	}
}

func TestLayaClientDoesNotRetryMalformedResponses(t *testing.T) {
	f := newFakeLaya(t)
	c := f.client(t)

	f.bodyOverride.Store(ptr(`{"results":[{"answers":{"auto_renewal":{"noul":0.9,"confidence":0.9}}}]}`))

	_, err := c.Analyze(context.Background(), []string{"A clause that is long enough to be analysed here."})
	if !errors.Is(err, ErrEngineResponse) {
		t.Fatalf("error = %v, want ErrEngineResponse", err)
	}
	// Retrying cannot make a malformed response well formed, so exactly one
	// attempt should have been made.
	if calls := f.calls.Load(); calls != 1 {
		t.Fatalf("made %d calls for a malformed response, want 1", calls)
	}
}

func TestLayaClientRejectsMissingCategory(t *testing.T) {
	f := newFakeLaya(t)
	c := f.client(t)
	f.bodyOverride.Store(ptr(`{"results":[{"answers":{}}]}`))

	_, err := c.Analyze(context.Background(), []string{"A clause that is long enough to be analysed here."})
	if !errors.Is(err, ErrEngineResponse) {
		t.Fatalf("error = %v, want ErrEngineResponse", err)
	}
}

func TestLayaClientRejectsNonFiniteAndOutOfRange(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"negative", answersJSON(t, CatAutoRenewal, -0.5, 0.9, 0.02)},
		{"above one", answersJSON(t, CatAutoRenewal, 1.5, 0.9, 0.02)},
		{"confidence above one", answersJSON(t, CatAutoRenewal, 0.9, 4.0, 0.02)},
		{"confidence negative", answersJSON(t, CatAutoRenewal, 0.9, -1.0, 0.02)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeLaya(t)
			client := f.client(t)
			f.bodyOverride.Store(ptr(c.body))

			if _, err := client.Analyze(context.Background(),
				[]string{"A clause that is long enough to be analysed here."}); !errors.Is(err, ErrEngineResponse) {
				t.Fatalf("error = %v, want ErrEngineResponse", err)
			}
		})
	}
}

func TestLayaClientRejectsResultCountMismatch(t *testing.T) {
	f := newFakeLaya(t)
	c := f.client(t)
	f.bodyOverride.Store(ptr(`{"results":[]}`))

	_, err := c.Analyze(context.Background(),
		[]string{"First clause long enough.", "Second clause long enough."})
	if !errors.Is(err, ErrEngineResponse) {
		t.Fatalf("error = %v, want ErrEngineResponse", err)
	}
}

func TestLayaClientMapsHTTPStatuses(t *testing.T) {
	cases := []struct {
		status  int64
		wantErr error
	}{
		{http.StatusUnauthorized, ErrEngineUnavailable},
		{http.StatusRequestEntityTooLarge, ErrEngineResponse},
		{http.StatusUnprocessableEntity, ErrEngineResponse},
		{http.StatusServiceUnavailable, ErrEngineUnavailable},
		{http.StatusBadGateway, ErrEngineUnavailable},
	}
	for _, c := range cases {
		f := newFakeLaya(t)
		client := f.client(t)
		f.status.Store(c.status)

		_, err := client.Analyze(context.Background(), []string{"A clause long enough to analyse."})
		if !errors.Is(err, c.wantErr) {
			t.Fatalf("HTTP %d produced %v, want %v", c.status, err, c.wantErr)
		}
	}
}

func TestLayaClientHandlesUnreachableService(t *testing.T) {
	cfg := defaultConfig()
	cfg.LayaURL = "http://127.0.0.1:1/analyze-batch"
	cfg.LayaAttempts = 1
	cfg.BreakerThreshold = 1
	c := NewLayaClient(cfg)

	_, err := c.Analyze(context.Background(), []string{"A clause long enough to analyse."})
	if !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("error = %v, want ErrEngineUnavailable", err)
	}
}

func TestLayaClientTimesOut(t *testing.T) {
	f := newFakeLaya(t)
	f.delay.Store(300)

	cfg := defaultConfig()
	cfg.LayaURL = f.server.URL + "/analyze-batch"
	cfg.LayaAttempts = 1
	cfg.LayaTimeout = 50 * time.Millisecond
	client := NewLayaClient(cfg)

	_, err := client.Analyze(context.Background(), []string{"A clause long enough to analyse."})
	if !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("error = %v, want ErrEngineUnavailable", err)
	}
}

// ------------------------------------------------------------------- breaker

func TestBreakerOpensAfterRepeatedFailures(t *testing.T) {
	b := newBreaker(3, time.Minute)
	now := time.Now()
	b.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if err := b.allow(); err != nil {
			t.Fatalf("attempt %d was blocked before the threshold: %v", i, err)
		}
		b.recordFailure()
	}

	if err := b.allow(); !errors.Is(err, ErrEngineOpen) {
		t.Fatalf("breaker did not open after %d failures: %v", 3, err)
	}
}

func TestBreakerAdmitsOneProbeAfterCooldown(t *testing.T) {
	b := newBreaker(1, 10*time.Millisecond)
	now := time.Now()
	b.now = func() time.Time { return now }

	b.allow()
	b.recordFailure()
	if err := b.allow(); !errors.Is(err, ErrEngineOpen) {
		t.Fatal("breaker should be open during the cooldown")
	}

	now = now.Add(20 * time.Millisecond)
	if err := b.allow(); err != nil {
		t.Fatalf("the probe after the cooldown was blocked: %v", err)
	}
	// Exactly one probe: a recovering service must not be stampeded.
	if err := b.allow(); !errors.Is(err, ErrEngineOpen) {
		t.Fatal("a second concurrent probe was admitted")
	}

	b.recordSuccess()
	if err := b.allow(); err != nil {
		t.Fatalf("the breaker stayed open after a success: %v", err)
	}
}

func TestBreakerFailsFastWithoutCallingUpstream(t *testing.T) {
	f := newFakeLaya(t)
	c := f.client(t)
	f.status.Store(http.StatusServiceUnavailable)

	// Exhaust the attempts and the breaker threshold.
	for i := 0; i < 3; i++ {
		_, _ = c.Analyze(context.Background(), []string{"A clause long enough to analyse."})
	}
	callsAfterFailures := f.calls.Load()

	_, err := c.Analyze(context.Background(), []string{"A clause long enough to analyse."})
	if !errors.Is(err, ErrEngineOpen) && !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("error = %v, want the breaker to be open", err)
	}
	if f.calls.Load() != callsAfterFailures {
		t.Fatal("an open breaker still called the inference service")
	}
}

func TestBreakerNeverRunsOutOfMemory(t *testing.T) {
	// newBreaker clamps nonsense input rather than trusting it.
	b := newBreaker(0, 0)
	if b.threshold < 1 {
		t.Fatal("threshold must be at least 1")
	}
	if b.cooldown <= 0 {
		t.Fatal("cooldown must be positive")
	}
}

// ----------------------------------------------------------------- readiness

func TestLayaClientReadyRequiresLoadedCheckpoint(t *testing.T) {
	f := newFakeLaya(t)
	c := f.client(t)

	if err := c.Ready(context.Background()); err != nil {
		t.Fatalf("a ready service reported %v", err)
	}
	if c.Model() != "english" {
		t.Fatalf("model = %q, want the name reported by the service", c.Model())
	}

	f.readyStatus.Store(http.StatusServiceUnavailable)
	if err := c.Ready(context.Background()); !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("a loading service reported %v, want ErrEngineUnavailable", err)
	}
}

func TestReadyURLDerivation(t *testing.T) {
	cases := map[string]string{
		"http://laya:8000/analyze-batch":      "http://laya:8000/ready",
		"http://127.0.0.1:8000/analyze-batch": "http://127.0.0.1:8000/ready",
		"http://laya:8000/custom":             "http://laya:8000/custom/ready",
	}
	for in, want := range cases {
		if got := readyURL(in); got != want {
			t.Errorf("readyURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// ------------------------------------------------------------------ helpers

func ptr(s string) *string { return &s }

func answersJSON(t *testing.T, category string, noul, confidence, rest float64) string {
	t.Helper()
	answers := map[string]any{}
	for _, id := range categoryOrder {
		answers[id] = map[string]any{"noul": rest, "confidence": 1 - rest}
	}
	answers[category] = map[string]any{"noul": noul, "confidence": confidence}
	body, err := json.Marshal(map[string]any{"results": []any{map[string]any{"answers": answers}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// TestSchemaV2Contract pins the wire contract the frontend depends on.
func TestSchemaV2Contract(t *testing.T) {
	if SchemaVersion != 2 {
		t.Fatalf("SchemaVersion = %d, want 2", SchemaVersion)
	}

	cfg := defaultConfig()
	agg, err := Aggregate(readings(map[string]float64{
		CatOneSidedIndemnity: 0.94,
		CatUncappedLiability: 0.88,
	}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	explanation, reason := composeNarrative(agg, cfg)

	result := AnalysisResult{
		Clause:      "The Customer shall indemnify the Supplier at its sole expense.",
		ClauseIndex: 4,
		RiskLevel:   agg.Level,
		RiskScore:   agg.Score,
		Confidence:  agg.Confidence,
		NeedsReview: agg.NeedsReview,
		Explanation: explanation,
		Reason:      reason,
		Issues:      buildIssues(agg, "The Customer shall indemnify the Supplier at its sole expense.", cfg),
		Location:    &ClauseLocation{ClauseIndex: 4},
	}
	response := AnalyzeResponse{
		SchemaVersion: SchemaVersion,
		Results:       []AnalysisResult{result},
		Skipped:       []SkippedClause{{ClauseIndex: 9, Reason: SkipTooShort}},
		Meta:          ResponseMeta{Engine: "laya", Calibration: CalibrationProvisional},
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}

	// Schema v1 fields must still be present so an older client keeps working.
	first, _ := decoded["results"].([]any)
	entry, _ := first[0].(map[string]any)
	for _, field := range []string{"clause", "risk_level", "confidence", "reason"} {
		if _, ok := entry[field]; !ok {
			t.Errorf("schema v1 field %q is missing", field)
		}
	}
	for _, field := range []string{"clause_index", "risk_score", "needs_review", "explanation", "issues", "location"} {
		if _, ok := entry[field]; !ok {
			t.Errorf("schema v2 field %q is missing", field)
		}
	}

	issues, _ := entry["issues"].([]any)
	if len(issues) == 0 {
		t.Fatal("no issues were emitted")
	}
	issue, _ := issues[0].(map[string]any)
	for _, field := range []string{"category", "label", "polarity", "reliability", "state", "detected",
		"probability", "confidence", "weight", "group_risk", "contributes", "explanation", "evidence_found"} {
		if _, ok := issue[field]; !ok {
			t.Errorf("issue field %q is missing", field)
		}
	}

	if decoded["schema_version"] != float64(2) {
		t.Errorf("schema_version = %v, want 2", decoded["schema_version"])
	}
	meta, _ := decoded["meta"].(map[string]any)
	if meta["calibration"] != CalibrationProvisional {
		t.Errorf("meta.calibration = %v, want %q so a consumer cannot mistake these weights for validated",
			meta["calibration"], CalibrationProvisional)
	}
}

func TestRiskScoreIsNotRenderedAsAPercentage(t *testing.T) {
	// Guards the trap that produced "94% risky" for a category the system
	// considered clear: the score is an aggregation output, not a probability.
	cfg := defaultConfig()
	agg, err := Aggregate(readings(map[string]float64{CatAutoRenewal: 0.90}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if agg.Score <= 0 || agg.Score >= 1 {
		t.Fatalf("score = %.4f, want a mid-range aggregation output", agg.Score)
	}
	issues := buildIssues(agg, "This agreement shall automatically renew each year.", cfg)
	if len(issues) == 0 || issues[0].Probability < 0.6 {
		t.Fatalf("probability was not reported: %+v", issues)
	}
}

func TestExplanationNeverMentionsAnInternalScoreEcho(t *testing.T) {
	cfg := defaultConfig()
	for _, ids := range []map[string]float64{
		nil,
		{CatAutoRenewal: 0.9},
		{CatUncappedLiability: 0.9, CatOneSidedIndemnity: 0.8, CatAutoRenewal: 0.7},
	} {
		agg, err := Aggregate(readings(ids), cfg)
		if err != nil {
			t.Fatal(err)
		}
		explanation, reason := composeNarrative(agg, cfg)

		// The old engine emitted literally "Jev score: 2.31". Neither field may
		// regress to echoing a score with no explanation attached.
		if strings.Contains(explanation, "Jev score") || strings.Contains(reason, "Jev score") {
			t.Fatalf("output regressed to a bare score echo: %q / %q", explanation, reason)
		}
		// The narrative must actually say something about the findings.
		if len(ids) > 0 && !strings.Contains(explanation, "this score") {
			t.Fatalf("narrative does not explain the score: %q", explanation)
		}
		if len(ids) > 0 && !strings.Contains(reason, "p=") {
			t.Fatalf("reason does not carry per-category probabilities: %q", reason)
		}
		// Every contributor must be named in the narrative.
		for id := range ids {
			if !strings.Contains(reason, id) {
				t.Fatalf("reason %q omits contributor %s", reason, id)
			}
		}
	}
}

func TestMergeReadingsIgnoresWindowsWithoutCategories(t *testing.T) {
	// A Jev-style outcome has nil categories and must not poison a merged set.
	outcomes := []ClauseOutcome{
		{Categories: readings(map[string]float64{CatUncappedLiability: 0.9})},
		{Categories: nil},
	}
	var sets []Readings
	for _, o := range outcomes {
		if o.HasCategories() {
			sets = append(sets, o.Categories)
		}
	}
	merged := mergeReadings(sets)
	if merged[CatUncappedLiability].Probability != 0.9 {
		t.Fatalf("uncapped_liability = %.2f, want 0.9", merged[CatUncappedLiability].Probability)
	}
	if math.Abs(merged[CatAutoRenewal].Probability) > 1e-9 {
		t.Fatal("a category absent from every window should stay at its fill value")
	}
}
