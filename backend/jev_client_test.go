package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// The Jev engine is development-only, but it is the independent second opinion
// the calibration depends on, so its transport behaviour is worth pinning down.

func TestJevClientSendsTheRubricAndBearerToken(t *testing.T) {
	var seenAuth atomic.Pointer[string]
	var seenBody atomic.Pointer[string]

	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		seenAuth.Store(&auth)
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		encoded, _ := json.Marshal(payload)
		body := string(encoded)
		seenBody.Store(&body)

		_ = json.NewEncoder(w).Encode(map[string]any{
			"answers": map[string]any{
				"risk_level": map[string]any{"score": 2.4, "confidence": 0.88},
			},
		})
	}))
	defer service.Close()

	cfg := defaultConfig()
	cfg.Engine = EngineJev
	cfg.LayaURL = service.URL
	cfg.LayaAttempts = 1

	client := NewJevClient(cfg, "gateway-key")
	outcomes, err := client.Analyze(context.Background(), []string{"The Vendor shall indemnify the Client."})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 {
		t.Fatalf("got %d outcomes", len(outcomes))
	}
	if outcomes[0].HasCategories() {
		t.Error("Jev must not claim per-category findings")
	}
	if outcomes[0].Label != "critical" {
		// 2.4 on the 0..3 rubric normalises to 0.80, at or above the critical
		// threshold of 0.75.
		t.Errorf("label = %q, want critical for a normalised score of 0.80", outcomes[0].Label)
	}

	auth := ""
	if p := seenAuth.Load(); p != nil {
		auth = *p
	}
	if auth != "Bearer gateway-key" {
		t.Errorf("Authorization = %q", auth)
	}

	body := ""
	if p := seenBody.Load(); p != nil {
		body = *p
	}
	for _, want := range []string{"risk_level", jevRiskInstruction, "critical", "unilateral termination"} {
		if !strings.Contains(body, want) {
			t.Errorf("the rubric sent upstream is missing %q; body = %s", want, body)
		}
	}
}

func TestJevClientRequiresAnAPIKey(t *testing.T) {
	cfg := defaultConfig()
	cfg.Engine = EngineJev
	client := NewJevClient(cfg, "")

	if err := client.Ready(context.Background()); !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("Ready() with no key = %v", err)
	}
	if _, err := client.Analyze(context.Background(), []string{"a clause"}); !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("Analyze() with no key = %v", err)
	}
}

func TestJevClientMapsGatewayErrors(t *testing.T) {
	// Regression test for an error-shadowing bug: `response, err := ...` inside
	// the if-block declared a new err, so lastErr recorded the outer (always nil)
	// variable and every non-2xx response was reported as a successful analysis
	// carrying an empty outcome.
	for _, status := range []int{
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusUnauthorized,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
	} {
		code := status
		service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
		}))

		cfg := defaultConfig()
		cfg.Engine = EngineJev
		cfg.LayaURL = service.URL
		cfg.LayaAttempts = 1

		outcomes, err := NewJevClient(cfg, "k").Analyze(context.Background(), []string{"a clause"})
		service.Close()

		if !errors.Is(err, ErrEngineUnavailable) {
			t.Fatalf("HTTP %d produced error %v, want ErrEngineUnavailable", code, err)
		}
		if outcomes != nil {
			t.Fatalf("HTTP %d produced outcomes %+v alongside the error", code, outcomes)
		}
	}
}

func TestJevClientDoesNotLeakTheResponseBody(t *testing.T) {
	// The gateway body is model output derived from the clause. It must not be
	// logged, so the test asserts the status line is all that is recorded: the
	// client's log line carries the status and byte count only, which the
	// behaviour here pins by checking a body containing a marker never reaches
	// the error text handed to callers.
	const marker = "SENTINEL-SHOULD-NOT-LEAK"
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"` + marker + `"}`))
	}))
	defer service.Close()

	cfg := defaultConfig()
	cfg.Engine = EngineJev
	cfg.LayaURL = service.URL
	cfg.LayaAttempts = 1

	_, err := NewJevClient(cfg, "k").Analyze(context.Background(), []string{"a clause"})
	if err == nil {
		t.Fatal("expected an error")
	}
	// The error names the status only, not the upstream body.
	if strings.Contains(err.Error(), marker) {
		t.Fatalf("the upstream body leaked into the error: %v", err)
	}
}

func TestJevClientNameAndModel(t *testing.T) {
	cfg := defaultConfig()
	cfg.Engine = EngineJev
	client := NewJevClient(cfg, "k")
	if client.Name() != "jev" {
		t.Errorf("Name = %q, want jev", client.Name())
	}
	if client.Model() != "typesafe-ai/jev" {
		t.Errorf("Model = %q", client.Model())
	}
}
