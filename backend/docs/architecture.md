# Architecture

## The pipeline

```
browser
  ├─ pdfjs-dist / mammoth     extract text          (the file never leaves the page)
  ├─ splitClauses             segment on Section N / N.N / (a) / iv.
  └─ strip numbering          1. / (a) removed before analysis
        │  POST {clauses: [...]}
        ▼
Go API  ── the only public entry point
  ├─ rate limit      3/min per address, TTL-evicted, X-Forwarded-For only from trusted proxies
  ├─ validate        body cap, ≤200 clauses, document-level contract guard
  ├─ PrepareClauses  drop too-short and non-contractual blocks, report them as skipped[]
  ├─ window          sentence windows with overlap (Laya silently right-truncates past 512 tokens)
  ├─ wuming.Redact   strip identifiers ─ last step before anything leaves the process
  ├─ chunk           ≤25 windows per request
  ├─ LayaClient      2 attempts, 10-min timeout, circuit breaker, STRICT response validation
  ├─ Aggregate       hysteresis → guarded groups → bounded combination
  └─ explain         deterministic rationale + verbatim evidence quote
        │  POST /analyze-batch  {"states": [...]}   internal network only
        ▼
Laya service  ── unauthenticated to the world, bearer to its caller
  ├─ hmac.compare_digest on the bearer token
  ├─ streaming body cap (Content-Length cannot be trusted)
  ├─ ≤50 states, ≤2400 chars per state, ≤64 questions
  ├─ Router.predict_batch() in a 1-worker thread pool off the event loop
  └─ 9 noul questions → {noul, confidence} per category
```

Contract text stops at the Laya container. Nothing reaches a third party.

## Division of labour

The split is deliberate: **the model detects, Go decides.**

Laya is a bidirectional encoder with a decision head that emits one probability
per masked option (`laya/common.py build_sequence`, `laya/agent.py
_decode_answers`). It has no autoregressive decode path, no sampling, and no
ability to emit prose. Everything that requires judgement, ordering or words
therefore lives in Go where it can be unit-tested without a checkpoint and
reviewed by a lawyer:

| Concern | Owner | Why |
|---|---|---|
| "does this clause contain an indemnity" | Laya | needs reading comprehension |
| "does that count as high risk" | Go | policy, not perception |
| category polarity and grouping | Go | taxonomy is a legal position |
| hysteresis and thresholds | Go | tunable, testable, revertible |
| the word "high" in the explanation | Go | static text, human-reviewable |
| which clause words to quote | Go | deterministic regex, verbatim |

## Explanations are composed, never generated

No LLM is involved in producing any user-visible string. Each explanation is:

```
<Rationale from the taxonomy, written by a human>
  + ' In this clause: "<verbatim substring matched by a regex>".'
```

The regex match is either an exact substring of the user's own contract or it is
absent — in which case `evidence_found` is `false` and the UI says the finding
rests on the model alone. No model output is ever paraphrased as a quote.

Consequences: no hallucination is possible, there is no added latency or cost,
and the text of every explanation the product can emit is reviewable before
ship.

## Files

| File | Role |
|---|---|
| `config.go` | env parsing; the **provisional** weights and thresholds |
| `taxonomy.go` | the nine categories, polarity, group, reliability, rationale, evidence regexes |
| `aggregate.go` | hysteresis, guarded groups, three aggregators, confidence, `needs_review` |
| `explain.go` | evidence extraction and narrative composition |
| `chunking.go` | clause eligibility, numbering removal, sentence windowing |
| `apitypes.go` | the schema v2 wire contract |
| `engine.go` | the `RiskEngine` interface both backends satisfy |
| `laya_client.go` | inference client: batching, retry, breaker, strict validation |
| `jev_client.go` | development-only Vercel AI Gateway engine |
| `main.go` | wiring, transport, pipeline |
| `laya_questions.py` | the nine questions; the single source of truth for the service and the validator |
| `laya_adapter.py` | the FastAPI inference service |
| `scripts/validate_taxonomy.py` | measures the model against the golden set |
| `scripts/benchmark_laya.py` | the Phase 7 benchmark |

## The API contract

`POST /analyze` → `{"clauses": ["..."]}`. Response `schema_version: 2`:

```json
{
  "schema_version": 2,
  "results": [{
    "clause": "The Company may terminate at any time without notice.",
    "clause_index": 3,
    "risk_level": "high",
    "risk_score": 0.50,
    "confidence": 0.75,
    "needs_review": false,
    "explanation": "High risk: 1 finding drives this score: Unilateral termination (probability 0.75).",
    "reason": "high: unilateral_termination p=0.75",
    "issues": [{
      "category": "unilateral_termination",
      "label": "Unilateral termination",
      "polarity": "risk",
      "reliability": "weak",
      "state": "detected",
      "detected": true,
      "probability": 0.75,
      "confidence": 0.75,
      "weight": 0.20,
      "group_risk": 0.75,
      "contributes": true,
      "explanation": "One party can end the agreement... In this clause: \"at any time and without notice\".",
      "evidence": "at any time and without notice",
      "evidence_label": "no notice",
      "evidence_found": true
    }],
    "location": { "clause_index": 3 }
  }],
  "skipped": [{ "clause_index": 7, "reason": "too_short", "detail": "11 characters..." }],
  "meta": {
    "engine": "laya", "model": "english", "aggregator": "noisy-or-floored-v1",
    "calibration": "provisional-unvalidated",
    "clauses_received": 12, "clauses_analyzed": 11, "clauses_skipped": 1,
    "windows_analyzed": 11, "duration_ms": 27639, "partial": false
  }
}
```

Three fields exist to prevent specific misreadings:

- **`probability` and `confidence` are separate.** The model reports confidence
  as `max(p, 1-p)`, so a category considered *clear* still carries ~0.95
  confidence. Rendering that beside `detected: false` would tell a lawyer a
  clause is "95% risky". The UI always shows probability for likelihood and
  labels confidence as "model certainty".
- **`risk_score` is an index, not a probability.** It is rendered as a bar with a
  fraction, never as a percentage.
- **`reliability` carries the measured weakness** of each category so a weak one
  is not presented as equal to a measured one.

Also available: `GET /health` (liveness) and `GET /ready` (readiness, 503 while
the checkpoint loads).

## Failures

| Condition | Behaviour |
|---|---|
| Laya down or restarting | circuit breaker opens after 5 failures for 60 s; requests fail fast with `503 engine_circuit_open` |
| One chunk of many fails | the successful chunks are returned with `meta.partial: true` and a warning. A 100-clause contract losing 80 clauses to one bad chunk is worse than a report that says which are missing |
| Response violates the contract | that chunk counts as an engine failure; **never** substituted with zero |
| No clause eligible | `400 no_eligible_clauses` |
| Clause numbers | preserved 1:1 with the input, so the client's indexing lines up with the document |

## Privacy

**What happens.** The file is parsed in the page and never uploaded. Clause text
goes to the Go service, has identifiers stripped by `wuming`, and is sent to the
Laya container on the internal network. Nothing is stored: there is no database.

**What does not.** `wuming` detects structured identifiers — national IDs,
emails, phone numbers, IBANs, card numbers, IPs, MACs, URLs — and **has no
person-name or organisation-name detector**, so a natural person's name and a
company name both pass through. That is asserted as a test
(`TestRunPipelineRedactsIdentifiersBeforeTheEngineSeesText`) so the limitation
cannot regress unnoticed.

It is not treated as a bug to paper over with a regex. In a *contract* the
party names are functionally necessary: masking "Customer" and "Vendor" would
destroy the asymmetry that `one_sided_indemnity` exists to detect. So for
contracts the privacy posture cannot be "scrub everything" — it has to be "your
infrastructure holds the text". That is the property running Laya locally
provides, and the property sending clause text to a gateway would not.

Clause text is never logged. Not the clause, not the redacted clause, not the
upstream response body. Only counts, statuses and sizes.
