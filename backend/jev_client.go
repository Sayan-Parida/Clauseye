package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
)

// JevClient calls typesafe-ai/jev through Vercel AI Gateway.
//
// DEVELOPMENT AND CALIBRATION ENGINE ONLY.
//
// Two reasons it must not be a production path:
//
//   - it costs money per input token, and the deployment's quota is shared by
//     whoever can reach the endpoint;
//   - it transmits clause text to a third party, which is precisely what running
//     Laya locally exists to avoid.
//
// It is retained because it is the only independent second opinion available for
// checking whether Laya's per-category readings agree with a different model.
// That comparison is what makes the calibration in Phase 2 falsifiable.
//
// It answers a single graded score, not the nine categories, so it produces
// visibly less information than the Laya path. It does not pretend otherwise.
type JevClient struct {
	url      string
	apiKey   string
	client   *http.Client
	attempts int
	model    string
}

// jevURL is the AI Gateway evaluation endpoint.
const jevURL = "https://ai-gateway.vercel.sh/v1/evaluate"

// jevRiskInstruction is the whole of the risk semantics handed to Jev.
//
// Note what this is not: it is not a category taxonomy. It is the rubric from
// the previous implementation, preserved verbatim so the two engines are compared
// on equal terms during calibration.
const jevRiskInstruction = "High risk: uncapped liability, one-sided indemnity, " +
	"auto-renewal traps, unilateral termination. Low risk: balanced terms, " +
	"mutual obligations, standard jurisdiction."

func NewJevClient(cfg Config, apiKey string) *JevClient {
	url := jevURL
	if raw := strings.TrimSpace(cfg.LayaURL); raw != "" && cfg.Engine == EngineJev {
		// Allows pointing the Jev engine at a test double.
		url = raw
	}
	return &JevClient{
		url:      url,
		apiKey:   apiKey,
		client:   &http.Client{Timeout: upstreamTimeout},
		attempts: cfg.LayaAttempts,
		model:    "typesafe-ai/jev",
	}
}

func (c *JevClient) Name() string  { return string(EngineJev) }
func (c *JevClient) Model() string { return c.model }

func (c *JevClient) Ready(ctx context.Context) error {
	if c.apiKey == "" {
		return engineError(ErrEngineUnavailable, "AI_GATEWAY_API_KEY is not set")
	}
	return nil
}

// Analyze evaluates one state per call, because Jev's evaluation endpoint takes
// a single `state` string. Batching would require one request per window.
func (c *JevClient) Analyze(ctx context.Context, states []string) ([]ClauseOutcome, error) {
	if c.apiKey == "" {
		return nil, engineError(ErrEngineUnavailable, "AI_GATEWAY_API_KEY is not set")
	}
	outcomes := make([]ClauseOutcome, 0, len(states))
	for i, state := range states {
		outcome, err := c.analyzeOne(ctx, state)
		if err != nil {
			return nil, engineError(ErrEngineUnavailable, "state %d: %v", i, err)
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes, nil
}

func (c *JevClient) analyzeOne(ctx context.Context, state string) (ClauseOutcome, error) {
	payload := map[string]any{
		"model": c.model,
		"state": state,
		"questions": map[string]any{
			"risk_level": map[string]any{
				"type":         "score",
				"instructions": jevRiskInstruction,
				"criteria":     []string{"low", "medium", "high", "critical"},
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ClauseOutcome{}, err
	}

	var lastErr error
	for attempt := 1; attempt <= c.attempts; attempt++ {
		reqCtx, cancel := context.WithTimeout(ctx, upstreamTimeout)

		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.url, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
			req.Header.Set("Content-Type", "application/json")
			// Assigned rather than declared: `response, err := ...` here would
			// declare a new err scoped to the if-block, shadowing the loop's err,
			// and lastErr below would then always record nil. That made every
			// upstream HTTP failure report as a successful analysis with an empty
			// outcome.
			var response *http.Response
			response, err = c.client.Do(req)
			if err == nil {
				raw, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
				response.Body.Close()
				// Status and size only. The body is model output derived from the
				// clause and may quote it back, so it is never written to logs.
				log.Printf("jev status=%d attempt=%d bytes=%d", response.StatusCode, attempt, len(raw))
				if readErr != nil {
					err = readErr
				} else if response.StatusCode < 200 || response.StatusCode >= 300 {
					err = fmt.Errorf("AI Gateway returned HTTP %d", response.StatusCode)
				} else {
					var data any
					if err = json.Unmarshal(raw, &data); err == nil {
						outcome, parseErr := parseJevResult(data)
						cancel()
						if parseErr == nil {
							return outcome, nil
						}
						return ClauseOutcome{}, parseErr
					}
				}
			}
		}
		cancel()
		lastErr = err
	}
	return ClauseOutcome{}, lastErr
}

// parseJevResult maps the gateway response onto a single graded outcome.
//
// The score is normalised from the 0..3 rubric onto 0..1 so it is comparable
// with the Laya path's risk_score. Confidence is read from the answer, and
// falls back to zero when the gateway omits it rather than being invented: for
// choice and score answers TypeSafe documents confidence in
// providerMetadata, so the answer object frequently has none.
func parseJevResult(data any) (ClauseOutcome, error) {
	root, ok := data.(map[string]any)
	if !ok {
		return ClauseOutcome{}, engineError(ErrEngineResponse, "unexpected gateway response shape")
	}
	answers, _ := root["answers"].(map[string]any)
	if answers == nil {
		return ClauseOutcome{}, engineError(ErrEngineResponse, "gateway response omitted answers")
	}
	risk, _ := answers["risk_level"].(map[string]any)
	if risk == nil {
		return ClauseOutcome{}, engineError(ErrEngineResponse, "gateway response omitted risk_level")
	}

	raw, ok := risk["score"].(float64)
	if !ok || math.IsNaN(raw) || math.IsInf(raw, 0) {
		return ClauseOutcome{}, engineError(ErrEngineResponse, "risk_level had no finite score")
	}
	if raw < 0 || raw > 3 {
		return ClauseOutcome{}, engineError(ErrEngineResponse, "risk_level score %.4f outside [0,3]", raw)
	}
	confidence, _ := risk["confidence"].(float64)
	if math.IsNaN(confidence) || confidence < 0 || confidence > 1 {
		confidence = 0
	}

	score := raw / 3
	label := riskLevel(score, defaultThresholds())
	return ClauseOutcome{
		Categories: nil, // Jev cannot answer per category; the API will say so.
		Score:      score,
		Confidence: confidence,
		Label:      label,
	}, nil
}
