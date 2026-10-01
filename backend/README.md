# Clauseye backend

Go analysis API plus the Laya inference service it calls.

```text
config.go        configuration, and the PROVISIONAL weights and thresholds
taxonomy.go      the nine risk categories: rationale and evidence patterns
aggregate.go     hysteresis, guarded groups, bounded combination
explain.go       deterministic explanation composition
chunking.go      clause eligibility and sentence windowing
apitypes.go      the schema v2 wire contract
engine.go        the RiskEngine interface both backends satisfy
laya_client.go   the self-hosted inference client
jev_client.go    development-only Vercel AI Gateway engine
main.go          wiring, transport, pipeline
```

See [`docs/architecture.md`](docs/architecture.md) for how it fits together and
[`docs/calibration.md`](docs/calibration.md) before trusting any number.

## Running

```powershell
docker compose up --build
```

The Laya service is deliberately **not** published to the host. It has no rate
limiting, no cost control, and will consume every core it is given; it is
reachable only from the backend container over the compose network.

## Configuration

Every value is read from the environment. `RISK_ENGINE=laya` is the default.

### Wiring

| Variable | Default | |
|---|---|---|
| `PORT` | `8080` | |
| `RISK_ENGINE` | `laya` | `laya` or `jev` |
| `LAYA_URL` | `http://127.0.0.1:8000/analyze-batch` | must be set for `laya`; **an empty value is an error, not a request for the default** |
| `LAYA_API_KEY` | | shared with the inference service. **Required in production.** |
| `LAYA_BATCH_CLAUSES` | `25` | windows per request |
| `LAYA_ATTEMPTS` | `2` | tries per chunk, including the first |
| `LAYA_TIMEOUT` | `10m` | per chunk |
| `CORS_ORIGIN` | | **fail closed.** Unset means cross-origin browser requests are rejected, not that `*` is used |
| `TRUSTED_PROXY_CIDRS` | | only these peers may set `X-Forwarded-For` |
| `RISK_BREAKER_THRESHOLD` | `5` | failures before the circuit opens |
| `RISK_BREAKER_COOLDOWN` | `60s` | |

### Limits and behaviour

| Variable | Default | |
|---|---|---|
| `MAX_CLAUSES` | `200` | |
| `MAX_REQUEST_BYTES` | `8388608` | |
| `MIN_CLAUSE_CHARS` | `40` | below this a clause is skipped and reported |
| `DOCUMENT_LEGAL_TERM_MIN` | `2` | document-level contract guard |
| `WINDOW_CHARS` | `2000` | Laya right-truncates silently past 512 tokens, so long clauses are windowed |
| `WINDOW_OVERLAP_CHARS` | `200` | |
| `RISK_INCLUDE_CLEAR_ISSUES` | `false` | emit clear categories too |
| `RISK_DOCUMENT_GATE` | `true` | |

### Scoring — all provisional

`RISK_AGGREGATOR` is `noisy-or-floored-v1` by default; `noisy-or-v1` and
`weighted-sum-v1` are also available.

`RISK_DETECT_THRESHOLD` `0.60`, `RISK_CLEAR_THRESHOLD` `0.40`,
`RISK_LEVEL_MEDIUM` `0.25`, `RISK_LEVEL_HIGH` `0.50`,
`RISK_LEVEL_CRITICAL` `0.75`, `RISK_REVIEW_CONFIDENCE_FLOOR` `0.60`,
`RISK_WEIGHT_{LIABILITY,INDEMNITY,TERMINATION,RENEWAL,OBLIGATION,JURISDICTION}`,
`RISK_LIABILITY_DAMPING`, `RISK_PLAIN_INDEMNITY_FACTOR`.

**These are not validated.** See [`docs/calibration.md`](docs/calibration.md).
Invalid combinations are rejected at start-up rather than silently clamped:
weights that do not sum to 1.0, `RISK_CLEAR_THRESHOLD` at or above
`RISK_DETECT_THRESHOLD`, out-of-order level thresholds, and an overlap larger
than the window all fail loudly.

## Endpoints

| | |
|---|---|
| `GET /health` | liveness; answers as soon as the process is up |
| `GET /ready` | readiness; `503` while the checkpoint is loading |
| `POST /analyze` | `{"clauses": ["..."]}` → schema v2 |

## Tests

```powershell
powershell -File scripts/run-tests.ps1          # gofmt, vet, suite
powershell -File scripts/run-tests.ps1 -Coverage
```

Go tests run inside `golang:1.22-alpine`, matching `go.mod` and the Dockerfile.
That is not a preference: a Windows Application Control policy on this machine
blocks freshly compiled test binaries from executing, so `go test` fails before
running anything. The reasoning is in the script.

## Benchmark

```powershell
docker compose --profile bench run --rm -e LAYA_THREADS=6 bench \
  --sizes 25,50,100 --out /app/testdata/benchmark_cpu.json
```

Results and their implications: [`docs/benchmark.md`](docs/benchmark.md).

## Logging

Clause text is never logged — not the clause, not the PII-redacted clause, not
the upstream response body. Only counts, statuses, sizes and outcomes.

## The Jev engine

`RISK_ENGINE=jev` calls `typesafe-ai/jev` through Vercel AI Gateway. It is a
**development and calibration tool only**: it costs money per token and it sends
clause text to a third party, which is exactly what running Laya locally avoids.
It answers with a single graded score rather than the nine categories, so it
produces a visibly thinner result.
