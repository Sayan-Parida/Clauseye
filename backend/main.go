package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/taoq-ai/wuming"
)

const (
	defaultLayaURL = "http://laya:8000/v1/systemone"
	maxRequestBody = 1 << 20
	maxClauses     = 50
)

type analyzeRequest struct {
	Clauses []string `json:"clauses"`
}

type issue struct {
	Category   string  `json:"category"`
	Detected   bool    `json:"detected"`
	Confidence float64 `json:"confidence"`
}

type clauseLocation struct {
	ClauseIndex int `json:"clause_index"`
}

type analysisResult struct {
	Clause     string          `json:"clause"`
	RiskLevel  string          `json:"risk_level"`
	RiskScore  float64         `json:"risk_score"`
	Confidence float64         `json:"confidence"`
	Issues     []issue         `json:"issues"`
	Location   *clauseLocation `json:"location,omitempty"`
}

type analyzeResponse struct {
	Results []analysisResult `json:"results"`
}

type server struct {
	client    *http.Client
	layaURL   string
	layaKey   string
	limit     *rateLimiter
	daily     *dailyCap
	batchSize int
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
	w := r.clients[ip]
	if w.started.IsZero() || now.Sub(w.started) >= time.Minute {
		r.clients[ip] = clientWindow{started: now, count: 1}
		return true
	}
	if w.count >= 3 {
		return false
	}
	w.count++
	r.clients[ip] = w
	return true
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

func main() {
	layaURL := os.Getenv("LAYA_URL")
	if layaURL == "" {
		layaURL = defaultLayaURL
	}
	batchSize := 50
	if value := os.Getenv("LAYA_BATCH_SIZE"); value != "" {
		if _, err := fmt.Sscanf(value, "%d", &batchSize); err != nil || batchSize < 1 || batchSize > maxClauses {
			batchSize = 50
		}
	}

	s := &server{
		client:    &http.Client{},
		layaURL:   layaURL,
		layaKey:   os.Getenv("LAYA_API_KEY"),
		limit:     newRateLimiter(),
		daily:     newDailyCap(500),
		batchSize: batchSize,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("POST /analyze", s.analyze)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	_ = http.ListenAndServe(":"+port, withCORS(mux))
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) analyze(w http.ResponseWriter, r *http.Request) {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	if !s.limit.allow(ip) {
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded; try again in a minute")
		return
	}
	if !s.daily.allow() {
		writeError(w, http.StatusTooManyRequests, "daily limit reached; try again tomorrow")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	defer r.Body.Close()
	var input analyzeRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := validateClauses(input.Clauses); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	results, err := s.assessBatch(r.Context(), input.Clauses)
	if err != nil {
		writeError(w, http.StatusBadGateway, "analysis provider failed; please retry")
		return
	}
	writeJSON(w, http.StatusOK, analyzeResponse{Results: results})
}

func validateClauses(clauses []string) error {
	if len(clauses) == 0 {
		return errors.New("clauses must contain at least one clause")
	}
	if len(clauses) > maxClauses {
		return fmt.Errorf("at most %d clauses are allowed per request", maxClauses)
	}
	for i, clause := range clauses {
		if len(strings.TrimSpace(clause)) < 20 {
			return fmt.Errorf("clause %d is too short to analyze", i+1)
		}
	}
	allText := strings.ToLower(strings.Join(clauses, " "))
	legalTerms := []string{"shall", "agreement", "party", "clause", "indemnify", "terminate", "liability", "jurisdiction", "obligation"}
	matches := 0
	for _, term := range legalTerms {
		if strings.Contains(allText, term) {
			matches++
		}
	}
	if matches < 2 {
		return errors.New("this does not appear to be a legal contract")
	}
	return nil
}

type layaRequest struct {
	State     string                  `json:"state"`
	Questions map[string]layaQuestion `json:"questions"`
}

type layaQuestion struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     any               `json:"criteria,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
}

type layaResponse struct {
	Answers map[string]layaAnswer `json:"answers"`
}

type layaAnswer struct {
	Choice        string             `json:"choice,omitempty"`
	Score         float64            `json:"score,omitempty"`
	Noul          float64            `json:"noul,omitempty"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Distribution  []float64          `json:"distribution,omitempty"`
}

func (s *server) assessBatch(ctx context.Context, clauses []string) ([]analysisResult, error) {
	results := make([]analysisResult, 0, len(clauses))
	for start := 0; start < len(clauses); start += s.batchSize {
		end := start + s.batchSize
		if end > len(clauses) {
			end = len(clauses)
		}
		batch, err := s.assessChunk(ctx, clauses[start:end], start)
		if err != nil {
			return nil, err
		}
		results = append(results, batch...)
	}
	return results, nil
}

func (s *server) assessChunk(ctx context.Context, clauses []string, offset int) ([]analysisResult, error) {
	results := make([]analysisResult, 0, len(clauses))
	for index, clause := range clauses {
		anonymizedClause, err := wuming.Redact(ctx, clause)
		if err != nil {
			return nil, fmt.Errorf("redact clause %d: %w", offset+index+1, err)
		}
		decision, err := s.callLaya(ctx, anonymizedClause)
		if err != nil {
			return nil, fmt.Errorf("laya clause %d: %w", offset+index+1, err)
		}
		results = append(results, aggregateResult(clause, offset+index, decision))
	}
	return results, nil
}

func (s *server) callLaya(ctx context.Context, state string) (layaResponse, error) {
	questions := legalQuestions()
	body, err := json.Marshal(layaRequest{State: state, Questions: questions})
	if err != nil {
		return layaResponse{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, s.layaURL, bytes.NewReader(body))
	if err != nil {
		return layaResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.layaKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.layaKey)
	}
	response, err := s.client.Do(req)
	if err != nil {
		return layaResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return layaResponse{}, fmt.Errorf("laya returned HTTP %d", response.StatusCode)
	}
	var result layaResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxRequestBody)).Decode(&result); err != nil {
		return layaResponse{}, err
	}
	if err := validateLayaResponse(result); err != nil {
		return layaResponse{}, err
	}
	return result, nil
}

func legalQuestions() map[string]layaQuestion {
	return map[string]layaQuestion{
		"limitation_of_liability": {Type: "noul", Instructions: "Does this clause contain a limitation of liability?", Labels: map[string]string{"false": "A", "true": "B"}},
		"uncapped_liability":      {Type: "noul", Instructions: "Is liability uncapped or unlimited in this clause?", Labels: map[string]string{"false": "A", "true": "B"}},
		"indemnification":         {Type: "noul", Instructions: "Does this clause contain an indemnification obligation?", Labels: map[string]string{"false": "A", "true": "B"}},
		"one_sided_indemnity":     {Type: "noul", Instructions: "Is the indemnification obligation one-sided?", Labels: map[string]string{"false": "A", "true": "B"}},
		"termination_right":       {Type: "noul", Instructions: "Does this clause create a termination right?", Labels: map[string]string{"false": "A", "true": "B"}},
		"unilateral_termination":  {Type: "noul", Instructions: "Is termination unilateral in this clause?", Labels: map[string]string{"false": "A", "true": "B"}},
		"auto_renewal":            {Type: "noul", Instructions: "Does this clause contain automatic renewal?", Labels: map[string]string{"false": "A", "true": "B"}},
		"jurisdiction_related":    {Type: "noul", Instructions: "Does this clause concern jurisdiction or governing law?", Labels: map[string]string{"false": "A", "true": "B"}},
		"unusual_obligation":      {Type: "noul", Instructions: "Does this clause create an unusual or one-sided obligation?", Labels: map[string]string{"false": "A", "true": "B"}},
	}
}

func validateLayaResponse(response layaResponse) error {
	questions := legalQuestions()
	for name, question := range questions {
		answer, ok := response.Answers[name]
		if !ok {
			return fmt.Errorf("laya response omitted %s", name)
		}
		if question.Type == "noul" && (answer.Noul < 0 || answer.Noul > 1) {
			return fmt.Errorf("laya response has invalid %s probability", name)
		}
		if answer.Confidence < 0 || answer.Confidence > 1 {
			return fmt.Errorf("laya response has invalid %s confidence", name)
		}
	}
	return nil
}

func aggregateResult(clause string, index int, response layaResponse) analysisResult {
	checks := []struct {
		category string
		value    float64
		weight   float64
	}{
		{"limitation_of_liability", response.Answers["limitation_of_liability"].Noul, 0.15},
		{"uncapped_liability", response.Answers["uncapped_liability"].Noul, 0.35},
		{"indemnification", response.Answers["indemnification"].Noul, 0.15},
		{"one_sided_indemnity", response.Answers["one_sided_indemnity"].Noul, 0.30},
		{"termination_right", response.Answers["termination_right"].Noul, 0.05},
		{"unilateral_termination", response.Answers["unilateral_termination"].Noul, 0.25},
		{"auto_renewal", response.Answers["auto_renewal"].Noul, 0.20},
		{"jurisdiction_related", response.Answers["jurisdiction_related"].Noul, 0.05},
		{"unusual_or_one_sided_obligation", response.Answers["unusual_obligation"].Noul, 0.20},
	}
	issues := make([]issue, 0, len(checks))
	score := 0.0
	confidence := 0.0
	for _, check := range checks {
		answerKey := check.category
		if check.category == "unusual_or_one_sided_obligation" {
			answerKey = "unusual_obligation"
		}
		answer := response.Answers[answerKey]
		detected := check.value >= 0.5
		if detected {
			score += check.weight * check.value
		}
		issues = append(issues, issue{Category: check.category, Detected: detected, Confidence: answer.Confidence})
		if detected && answer.Confidence > confidence {
			confidence = answer.Confidence
		}
	}
	if score > 1 {
		score = 1
	}
	return analysisResult{
		Clause:     clause,
		RiskLevel:  riskLevel(score),
		RiskScore:  score,
		Confidence: confidence,
		Issues:     issues,
		Location:   &clauseLocation{ClauseIndex: index},
	}
}

func riskLevel(score float64) string {
	switch {
	case score >= 0.75:
		return "critical"
	case score >= 0.50:
		return "high"
	case score >= 0.25:
		return "medium"
	default:
		return "low"
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := os.Getenv("CORS_ORIGIN")
		if origin == "" {
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
