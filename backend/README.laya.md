# Clauseye Laya inference service

The FastAPI service that runs the Laya checkpoint. Called by the Go backend over
the compose network; not reachable from outside it.

## What it does

Answers nine `noul` (yes/no probability) questions about a batch of clause
windows and returns typed answers. It does **not** aggregate, score, or explain.
Those are deterministic Go code in the parent module, so the maths can be
unit-tested without a checkpoint and every word of every explanation can be
reviewed by a lawyer.

## Contract

Unchanged from the original proof of concept, so `test_laya_adapter.py` and
`run_laya_smoke.py` still pass unmodified:

```http
POST /analyze-batch   {"states": ["...", "..."]}
                      -> {"results": [{"answers": {...}, "routing": {...}, "usage": {...}}]}

GET  /health          -> {"status": "ok", "model": ..., "device": ...}
GET  /ready           -> 200 when the checkpoint is resident, 503 while loading
```

`answers[qid].noul` is `P(the statement holds)`. The `A`/`B` labels only change
the text the model sees in place of false/true; they never invert the answer.

## Hardening added over the proof of concept

Everything below was taken from `laya.serve`, the SDK's own Jev-protocol server,
except the batching which that server lacks and this service needs.

| | |
|---|---|
| Bearer auth | `hmac.compare_digest` on bytes, because Starlette decodes headers as latin-1 and a non-ASCII byte would otherwise raise inside the comparison |
| Body cap | streamed, not read from `Content-Length`. That header is absent under HTTP/2 and under chunked encoding, so trusting it is not a limit |
| State cap | 2400 characters. The checkpoint is built with `max_len=512` and `build_sequence` **silently right-truncates** once the question head has taken its share, and the tail of a clause is where liability carve-outs live. Rejecting is better than truncating: the Go side windows explicitly, so the boundary is visible in code |
| Question cap | 64 |
| No CORS | server-to-server only |
| Off-loop inference | `ThreadPoolExecutor(max_workers=1)` behind a gate. A synchronous CPU call on the event loop would stall `/health` and `/ready` for every client |
| Error hygiene | `except Exception -> 500 "inference failed"`. Exception text leaks paths and shapes |

`/ready` is what a container healthcheck should poll. The compose service does
exactly that, so a still-loading instance never receives traffic.

## Configuration

| Variable | Default | |
|---|---|---|
| `LAYA_MODEL` | `english` | also `multilingual`, `typed-decisions` |
| `LAYA_MODEL_PATH` | | local checkpoint mapped onto `LAYA_MODEL` |
| `LAYA_DEVICE` | auto | |
| `LAYA_API_KEY` | | bearer auth on `/analyze-batch` and `/ready`. **Required in production.** |
| `LAYA_BATCH_SIZE` | `8` | states per forward pass. See below — this is not a throughput lever on CPU |
| `LAYA_MAX_STATES` | `50` | |
| `LAYA_MAX_STATE_CHARS` | `2400` | |
| `LAYA_MAX_BODY_BYTES` | `2097152` | |
| `LAYA_THREADS` | | cap torch intra-op threads; about half the logical core count |
| `LAYA_PRELOAD` | `1` | build the checkpoint before serving |
| `LAYA_QUESTION_SET` | `default` | or `variant`; see the note below |
| `HF_HOME` | | checkpoint cache |

## Questions

`laya_questions.py` is the single source of truth, imported by both the service
and `scripts/validate_taxonomy.py` so they cannot drift. `validate_go_taxonomy()`
in the validator fails loudly if it and `taxonomy.go` diverge on the nine
identifiers.

Each question is a bare yes/no statement with A/B labels and no criteria, which
renders in the checkpoint's input format as:

```
noul question: Does this clause contain a limitation of liability?
[SEP] A: no, the statement does not hold [SEP] <clause> [SEP]
```

That is deliberate. The checkpoint's calibration is a function of the exact
input format it was trained on, so adding descriptive criteria moves the
probabilities and invalidates any figure already measured. `variant_questions()`
provides that alternative so it can be compared —
`scripts/validate_taxonomy.py --variant` — rather than changed silently.

**Four of the nine questions currently do not work well.** See
[`docs/calibration.md`](docs/calibration.md). This is a question-design problem,
not a threshold problem.

## Performance

Measured, not assumed. Full numbers in [`docs/benchmark.md`](docs/benchmark.md).

The headline: **~2 600 ms per clause on CPU**, flat across 25, 50 and 100
clauses. A 50-clause contract takes about 131 s and a 100-clause contract about
263 s. Batch size and concurrency both fail to move it; only `LAYA_THREADS`
does, sub-linearly, peaking around half the logical core count.

## Running

```powershell
.venv-laya\Scripts\python.exe -m uvicorn laya_adapter:app --host 127.0.0.1 --port 8000
```

or, with the checkpoint cached in a volume:

```powershell
docker compose up laya
```

The first start downloads ~842 MB from Hugging Face. After that, and with
`HF_HOME` on a persistent volume, inference is entirely offline: the SDK makes
no outbound network calls.
