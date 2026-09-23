package main

import (
	"bytes"
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
)

const jevURL = "https://ai-gateway.vercel.sh/v1/evaluate"

type analyzeRequest struct {
	Clauses []string `json:"clauses"`
}

type analysisResult struct {
	Clause     string  `json:"clause"`
	RiskLevel  string  `json:"risk_level"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

type analyzeResponse struct {
	Results []analysisResult `json:"results"`
}

type server struct {
	apiKey string
	client *http.Client
	limit  *rateLimiter
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
	if w.count >= 10 {
		return false
	}
	w.count++
	r.clients[ip] = w
	return true
}

func main() {
	apiKey := os.Getenv("AI_GATEWAY_API_KEY")
	if apiKey == "" {
		log.Println("warning: AI_GATEWAY_API_KEY is not set; /analyze will return 503")
	}

	s := &server{
		apiKey: apiKey,
		client: &http.Client{},
		limit:  newRateLimiter(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("POST /analyze", s.analyze)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("Clauseye backend listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, withCORS(mux)))
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
	if s.apiKey == "" {
		writeError(w, http.StatusServiceUnavailable, "analysis service is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	var input analyzeRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(input.Clauses) == 0 {
		writeError(w, http.StatusBadRequest, "clauses must contain at least one clause")
		return
	}
	if len(input.Clauses) > 50 {
		writeError(w, http.StatusBadRequest, "at most 50 clauses are allowed per request")
		return
	}

	results := make([]analysisResult, 0, len(input.Clauses))
	for i, clause := range input.Clauses {
		if strings.TrimSpace(clause) == "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("clause %d is empty", i+1))
			return
		}
		result, err := s.assess(clause)
		if err != nil {
			log.Printf("analysis failed for clause %d: %v", i+1, err)
			writeError(w, http.StatusBadGateway, "analysis provider failed; please retry")
			return
		}
		results = append(results, result)
	}
	writeJSON(w, http.StatusOK, analyzeResponse{Results: results})
}

func (s *server) assess(clause string) (analysisResult, error) {
	payload := map[string]any{
		"model": "typesafe-ai/jev",
		"state": clause,
		"questions": map[string]any{
			"risk_level": map[string]any{
				"type":         "score",
				"instructions": "High risk: uncapped liability, one-sided indemnity, auto-renewal traps, unilateral termination. Low risk: balanced terms, mutual obligations, standard jurisdiction.",
				"criteria":     []string{"low", "medium", "high", "critical"},
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return analysisResult{}, err
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, jevURL, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+s.apiKey)
			req.Header.Set("Content-Type", "application/json")
			var response *http.Response
			response, err = s.client.Do(req)
			if err == nil {
				rawBody, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
				response.Body.Close()
				log.Printf("Jev response: HTTP %d body=%s", response.StatusCode, rawBody)
				if readErr != nil {
					err = readErr
				} else if response.StatusCode >= 200 && response.StatusCode < 300 {
					var data any
					err = json.Unmarshal(rawBody, &data)
					if err == nil {
						result, parseErr := parseResult(clause, data)
						if parseErr == nil {
							return result, nil
						}
						err = parseErr
					}
				} else {
					err = fmt.Errorf("Jev returned HTTP %d", response.StatusCode)
				}
			}
		}
		cancel()
		lastErr = err
	}
	return analysisResult{}, lastErr
}

func parseResult(clause string, data any) (analysisResult, error) {
	root, ok := data.(map[string]any)
	if !ok {
		return analysisResult{}, errors.New("unexpected provider response")
	}

	answers, _ := root["answers"].(map[string]any)
	if answers == nil {
		return analysisResult{}, errors.New("provider response omitted answers")
	}
	risk, _ := answers["risk_level"].(map[string]any)
	if risk == nil {
		return analysisResult{}, errors.New("provider response omitted risk_level")
	}

	score, _ := risk["score"].(float64)
	confidence, _ := risk["confidence"].(float64)

	labels := []string{"low", "medium", "high", "critical"}
	idx := int(score)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(labels) {
		idx = len(labels) - 1
	}
	label := labels[idx]

	return analysisResult{
		Clause:     clause,
		RiskLevel:  label,
		Confidence: confidence,
		Reason:     fmt.Sprintf("Jev score: %.2f", score),
	}, nil
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
