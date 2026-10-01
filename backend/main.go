package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/taoq-ai/wuming"
)

// This file is wiring and transport. The substance lives elsewhere:
//
//	config.go       configuration, and the PROVISIONAL weight/threshold values
//	taxonomy.go     the nine risk categories, their rationale and evidence rules
//	aggregate.go    hysteresis, guarded groups, bounded combination
//	explain.go      deterministic explanation and evidence composition
//	chunking.go     clause eligibility and sentence windowing
//	engine.go       the engine interface both backends satisfy
//	laya_client.go  the self-hosted inference client, with strict validation
//	jev_client.go   the development-only Vercel AI Gateway engine
//
// Two rules this file enforces because they are easy to get wrong:
//
//   - Clause text is never logged. Not the clause, not the PII-redacted clause,
//     not the upstream response body. Only counts, statuses and sizes. PII
//     redaction covers personal identifiers, not commercial sensitivity, so a
//     "redacted" clause is still confidential material.
//
//   - PII redaction happens here, immediately before the only network call.
//     Nothing unredacted crosses a process boundary.

const (
	// maxResponseBytes caps how much of an upstream response is read. The body
	// is never logged, but it is unmarshalled, so it still needs a bound.
	maxResponseBytes = 1 << 20

	// rateWindow and rateLimit are the per-client request budget. Windows are
	// evicted after rateWindow so the tracker map cannot grow without bound when
	// many distinct clients appear.
	rateWindow = time.Minute
	rateLimit  = 3

	// dailyCapMax is the process-wide daily analysis budget.
	dailyCapMax = 500

	// maxTrackedClients bounds the rate limiter map even before eviction runs.
	maxTrackedClients = 65536

	// upstreamTimeout bounds a single upstream evaluation call.
	upstreamTimeout = 10 * time.Second
)

type analyzeRequest struct {
	Clauses []string `json:"clauses"`
}

type server struct {
	cfg            Config
	engine         RiskEngine
	client         *http.Client
	limit          *rateLimiter
	daily          *dailyCap
	trustedProxies []*net.IPNet
	corsOrigin     string
	startedAt      time.Time
}

type clientWindow struct {
	started time.Time
	count   int
}

type rateLimiter struct {
	mu      sync.Mutex
	clients map[string]clientWindow
}

func newRateLimiter() *rateLimiter { return &rateLimiter{clients: make(map[string]clientWindow)} }

func (r *rateLimiter) allow(ip string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	r.evictLocked(now)
	w := r.clients[ip]
	if w.started.IsZero() || now.Sub(w.started) >= rateWindow {
		r.clients[ip] = clientWindow{started: now, count: 1}
		return true
	}
	if w.count >= rateLimit {
		return false
	}
	w.count++
	r.clients[ip] = w
	return true
}

// evictLocked drops windows that can no longer admit a request, and enforces the
// map bound when eviction was not enough.
func (r *rateLimiter) evictLocked(now time.Time) {
	for ip, w := range r.clients {
		if now.Sub(w.started) >= rateWindow {
			delete(r.clients, ip)
		}
	}
	if len(r.clients) < maxTrackedClients {
		return
	}
	// Still at the bound: clear wholesale rather than let memory grow. Existing
	// clients lose their budget once, which beats unbounded memory.
	r.clients = make(map[string]clientWindow)
}

type dailyCap struct {
	mu    sync.Mutex
	day   time.Time
	count int
	max   int
}

func newDailyCap(max int) *dailyCap { return &dailyCap{day: time.Now(), max: max} }

func (d *dailyCap) allow() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	if now.Sub(d.day) >= 24*time.Hour {
		d.day = now
		d.count = 0
	}
	if d.count >= d.max {
		return false
	}
	d.count++
	return true
}

// clientIP resolves the caller address for rate limiting.
//
// RemoteAddr is the only source trusted unless TRUSTED_PROXY_CIDRS names the
// proxy, because a forwarded header a client can set is otherwise a trivial way
// to defeat per-IP limiting. Behind a reverse proxy the naive alternative --
// always reading r.RemoteAddr -- collapses every internet client onto the proxy's
// single address, so all of them share one budget.
func clientIP(r *http.Request, trustedProxies []*net.IPNet) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remote := net.ParseIP(host)
	if remote == nil || len(trustedProxies) == 0 {
		return host
	}
	trusted := false
	for _, network := range trustedProxies {
		if network.Contains(remote) {
			trusted = true
			break
		}
	}
	if !trusted {
		return host
	}
	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded == "" {
		return host
	}
	// The left-most entry is the original client. Reject an entry that does not
	// parse rather than passing it through as an identity.
	first := forwarded
	if idx := strings.IndexByte(forwarded, ','); idx >= 0 {
		first = forwarded[:idx]
	}
	first = strings.TrimSpace(first)
	if net.ParseIP(first) == nil {
		return host
	}
	return first
}

func parseTrustedProxies(raw string) []*net.IPNet {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var networks []*net.IPNet
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, network, err := net.ParseCIDR(entry); err == nil {
			networks = append(networks, network)
			continue
		}
		if ip := net.ParseIP(entry); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			} else {
				ip = ip.To4()
			}
			networks = append(networks, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		}
	}
	return networks
}

// --------------------------------------------------------------------- startup

func main() {
	// The container image is distroless: no shell, no curl, no wget. Docker's
	// healthcheck therefore runs the binary itself.
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		os.Exit(runHealthcheck())
	}

	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	corsOrigin := strings.TrimSpace(os.Getenv("CORS_ORIGIN"))
	if corsOrigin == "" {
		// Wildcard CORS on a public, unauthenticated endpoint lets any web page
		// spend this deployment's quota. Browsers do not send credentials with a
		// wildcard, but a script does not need them. Fail closed.
		log.Println("warning: CORS_ORIGIN is not set; cross-origin browser requests will be rejected")
	}

	var engine RiskEngine
	switch cfg.Engine {
	case EngineJev:
		apiKey := os.Getenv("AI_GATEWAY_API_KEY")
		if apiKey == "" {
			log.Println("warning: AI_GATEWAY_API_KEY is not set; the jev engine will return 503")
		}
		engine = NewJevClient(cfg, apiKey)
		log.Println("using the jev engine (development only: it sends clause text to a third party)")
	default:
		engine = NewLayaClient(cfg)
		if cfg.LayaAPIKey == "" {
			// The inference service is internal, but an unauthenticated one is
			// one container misconfiguration away from being reachable.
			log.Println("warning: LAYA_API_KEY is not set; the inference service will accept unauthenticated calls")
		}
	}

	s := &server{
		cfg:            cfg,
		engine:         engine,
		client:         &http.Client{Timeout: upstreamTimeout},
		limit:          newRateLimiter(),
		daily:          newDailyCap(dailyCapMax),
		trustedProxies: parseTrustedProxies(os.Getenv("TRUSTED_PROXY_CIDRS")),
		corsOrigin:     corsOrigin,
		startedAt:      time.Now(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /ready", s.ready)
	mux.HandleFunc("POST /analyze", s.analyze)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// The default http.Server has no timeouts, which is a Slowloris surface:
	// clients can hold connections open indefinitely by dribbling headers.
	// WriteTimeout is generous because a 100-clause contract takes minutes on
	// CPU; see the Phase 7 benchmark.
	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           withCORS(s.corsOrigin, mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      15 * time.Minute,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("Clauseye backend listening on :%s engine=%s aggregator=%s calibration=%s",
		port, cfg.Engine, cfg.Aggregator, CalibrationProvisional)
	log.Fatal(httpServer.ListenAndServe())
}

// runHealthcheck probes this process's own readiness endpoint and reports it as
// an exit code, so Docker can use it without a shell in the image.
//
// It polls /ready rather than /health on purpose: /ready fails while the
// inference checkpoint is still loading, which is exactly the condition Docker
// should not restart or route around.
func runHealthcheck() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	client := &http.Client{Timeout: 5 * time.Second}

	for _, path := range []string{"/ready", "/health"} {
		request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+port+path, nil)
		if err != nil {
			continue
		}
		response, err := client.Do(request)
		if err != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		response.Body.Close()
		if response.StatusCode == http.StatusOK {
			return 0
		}
	}
	return 1
}

// ------------------------------------------------------------------- handlers

// health is liveness. It answers as long as the process is up, so a restart
// during a bad deploy does not look like a failed health check forever.
func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"uptime":  int(time.Since(s.startedAt).Seconds()),
		"engine":  s.engine.Name(),
		"version": SchemaVersion,
	})
}

// ready is readiness. It fails while the inference checkpoint is still loading,
// so an orchestrator keeps an unusable instance out of the load balancer instead
// of routing to it.
func (s *server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.engine.Ready(r.Context()); err != nil {
		writeErrorCode(w, http.StatusServiceUnavailable, "not_ready", "analysis engine is not ready")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "ready",
		"engine":      s.engine.Name(),
		"model":       s.engine.Model(),
		"calibration": CalibrationProvisional,
	})
}

func (s *server) analyze(w http.ResponseWriter, r *http.Request) {
	started := time.Now()

	if !s.limit.allow(clientIP(r, s.trustedProxies)) {
		writeErrorCode(w, http.StatusTooManyRequests, "rate_limited",
			fmt.Sprintf("rate limit exceeded; at most %d requests per %s per address", rateLimit, rateWindow))
		return
	}
	if !s.daily.allow() {
		writeErrorCode(w, http.StatusTooManyRequests, "daily_limit",
			"daily analysis limit reached; try again tomorrow")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxRequestBytes)
	defer r.Body.Close()

	var input analyzeRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&input); err != nil {
		// Distinguish an over-limit body from a malformed one, because the two
		// need different fixes at the client.
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErrorCode(w, http.StatusRequestEntityTooLarge, "body_too_large",
				fmt.Sprintf("request body exceeds the %d byte limit", s.cfg.MaxRequestBytes))
			return
		}
		writeErrorCode(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	if len(input.Clauses) == 0 {
		writeErrorCode(w, http.StatusBadRequest, "no_clauses", "clauses must contain at least one clause")
		return
	}
	if len(input.Clauses) > s.cfg.MaxClauses {
		writeErrorCode(w, http.StatusBadRequest, "too_many_clauses",
			fmt.Sprintf("at most %d clauses per request, got %d; split the contract", s.cfg.MaxClauses, len(input.Clauses)))
		return
	}

	// Document-level guard. This is an abuse guard rather than a risk
	// judgement: the endpoint costs nothing per token when Laya is local, but
	// it does cost CPU, and it exists to stop someone using this as a general
	// text classifier.
	if s.cfg.IncludeDocumentGate && !HasDocumentSignal(input.Clauses) {
		writeErrorCode(w, http.StatusBadRequest, "not_a_contract",
			"this does not appear to be a legal contract")
		return
	}

	eligible, skipped := PrepareClauses(input.Clauses, s.cfg)
	if len(eligible) == 0 {
		writeErrorCode(w, http.StatusBadRequest, "no_eligible_clauses",
			"no clause in this request was long enough or contractual enough to analyse")
		return
	}

	results, warnings, windowsDone, err := s.runPipeline(r.Context(), eligible)
	if err != nil {
		s.writeEngineError(w, err)
		return
	}

	analyzed := 0
	for _, result := range results {
		if result != nil {
			analyzed++
		}
	}
	response := AnalyzeResponse{
		SchemaVersion: SchemaVersion,
		Results:       make([]AnalysisResult, 0, len(input.Clauses)),
		Skipped:       skipped,
		Warnings:      warnings,
		Meta: ResponseMeta{
			Engine:          s.engine.Name(),
			Model:           s.engine.Model(),
			Aggregator:      s.cfg.Aggregator,
			Calibration:     CalibrationProvisional,
			ClausesReceived: len(input.Clauses),
			ClausesAnalyzed: analyzed,
			ClausesSkipped:  len(skipped),
			WindowsAnalyzed: windowsDone,
			DurationMS:      time.Since(started).Milliseconds(),
			Partial:         len(warnings) > 0,
		},
	}
	for i, result := range results {
		if result != nil {
			response.Results = append(response.Results, *result)
		} else {
			// A clause whose every window failed keeps its slot so the client's
			// clause numbering still lines up with the document.
			response.Results = append(response.Results, AnalysisResult{
				Clause:      input.Clauses[eligible[i].Index],
				ClauseIndex: eligible[i].Index,
				RiskLevel:   "low",
				Explanation: "This clause was not analysed because the analysis engine did not return a result for it.",
				Reason:      "not analysed",
				Issues:      []Issue{},
				NeedsReview: true,
			})
		}
	}
	writeJSON(w, http.StatusOK, response)
}

// runPipeline is the analysis pipeline: redact, window, batch, aggregate,
// explain. One chunk failing degrades the result rather than failing the whole
// request, because a 100-clause contract losing 80 clauses to one bad chunk is
// worse than a report that says which chunks are missing.
func (s *server) runPipeline(ctx context.Context, eligible []ClauseInput) ([]*AnalysisResult, []string, int, error) {
	// Build the window list once, redacting as we go. Redaction is the last
	// thing that happens to text before it leaves the process.
	windows := make([]string, 0, len(eligible))
	windowOwner := make([]int, 0, len(eligible))
	for i, clause := range eligible {
		for _, window := range windowText(clause.Text, s.cfg) {
			redacted, err := wuming.Redact(ctx, window)
			if err != nil {
				return nil, nil, 0, fmt.Errorf("redact clause %d: %w", clause.Index, err)
			}
			windows = append(windows, redacted)
			windowOwner = append(windowOwner, i)
		}
	}

	perClause := make([][]ClauseOutcome, len(eligible))
	var warnings []string
	windowsDone := 0

	for start := 0; start < len(windows); start += s.cfg.LayaBatchClauses {
		end := start + s.cfg.LayaBatchClauses
		if end > len(windows) {
			end = len(windows)
		}
		outcomes, err := s.engine.Analyze(ctx, windows[start:end])
		if err != nil {
			// Only fail the request if nothing at all succeeded.
			if countNonEmpty(perClause) == 0 {
				return nil, nil, windowsDone, err
			}
			warnings = append(warnings, fmt.Sprintf(
				"clauses analysed %d of %d: the engine stopped responding part way through, so some findings are missing",
				countNonEmpty(perClause), len(eligible)))
			log.Printf("degraded after %d windows: %v", windowsDone, err)
			break
		}
		for i, outcome := range outcomes {
			owner := windowOwner[start+i]
			perClause[owner] = append(perClause[owner], outcome)
		}
		windowsDone = end
	}

	results := make([]*AnalysisResult, len(eligible))
	for i, clause := range eligible {
		if len(perClause[i]) == 0 {
			results[i] = nil
			continue
		}
		result, err := s.assemble(clause, perClause[i])
		if err != nil {
			log.Printf("clause %d could not be assembled: %v", clause.Index, err)
			results[i] = nil
			continue
		}
		results[i] = result
	}
	if countNonEmpty(perClause) == 0 {
		return nil, nil, windowsDone, ErrEngineResponse
	}
	return results, warnings, windowsDone, nil
}

// assemble turns one clause's outcomes into the API result.
//
// Engines that answer per category are aggregated through the taxonomy. An
// engine that returns only a graded score -- the Jev development path -- still
// produces a result, but an explicitly thinner one that says so, rather than
// being dropped or being handed findings it never reported.
func (s *server) assemble(clause ClauseInput, outcomes []ClauseOutcome) (*AnalysisResult, error) {
	var categorized []Readings
	var fallback *ClauseOutcome
	for i := range outcomes {
		if outcomes[i].HasCategories() {
			categorized = append(categorized, outcomes[i].Categories)
			continue
		}
		if fallback == nil {
			fallback = &outcomes[i]
		}
	}

	base := AnalysisResult{
		Clause:      clause.Original,
		ClauseIndex: clause.Index,
		Location:    &ClauseLocation{ClauseIndex: clause.Index},
		Issues:      []Issue{},
	}

	if len(categorized) == 0 {
		if fallback == nil {
			return nil, errors.New("engine returned neither categories nor a score")
		}
		base.RiskLevel = fallback.Label
		base.RiskScore = clamp01(fallback.Score)
		base.Confidence = clamp01(fallback.Confidence)
		base.NeedsReview = true
		base.Explanation = "This engine returns a single graded score rather than per-category " +
			"findings, so this clause has no clause-level explanation and needs reading."
		base.Reason = fmt.Sprintf("%s: engine score %.2f, no per-category findings",
			fallback.Label, base.RiskScore)
		return &base, nil
	}

	agg, err := Aggregate(mergeReadings(categorized), s.cfg)
	if err != nil {
		return nil, err
	}
	explanation, reason := composeNarrative(agg, s.cfg)
	base.RiskLevel = agg.Level
	base.RiskScore = agg.Score
	base.Confidence = agg.Confidence
	base.NeedsReview = agg.NeedsReview
	base.Explanation = explanation
	base.Reason = reason
	base.Issues = buildIssues(agg, clause.Text, s.cfg)
	return &base, nil
}

// writeEngineError maps an engine failure onto a status code and a stable error
// code, without leaking the underlying detail to the caller.
func (s *server) writeEngineError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrEngineOpen):
		writeErrorCode(w, http.StatusServiceUnavailable, "engine_circuit_open",
			"the analysis engine is recovering; try again shortly")
	case errors.Is(err, ErrEngineUnavailable):
		writeErrorCode(w, http.StatusServiceUnavailable, "engine_unavailable",
			"the analysis engine is unavailable; try again shortly")
	case errors.Is(err, ErrEngineResponse):
		writeErrorCode(w, http.StatusBadGateway, "engine_bad_response",
			"the analysis engine returned an unexpected response")
	default:
		writeErrorCode(w, http.StatusInternalServerError, "internal", "analysis failed")
	}
}

func countNonEmpty(sets [][]ClauseOutcome) int {
	n := 0
	for _, set := range sets {
		if len(set) > 0 {
			n++
		}
	}
	return n
}

// ------------------------------------------------------------------- plumbing

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeErrorCode(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": message, "code": code})
}

// withCORS restricts browser access to the configured origin.
//
// With no origin configured the allow header is absent, so browsers block the
// cross-origin read. The request still executes server-side; what is removed is
// the ability for an arbitrary page to read the response and drive this service
// from a visitor's browser.
func withCORS(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
