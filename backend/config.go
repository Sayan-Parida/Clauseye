package main

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// Calibration status of the scoring parameters.
//
// The weights and thresholds in this file are ENGINEERING DEFAULTS, not
// measured facts. No labelled corpus of contract clauses exists in this
// repository, so nothing here has been validated against ground truth. They are
// plausible starting values chosen so that the aggregation is monotone and
// bounded, not because anyone has shown 0.30 is the right weight for liability.
//
// Until scripts/validate_taxonomy.py has been run over a labelled set and the
// numbers re-derived from measurements, treat every constant in Thresholds and
// GroupWeights as PROVISIONAL. The API surfaces this verbatim in
// meta.calibration so a downstream consumer never mistakes these for
// validated values.
const (
	CalibrationProvisional = "provisional-unvalidated"
	CalibrationValidated   = "validated"
)

// Engine selects the risk-analysis backend.
type Engine string

const (
	// EngineLaya calls the self-hosted Laya inference service. Clause text never
	// leaves the deployment network.
	EngineLaya Engine = "laya"
	// EngineJev calls typesafe-ai/jev through Vercel AI Gateway. It costs money
	// and transmits clause text to a third party, so it is a development and
	// calibration engine only, never a production fallback.
	EngineJev Engine = "jev"
)

// Aggregation strategies.
const (
	// AggregatorNoisyORFloored is the default. See aggregate.go for why the
	// floor is structurally necessary rather than a tuning preference.
	AggregatorNoisyORFloored = "noisy-or-floored-v1"
	// AggregatorNoisyOR is un-floored absolute noisy-OR. Kept so the floor can
	// be switched off and its effect measured rather than assumed.
	AggregatorNoisyOR = "noisy-or-v1"
	// AggregatorWeightedSum is the plain normalized weighted mean. Kept so all
	// three can be compared on the golden set before one is chosen.
	AggregatorWeightedSum = "weighted-sum-v1"
)

// Thresholds are PROVISIONAL. See the package note above.
type Thresholds struct {
	// Detect and Clear bound the hysteresis band. A probability at or above
	// Detect is "detected"; at or below Clear it is "clear"; between them it is
	// "uncertain". Clear < Detect is required.
	Detect float64
	Clear  float64
	// Medium, High and Critical are the lower bounds of the risk levels.
	Medium   float64
	High     float64
	Critical float64
	// ReviewConfidenceFloor is the confidence below which a result is flagged
	// needs_review regardless of which categories fired.
	ReviewConfidenceFloor float64
}

// GroupWeights weight the six guarded groups. PROVISIONAL.
//
// Liability and indemnity dominate because uncapped exposure and asymmetric
// indemnity are the categories with the largest financial consequence. Renewal,
// obligation and jurisdiction are real but more often commercial preferences
// than genuine hazards, so they carry less.
type GroupWeights struct {
	Liability    float64
	Indemnity    float64
	Termination  float64
	Renewal      float64
	Obligation   float64
	Jurisdiction float64
}

// Sum returns the total group weight. Both aggregation strategies assume it is
// 1.0.
func (w GroupWeights) Sum() float64 {
	return w.Liability + w.Indemnity + w.Termination + w.Renewal + w.Obligation + w.Jurisdiction
}

// GroupFactors tune the guarded group formulas. PROVISIONAL.
type GroupFactors struct {
	// LiabilityDamping is how strongly a liability cap suppresses the
	// uncapped-liability signal: 0.5 means a cap found with certainty halves
	// the uncapped reading.
	LiabilityDamping float64
	// PlainIndemnityFactor scales a plain indemnity relative to a one-sided
	// one. One-sided takes precedence via max(), so this only sets the floor
	// for a balanced indemnity.
	PlainIndemnityFactor float64
}

// Config is the resolved runtime configuration.
type Config struct {
	Engine Engine

	LayaURL     string
	LayaAPIKey  string
	LayaTimeout time.Duration
	// LayaAttempts is the total number of tries per chunk, including the first.
	LayaAttempts int
	// LayaBatchClauses is how many clauses go into a single upstream request.
	// Batching is what makes the model affordable: Router.predict_batch shares
	// forward passes across states.
	LayaBatchClauses int

	BreakerThreshold int
	BreakerCooldown  time.Duration

	Aggregator string

	Thresholds   Thresholds
	GroupWeights GroupWeights
	GroupFactors GroupFactors

	MaxClauses        int
	MaxRequestBytes   int64
	MinClauseChars    int
	DocumentLegalTerm int

	// WindowChars bounds one analysis window. Laya's encoder is built with
	// max_len=512 tokens and build_sequence right-truncates silently once the
	// question head has consumed its share, so an over-long clause loses its
	// tail. Windows keep every token in view instead.
	WindowChars         int
	WindowOverlapChars  int
	IncludeClearIssues  bool
	IncludeDocumentGate bool
}

// defaultConfig returns the built-in defaults, all PROVISIONAL.
func defaultConfig() Config {
	return Config{
		Engine:              EngineLaya,
		LayaURL:             "http://127.0.0.1:8000/analyze-batch",
		LayaTimeout:         120 * time.Second,
		LayaAttempts:        2,
		LayaBatchClauses:    25,
		BreakerThreshold:    5,
		BreakerCooldown:     60 * time.Second,
		Aggregator:          AggregatorNoisyORFloored,
		Thresholds:          defaultThresholds(),
		GroupWeights:        defaultGroupWeights(),
		GroupFactors:        defaultGroupFactors(),
		MaxClauses:          200,
		MaxRequestBytes:     8 << 20,
		MinClauseChars:      40,
		DocumentLegalTerm:   2,
		WindowChars:         2000,
		WindowOverlapChars:  200,
		IncludeClearIssues:  false,
		IncludeDocumentGate: true,
	}
}

// defaultThresholds are PROVISIONAL and unvalidated.
//
// The 0.25 / 0.50 / 0.75 level boundaries match the shape of the aggregation
// that was previously deleted from this repository, so they are familiar rather
// than invented. The hysteresis band around detection is new.
func defaultThresholds() Thresholds {
	return Thresholds{
		Detect:                0.60,
		Clear:                 0.40,
		Medium:                0.25,
		High:                  0.50,
		Critical:              0.75,
		ReviewConfidenceFloor: 0.60,
	}
}

func defaultGroupWeights() GroupWeights {
	return GroupWeights{
		Liability:    0.30,
		Indemnity:    0.25,
		Termination:  0.20,
		Renewal:      0.10,
		Obligation:   0.10,
		Jurisdiction: 0.05,
	}
}

func defaultGroupFactors() GroupFactors {
	return GroupFactors{
		LiabilityDamping:     0.50,
		PlainIndemnityFactor: 0.50,
	}
}

// LoadConfig resolves configuration from the environment, falling back to the
// defaults above. Values that cannot be parsed are reported rather than
// silently ignored, because a typo in RISK_DETECT_THRESHOLD should not quietly
// change risk classification.
func LoadConfig() (Config, error) {
	cfg := defaultConfig()
	var problems []string

	engine := strings.ToLower(strings.TrimSpace(os.Getenv("RISK_ENGINE")))
	switch Engine(engine) {
	case "":
	case EngineLaya:
		cfg.Engine = EngineLaya
	case EngineJev:
		cfg.Engine = EngineJev
	default:
		problems = append(problems, fmt.Sprintf("RISK_ENGINE %q is not one of laya, jev", engine))
	}

	agg := strings.TrimSpace(os.Getenv("RISK_AGGREGATOR"))
	switch agg {
	case "":
	case AggregatorNoisyORFloored, AggregatorNoisyOR, AggregatorWeightedSum:
		cfg.Aggregator = agg
	default:
		problems = append(problems, fmt.Sprintf("RISK_AGGREGATOR %q is not one of %s, %s, %s",
			agg, AggregatorNoisyORFloored, AggregatorNoisyOR, AggregatorWeightedSum))
	}

	if v, set := os.LookupEnv("LAYA_URL"); set {
		// An explicitly empty value is a misconfiguration, not a request for the
		// default: `LAYA_URL=` in a compose file or App Service setting would
		// otherwise silently send analysis to localhost.
		if strings.TrimSpace(v) == "" {
			problems = append(problems, "LAYA_URL is set but empty")
		} else {
			cfg.LayaURL = strings.TrimSpace(v)
		}
	}
	cfg.LayaAPIKey = os.Getenv("LAYA_API_KEY")

	problems = append(problems, envInt("LAYA_BATCH_CLAUSES", &cfg.LayaBatchClauses, 1, 200)...)
	problems = append(problems, envInt("LAYA_ATTEMPTS", &cfg.LayaAttempts, 1, 5)...)
	problems = append(problems, envInt("RISK_BREAKER_THRESHOLD", &cfg.BreakerThreshold, 1, 1000)...)
	problems = append(problems, envInt("MAX_CLAUSES", &cfg.MaxClauses, 1, 5000)...)
	problems = append(problems, envInt("MIN_CLAUSE_CHARS", &cfg.MinClauseChars, 1, 10000)...)
	problems = append(problems, envInt("DOCUMENT_LEGAL_TERM_MIN", &cfg.DocumentLegalTerm, 0, 100)...)
	problems = append(problems, envInt("WINDOW_CHARS", &cfg.WindowChars, 200, 20000)...)
	problems = append(problems, envInt("WINDOW_OVERLAP_CHARS", &cfg.WindowOverlapChars, 0, 5000)...)
	problems = append(problems, envBool("RISK_INCLUDE_CLEAR_ISSUES", &cfg.IncludeClearIssues)...)
	problems = append(problems, envBool("RISK_DOCUMENT_GATE", &cfg.IncludeDocumentGate)...)

	problems = append(problems, envFloat("RISK_DETECT_THRESHOLD", &cfg.Thresholds.Detect, 0, 1)...)
	problems = append(problems, envFloat("RISK_CLEAR_THRESHOLD", &cfg.Thresholds.Clear, 0, 1)...)
	problems = append(problems, envFloat("RISK_LEVEL_MEDIUM", &cfg.Thresholds.Medium, 0, 1)...)
	problems = append(problems, envFloat("RISK_LEVEL_HIGH", &cfg.Thresholds.High, 0, 1)...)
	problems = append(problems, envFloat("RISK_LEVEL_CRITICAL", &cfg.Thresholds.Critical, 0, 1)...)
	problems = append(problems, envFloat("RISK_REVIEW_CONFIDENCE_FLOOR", &cfg.Thresholds.ReviewConfidenceFloor, 0, 1)...)

	problems = append(problems, envFloat("RISK_WEIGHT_LIABILITY", &cfg.GroupWeights.Liability, 0, 1)...)
	problems = append(problems, envFloat("RISK_WEIGHT_INDEMNITY", &cfg.GroupWeights.Indemnity, 0, 1)...)
	problems = append(problems, envFloat("RISK_WEIGHT_TERMINATION", &cfg.GroupWeights.Termination, 0, 1)...)
	problems = append(problems, envFloat("RISK_WEIGHT_RENEWAL", &cfg.GroupWeights.Renewal, 0, 1)...)
	problems = append(problems, envFloat("RISK_WEIGHT_OBLIGATION", &cfg.GroupWeights.Obligation, 0, 1)...)
	problems = append(problems, envFloat("RISK_WEIGHT_JURISDICTION", &cfg.GroupWeights.Jurisdiction, 0, 1)...)

	problems = append(problems, envFloat("RISK_LIABILITY_DAMPING", &cfg.GroupFactors.LiabilityDamping, 0, 1)...)
	problems = append(problems, envFloat("RISK_PLAIN_INDEMNITY_FACTOR", &cfg.GroupFactors.PlainIndemnityFactor, 0, 1)...)

	if v := strings.TrimSpace(os.Getenv("LAYA_TIMEOUT")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			problems = append(problems, fmt.Sprintf("LAYA_TIMEOUT %q is not a duration", v))
		} else if d <= 0 {
			problems = append(problems, "LAYA_TIMEOUT must be positive")
		} else {
			cfg.LayaTimeout = d
		}
	}
	if v := strings.TrimSpace(os.Getenv("RISK_BREAKER_COOLDOWN")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			problems = append(problems, fmt.Sprintf("RISK_BREAKER_COOLDOWN %q is not a duration", v))
		} else if d <= 0 {
			problems = append(problems, "RISK_BREAKER_COOLDOWN must be positive")
		} else {
			cfg.BreakerCooldown = d
		}
	}
	if v := strings.TrimSpace(os.Getenv("MAX_REQUEST_BYTES")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("MAX_REQUEST_BYTES %q is not an integer", v))
		case n < 1024:
			problems = append(problems, "MAX_REQUEST_BYTES must be at least 1024")
		default:
			cfg.MaxRequestBytes = n
		}
	}

	problems = append(problems, cfg.validate()...)
	if len(problems) > 0 {
		return cfg, fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return cfg, nil
}

func (c Config) validate() []string {
	var problems []string

	if c.Thresholds.Clear >= c.Thresholds.Detect {
		problems = append(problems, fmt.Sprintf("RISK_CLEAR_THRESHOLD (%.3f) must be below RISK_DETECT_THRESHOLD (%.3f)", c.Thresholds.Clear, c.Thresholds.Detect))
	}
	if !(c.Thresholds.Medium <= c.Thresholds.High && c.Thresholds.High <= c.Thresholds.Critical) {
		problems = append(problems, fmt.Sprintf("risk level thresholds must be non-decreasing, got medium=%.3f high=%.3f critical=%.3f", c.Thresholds.Medium, c.Thresholds.High, c.Thresholds.Critical))
	}
	if sum := c.GroupWeights.Sum(); math.Abs(sum-1.0) > 1e-6 {
		problems = append(problems, fmt.Sprintf("risk group weights must sum to 1.0, got %.4f", sum))
	}
	if c.WindowOverlapChars >= c.WindowChars {
		problems = append(problems, fmt.Sprintf("WINDOW_OVERLAP_CHARS (%d) must be below WINDOW_CHARS (%d)", c.WindowOverlapChars, c.WindowChars))
	}
	if c.Engine == EngineLaya && strings.TrimSpace(c.LayaURL) == "" {
		problems = append(problems, "LAYA_URL is required when RISK_ENGINE=laya")
	}
	return problems
}

func envInt(name string, target *int, min, max int) []string {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return []string{fmt.Sprintf("%s %q is not an integer", name, v)}
	}
	if n < min || n > max {
		return []string{fmt.Sprintf("%s must be between %d and %d, got %d", name, min, max, n)}
	}
	*target = n
	return nil
}

func envFloat(name string, target *float64, min, max float64) []string {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return []string{fmt.Sprintf("%s %q is not a number", name, v)}
	}
	if f < min || f > max {
		return []string{fmt.Sprintf("%s must be between %v and %v, got %v", name, min, max, f)}
	}
	*target = f
	return nil
}

func envBool(name string, target *bool) []string {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	if v == "" {
		return nil
	}
	switch v {
	case "1", "true", "yes", "on":
		*target = true
	case "0", "false", "no", "off":
		*target = false
	default:
		return []string{fmt.Sprintf("%s %q is not a boolean", name, v)}
	}
	return nil
}
