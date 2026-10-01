package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sync"
	"time"
)

// LayaClient calls the self-hosted Laya inference service.
//
// Clause text is sent only to this service, which the deployment keeps on an
// internal network. Nothing here reaches a third party.
//
// Two behaviours are worth calling out because they exist to fail loudly rather
// than quietly produce a wrong risk level:
//
//   - every answer is validated against the nine-category contract, including
//     explicit NaN and range checks. The previous engine did
//     `score, _ := risk["score"].(float64)`, which turned a malformed response
//     into a confident-looking 0 and silently reclassified every clause in the
//     product when a provider changed shape.
//
//   - failures feed a circuit breaker, so an inference service that is down does
//     not consume a request timeout per clause before returning 502.
type LayaClient struct {
	url      string
	apiKey   string
	client   *http.Client
	timeout  time.Duration
	attempts int
	breaker  *breaker
	model    string
}

// NewLayaClient builds a client from configuration.
func NewLayaClient(cfg Config) *LayaClient {
	return &LayaClient{
		url:      cfg.LayaURL,
		apiKey:   cfg.LayaAPIKey,
		client:   &http.Client{Timeout: cfg.LayaTimeout},
		timeout:  cfg.LayaTimeout,
		attempts: cfg.LayaAttempts,
		breaker:  newBreaker(cfg.BreakerThreshold, cfg.BreakerCooldown),
	}
}

// SetModel records the checkpoint name reported by the service, for API
// metadata. Called after a readiness probe.
func (c *LayaClient) SetModel(model string) { c.model = model }

func (c *LayaClient) Name() string  { return string(EngineLaya) }
func (c *LayaClient) Model() string { return c.model }

// wire types for the inference service

type layaBatchRequest struct {
	States []string `json:"states"`
}

type layaBatchResponse struct {
	Results []layaResult `json:"results"`
}

type layaResult struct {
	Answers map[string]layaAnswer `json:"answers"`
}

type layaAnswer struct {
	// Noul is P(the statement holds). For a noul question the checkpoint always
	// returns P(true) here; the A/B labels never invert it.
	Noul float64 `json:"noul"`
	// Confidence is the model's certainty about its own answer, which for a
	// binary question is max(p, 1-p). A confidently clear category therefore
	// carries a high confidence and a low probability, which is exactly why the
	// two are never conflated in the API.
	Confidence float64 `json:"confidence"`
}

// Analyze evaluates the supplied states in one batched request.
func (c *LayaClient) Analyze(ctx context.Context, states []string) ([]ClauseOutcome, error) {
	if len(states) == 0 {
		return nil, nil
	}
	if err := c.breaker.allow(); err != nil {
		return nil, err
	}

	body, err := json.Marshal(layaBatchRequest{States: states})
	if err != nil {
		return nil, engineError(ErrEngineResponse, "encode request: %v", err)
	}

	var lastErr error
	for attempt := 1; attempt <= c.attempts; attempt++ {
		outcomes, err := c.attempt(ctx, body)
		if err == nil {
			c.breaker.recordSuccess()
			return outcomes, nil
		}
		lastErr = err
		// A response that does not match the contract will not match it on a
		// retry either, so it is not retried.
		if errors.Is(err, ErrEngineResponse) {
			break
		}
		log.Printf("laya attempt %d/%d failed: %v", attempt, c.attempts, err)
		if attempt < c.attempts {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}
	}

	c.breaker.recordFailure()
	return nil, lastErr
}

func (c *LayaClient) attempt(ctx context.Context, body []byte) ([]ClauseOutcome, error) {
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, engineError(ErrEngineUnavailable, "build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	response, err := c.client.Do(req)
	if err != nil {
		return nil, engineError(ErrEngineUnavailable, "call failed: %v", err)
	}
	defer response.Body.Close()

	// Read a bounded amount. The body is not logged; it is unmarshalled, so it
	// still needs a ceiling.
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return nil, engineError(ErrEngineUnavailable, "read response: %v", err)
	}
	log.Printf("laya status=%d bytes=%d", response.StatusCode, len(raw))

	switch {
	case response.StatusCode == http.StatusUnauthorized:
		return nil, engineError(ErrEngineUnavailable, "inference service rejected the credential (HTTP 401)")
	case response.StatusCode == http.StatusRequestEntityTooLarge:
		// Retrying will not help: a window is over the service's character cap,
		// so the windowing upstream is wrong.
		return nil, engineError(ErrEngineResponse, "inference service rejected a window as too large (HTTP 413); reduce WINDOW_CHARS")
	case response.StatusCode == http.StatusUnprocessableEntity:
		return nil, engineError(ErrEngineResponse, "inference service rejected the question schema (HTTP 422)")
	case response.StatusCode < 200 || response.StatusCode >= 300:
		return nil, engineError(ErrEngineUnavailable, "inference service returned HTTP %d", response.StatusCode)
	}

	var decoded layaBatchResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, engineError(ErrEngineResponse, "decode response: %v", err)
	}
	if len(decoded.Results) != len(jsonStates(body)) {
		return nil, engineError(ErrEngineResponse,
			"inference service returned %d results for %d states", len(decoded.Results), len(jsonStates(body)))
	}

	outcomes := make([]ClauseOutcome, 0, len(decoded.Results))
	for i, result := range decoded.Results {
		readings, err := parseAnswers(result.Answers)
		if err != nil {
			return nil, engineError(ErrEngineResponse, "state %d: %v", i, err)
		}
		outcomes = append(outcomes, ClauseOutcome{Categories: readings})
	}
	return outcomes, nil
}

// parseAnswers validates the nine answers for one state and converts them to
// Readings.
func parseAnswers(answers map[string]layaAnswer) (Readings, error) {
	readings := Readings{}
	for _, id := range categoryOrder {
		answer, ok := answers[id]
		if !ok {
			return nil, fmt.Errorf("response omitted %s", id)
		}
		if math.IsNaN(answer.Noul) || math.IsInf(answer.Noul, 0) {
			return nil, fmt.Errorf("%s has a non-finite probability", id)
		}
		if answer.Noul < 0 || answer.Noul > 1 {
			return nil, fmt.Errorf("%s has probability %.4f outside [0,1]", id, answer.Noul)
		}
		if math.IsNaN(answer.Confidence) || math.IsInf(answer.Confidence, 0) {
			return nil, fmt.Errorf("%s has a non-finite confidence", id)
		}
		if answer.Confidence < 0 || answer.Confidence > 1 {
			return nil, fmt.Errorf("%s has confidence %.4f outside [0,1]", id, answer.Confidence)
		}
		readings[id] = ProbabilityPair{Probability: answer.Noul, Confidence: answer.Confidence}
	}
	if err := ValidateReadings(readings); err != nil {
		return nil, err
	}
	return readings, nil
}

// jsonStates recovers the state count from the encoded request, so the response
// can be length-checked against what was actually asked for.
func jsonStates(body []byte) []string {
	var request layaBatchRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return nil
	}
	return request.States
}

// Ready probes the inference service's readiness endpoint.
//
// It returns nil only when the service reports the checkpoint is resident, so a
// still-loading instance does not advertise itself as able to serve.
func (c *LayaClient) Ready(ctx context.Context) error {
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, readyURL(c.url), nil)
	if err != nil {
		return engineError(ErrEngineUnavailable, "build readiness request: %v", err)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	response, err := c.client.Do(req)
	if err != nil {
		return engineError(ErrEngineUnavailable, "readiness probe failed: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return engineError(ErrEngineUnavailable, "inference service is not ready (HTTP %d)", response.StatusCode)
	}

	// Decode before draining. Reading the body first and decoding afterwards
	// yields an empty payload and silently loses the model name.
	var payload struct {
		Model  string `json:"model"`
		Loaded bool   `json:"loaded"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&payload); err == nil && payload.Model != "" {
		c.SetModel(payload.Model)
	}
	return nil
}

// readyURL derives /ready from the configured /analyze-batch URL.
func readyURL(analyzeURL string) string {
	switch {
	case len(analyzeURL) > len("/analyze-batch") &&
		analyzeURL[len(analyzeURL)-len("/analyze-batch"):] == "/analyze-batch":
		return analyzeURL[:len(analyzeURL)-len("/analyze-batch")] + "/ready"
	default:
		return analyzeURL + "/ready"
	}
}

// ------------------------------------------------------------------- breaker

// breaker is a minimal circuit breaker.
//
// It exists so that an inference service which is down, restarting, or
// deliberately loading does not make every request wait out its full timeout.
// Opening after a handful of consecutive failures is what keeps a cascading
// failure from becoming an outage.
type breaker struct {
	mu           sync.Mutex
	threshold    int
	cooldown     time.Duration
	failures     int
	openUntil    time.Time
	now          func() time.Time
	halfOpenBusy bool
}

func newBreaker(threshold int, cooldown time.Duration) *breaker {
	if threshold < 1 {
		threshold = 1
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	return &breaker{threshold: threshold, cooldown: cooldown, now: time.Now}
}

// allow reports whether a call may proceed.
func (b *breaker) allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.openUntil.IsZero() {
		return nil
	}
	if b.now().Before(b.openUntil) {
		return ErrEngineOpen
	}
	// Cooldown elapsed. Admit exactly one probe so the breaker cannot stampede
	// the recovering service with every waiting request at once.
	if b.halfOpenBusy {
		return ErrEngineOpen
	}
	b.halfOpenBusy = true
	return nil
}

func (b *breaker) recordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.openUntil = time.Time{}
	b.halfOpenBusy = false
}

func (b *breaker) recordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.halfOpenBusy = false
	b.failures++
	if b.failures >= b.threshold {
		b.openUntil = b.now().Add(b.cooldown)
		b.failures = 0
	}
}

// backoff returns the delay before retry attempt n, counting from one.
func backoff(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return 250 * time.Millisecond
	case attempt == 2:
		return time.Second
	default:
		return 2 * time.Second
	}
}
