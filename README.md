# Clauseye

Contract clause risk triage. A user uploads a PDF or DOCX contract; the
browser extracts the text and splits it into clauses; a Go service redacts
identifiers and sends each clause to a self-hosted Laya inference service that
scores nine risk categories; the result comes back as per-clause findings with
deterministic, quoted explanations.

Contract text never reaches a third party.

## Layout

```text
Clauseye/
├── backend/    Go API + the Laya inference service (FastAPI)
│   └── docs/   architecture, calibration status, benchmark results
└── frontend/   TanStack Start / React (Cloudflare Workers)
```

## Running it locally

Requires Docker. The first start downloads a ~842 MB model checkpoint, which
takes a few minutes.

```powershell
cd backend
docker compose up --build
```

| | |
|---|---|
| Frontend | http://localhost:3000 |
| API | http://localhost:8080 |
| Laya service | **not published** — internal network only |

The API returns `503` until the model checkpoint is resident; `GET /ready` is
what reports that. Check `docker compose ps` for `healthy`.

## Tests

```powershell
# Go: gofmt, go vet, and the suite, in golang:1.22-alpine
cd backend
powershell -File scripts/run-tests.ps1

# Python: the pre-existing POC smoke tests, unchanged
.venv-laya\Scripts\python.exe run_laya_smoke.py

# Frontend
cd ..\frontend
npm run lint
npx tsc --noEmit
```

`scripts/run-tests.ps1` runs the Go suite in a container rather than on the
host, because a Windows Application Control policy on this machine blocks
freshly compiled test executables from running. See the comment in that script.

## Documentation

| | |
|---|---|
| [`backend/docs/architecture.md`](backend/docs/architecture.md) | how it works and why it is split this way |
| [`backend/docs/calibration.md`](backend/docs/calibration.md) | **the weights and thresholds are provisional** — read before quoting any number |
| [`backend/docs/benchmark.md`](backend/docs/benchmark.md) | measured CPU performance, and what it implies for deployment |

## Status

The engine, the pipeline and the API are complete and tested. Two things are
deliberately not done:

- **The risk weights are unvalidated.** No labelled corpus of reviewed contracts
  exists, so every number in the taxonomy and every threshold is a documented
  default. The API reports `calibration: provisional-unvalidated` and the
  frontend surfaces it.
- **No deployment has been made.** Phase 7 benchmarked CPU inference and found
  it too slow for interactive use; the deployment decision is pending on that
  result rather than assumed.
