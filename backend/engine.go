package main

import (
	"context"
	"errors"
	"fmt"
)

// ClauseOutcome is one analysed analysis-window, as returned by an engine.
//
// The two shapes exist because the two engines answer different questions.
// Laya returns all nine categories, which is the shape the taxonomy and the
// explanations are built around. The Jev development engine returns a single
// graded score and cannot produce per-category findings, so it fills Score and
// leaves Categories nil.
//
// Collapsing both into one shape would either force Jev to invent categories it
// never produced, or force the taxonomy to work without categories. Keeping them
// apart means the Jev path stays an honest fallback that is visibly less
// informative, rather than one that pretends to be equivalent.
type ClauseOutcome struct {
	Categories Readings
	Score      float64
	Confidence float64
	Label      string
}

// HasCategories reports whether this outcome carries per-category findings.
func (o ClauseOutcome) HasCategories() bool { return o.Categories != nil }

// RiskEngine evaluates analysis windows.
//
// Implementations must be safe for concurrent use: the HTTP server serves
// requests on many goroutines at once.
type RiskEngine interface {
	// Name identifies the engine in API metadata, e.g. "laya".
	Name() string
	// Model identifies the checkpoint or upstream model in use.
	Model() string
	// Analyze evaluates the supplied windows and returns one outcome per window,
	// in the same order.
	Analyze(ctx context.Context, states []string) ([]ClauseOutcome, error)
	// Ready reports whether the engine can serve traffic. It backs the /ready
	// endpoint so a container orchestrator can keep an instance whose checkpoint
	// is still loading out of the load balancer.
	Ready(ctx context.Context) error
}

// Errors an engine may return. Handlers map these onto status codes.
var (
	// ErrEngineUnavailable means the engine could not be reached or is not
	// ready. Callers should surface 503.
	ErrEngineUnavailable = errors.New("risk engine unavailable")
	// ErrEngineResponse means the engine answered but the answer did not match
	// the contract. Treated as an engine failure rather than papered over.
	ErrEngineResponse = errors.New("risk engine returned an unexpected response")
	// ErrEngineOpen means the circuit breaker is open and calls are being
	// failed fast on purpose.
	ErrEngineOpen = errors.New("risk engine circuit breaker is open")
)

// engineError wraps a cause with one of the sentinels above.
func engineError(sentinel error, format string, args ...any) error {
	return fmt.Errorf("%w: %s", sentinel, fmt.Sprintf(format, args...))
}
