"""Clause risk inference service for the Laya model.

This is the hardened successor to the original proof-of-concept adapter. The
external contract is deliberately unchanged so the existing smoke tests
(`test_laya_adapter.py`, `run_laya_smoke.py`) keep working unchanged:

    POST /analyze-batch  {"states": [...]}  ->  {"results": [...]}
    GET  /health                           ->  {"status": "ok", ...}

What changed is everything behind that contract, adopting the security patterns
from `laya.serve` (the SDK's own Jev-protocol server) while keeping the batching
that `laya.serve` lacks:

  * bearer authentication, compared in constant time
  * request limits enforced by streaming the body, not by trusting
    Content-Length (which is absent under HTTP/2 and under chunked encoding)
  * inference off the event loop, in a single-worker thread pool behind a gate,
    so /health and /ready stay responsive while a batch is running
  * a distinct /ready that reports whether the checkpoint is resident, which is
    what a container healthcheck should poll
  * errors that never echo exception text, because torch OOM messages contain
    paths and shapes

What this service does not do: it does not aggregate, score, or explain. It
returns typed probabilities. Aggregation, thresholds and explanation composition
are deterministic Go code, so that the maths is unit-testable without a model and
so a reviewer can read every word of every explanation the product emits.
"""

from __future__ import annotations

import asyncio
import hmac
import json
import os
import time
from concurrent.futures import ThreadPoolExecutor
from contextlib import asynccontextmanager
from typing import Any, Iterator

from fastapi import FastAPI, Header, HTTPException, Request
from fastapi.responses import JSONResponse
from pydantic import BaseModel, Field

import laya
from laya_questions import QUESTION_SETS, legal_questions

# ---------------------------------------------------------------- configuration

MODEL = os.getenv("LAYA_MODEL", "english")
MODEL_PATH = os.getenv("LAYA_MODEL_PATH") or None
DEVICE = os.getenv("LAYA_DEVICE") or None
MAX_STATES = int(os.getenv("LAYA_MAX_STATES", "50"))
# States per forward pass. Each state is tokenised once per question, so this is
# really states * 9 rows per batch. 8 states is ~72 rows of 512 tokens, which is
# a reasonable working set for CPU inference. TUNED IN PHASE 7 -- see
# scripts/benchmark_laya.py before changing this blindly.
BATCH_SIZE = int(os.getenv("LAYA_BATCH_SIZE", "8"))
QUESTION_SET = os.getenv("LAYA_QUESTION_SET", "default")
API_KEY = os.getenv("LAYA_API_KEY") or None
THREADS = os.getenv("LAYA_THREADS") or None
PRELOAD = os.getenv("LAYA_PRELOAD", "1").strip().lower() in ("1", "true", "yes", "on")
LOG_LEVEL = os.getenv("LAYA_LOG_LEVEL", "info")

# Request limits.
#
# MAX_STATE_CHARS is far tighter than laya.serve's 50000 because the checkpoint
# is configured with max_len=512 and build_sequence silently right-truncates the
# state once the question head has taken its share of the budget. A long clause
# loses its tail, and the tail is where liability carve-outs live. So rather
# than truncate silently, this service rejects: the Go side windows clauses
# explicitly, and the window boundary is therefore visible in the code.
MAX_STATE_CHARS = int(os.getenv("LAYA_MAX_STATE_CHARS", "2400"))
MAX_QUESTIONS = 64
MAX_BODY_BYTES = int(os.getenv("LAYA_MAX_BODY_BYTES", str(2 * 1024 * 1024)))

# Compared as bytes, not str: hmac.compare_digest raises TypeError when a str
# operand holds a non-ASCII character, and Starlette decodes headers as latin-1.
_expected_auth = ("Bearer " + API_KEY).encode("utf-8", "surrogateescape") if API_KEY else b""

_started_at = time.monotonic()


def _check_auth(authorization: str | None) -> None:
    if API_KEY is None:
        return
    supplied = (authorization or "").encode("utf-8", "surrogateescape")
    if not hmac.compare_digest(supplied, _expected_auth):
        raise HTTPException(status_code=401, detail="invalid or missing bearer token")


async def _read_body_capped(request: Request) -> bytes:
    """Read the body, abandoning it as soon as it exceeds the cap.

    Content-Length cannot be the only gate. It is a value the client chooses, and
    under Transfer-Encoding: chunked it is absent altogether, so a request that
    simply omits it would otherwise be buffered in full regardless of its size.
    """
    total = 0
    chunks: list[bytes] = []
    async for chunk in request.stream():
        if not chunk:
            continue
        total += len(chunk)
        if total > MAX_BODY_BYTES:
            raise HTTPException(status_code=413, detail="request body too large")
        chunks.append(chunk)
    return b"".join(chunks)


def _check_request_limits(states: Any) -> None:
    if not isinstance(states, list):
        raise HTTPException(status_code=400, detail="'states' must be an array")
    if not states:
        raise HTTPException(status_code=400, detail="'states' must not be empty")
    if len(states) > MAX_STATES:
        raise HTTPException(
            status_code=413, detail=f"too many states ({len(states)} > {MAX_STATES})"
        )
    for index, state in enumerate(states):
        if not isinstance(state, str):
            raise HTTPException(
                status_code=400, detail=f"state {index} must be a string"
            )
        if len(state) > MAX_STATE_CHARS:
            raise HTTPException(
                status_code=413,
                detail=(
                    f"state {index} is {len(state)} characters, over the "
                    f"{MAX_STATE_CHARS} limit; window it upstream rather than "
                    "relying on silent truncation"
                ),
            )


def _apply_thread_limit() -> int | None:
    """Cap torch intra-op threads.

    Oversubscribing logical cores against a CPU encoder is a large regression,
    so LAYA_THREADS should be at or below the physical core count.
    """
    if not THREADS:
        return None
    try:
        n = int(THREADS)
    except ValueError:
        return None
    if n <= 0:
        return None
    try:
        import torch
    except ImportError:
        return None
    torch.set_num_threads(n)
    return n


# ---------------------------------------------------------------------- adapter


class BatchRequest(BaseModel):
    states: list[str] = Field(min_length=1, max_length=MAX_STATES)


class Adapter:
    """Wraps the Laya Router with batched, typed inference."""

    def __init__(self) -> None:
        self._apply_thread_limit()
        kwargs: dict[str, Any] = {"preload": False, "max_loaded": 1}
        if DEVICE:
            kwargs["device"] = DEVICE
        if MODEL_PATH:
            kwargs["models"] = {MODEL: MODEL_PATH}
        self.router = laya.Router(**kwargs)
        self.model = MODEL
        self.question_set = QUESTION_SET if QUESTION_SET in QUESTION_SETS else "default"
        self.load_seconds: float | None = None

        start = time.monotonic()
        if PRELOAD:
            # Build the checkpoint before serving so no request pays a model
            # load. This blocks start-up, which is why /ready exists: a container
            # healthcheck polling /ready sees "not ready" until this finishes.
            self.router.preload([self.model])
            self.load_seconds = time.monotonic() - start

    @property
    def loaded(self) -> bool:
        try:
            return bool(self.router.loaded)
        except Exception:  # noqa: BLE001 -- readiness must never raise
            return False

    def _questions(self) -> dict[str, dict[str, Any]]:
        return QUESTION_SETS[self.question_set]()

    def predict_batch(self, states: list[str]) -> list[dict[str, Any]]:
        questions = self._questions()
        requests = [
            {"state": state, "questions": questions, "model": self.model}
            for state in states
        ]
        # Every request in one call, so states sharing this question schema
        # share forward passes. This is the whole reason for batching.
        results = self.router.predict_batch(requests, batch_size=BATCH_SIZE)
        if len(results) != len(states):
            raise RuntimeError("Laya returned an unexpected result count")
        return [normalize_result(result) for result in results]

    def _apply_thread_limit(self) -> None:
        self._threads = _apply_thread_limit()


def normalize_result(result: dict[str, Any]) -> dict[str, Any]:
    """Reduce one Laya result to the fields the Go client reads.

    The answers are passed through untouched. Any transformation of the model's
    probabilities would make the Go-side validation meaningless, because the
    thing being validated would no longer be what the model produced.
    """
    answers = result.get("answers")
    if not isinstance(answers, dict):
        raise RuntimeError("Laya result omitted answers")
    return {
        "answers": answers,
        "routing": result.get("routing"),
        "usage": result.get("usage"),
    }


# ------------------------------------------------------------------ application

adapter = Adapter()

# One worker. A forward pass is synchronous torch; letting several run at once on
# one CPU or GPU agent makes each of them slower, and the Router already guards
# checkpoint lifecycle. Requests queue here, which is the correct behaviour for a
# CPU-bound service: it bounds memory and keeps tail latency predictable.
_pool = ThreadPoolExecutor(max_workers=1, thread_name_prefix="laya-infer")
# Created on first use: an asyncio.Lock binds to the loop running when it is
# first awaited, and the adapter is constructed at module scope.
_gate: asyncio.Lock | None = None


@asynccontextmanager
async def lifespan(_app: FastAPI) -> Iterator[None]:
    try:
        yield
    finally:
        _pool.shutdown(wait=True, cancel_futures=True)


app = FastAPI(
    title="Clauseye Laya inference service",
    version="1.0.0",
    summary="Batched typed risk-question inference over the Laya checkpoint",
    lifespan=lifespan,
)


@app.get("/health")
def health() -> dict[str, Any]:
    """Liveness. Answers as soon as the process is up, loaded or not."""
    return {
        "status": "ok",
        "model": MODEL,
        "model_path": MODEL_PATH,
        "device": DEVICE or "auto",
    }


@app.get("/ready")
def ready() -> dict[str, Any]:
    """Readiness. This is what a container healthcheck should poll.

    Returns 503 until the checkpoint is resident, so a load still in progress
    keeps the service out of a load-balanced pool instead of serving errors.
    """
    payload = {
        "status": "ready" if adapter.loaded else "loading",
        "model": MODEL,
        "model_path": MODEL_PATH,
        "device": DEVICE or "auto",
        "loaded": adapter.loaded,
        "load_seconds": round(adapter.load_seconds, 3) if adapter.load_seconds else None,
        "uptime_seconds": round(time.monotonic() - _started_at, 3),
        "threads": getattr(adapter, "_threads", None),
        "batch_size": BATCH_SIZE,
        "max_states": MAX_STATES,
        "max_state_chars": MAX_STATE_CHARS,
        "question_set": adapter.question_set,
        "auth_required": API_KEY is not None,
    }
    if not adapter.loaded:
        return JSONResponse(status_code=503, content=payload)
    return payload


@app.post("/analyze-batch")
async def analyze_batch(request: Request, authorization: str | None = Header(default=None)):
    global _gate

    _check_auth(authorization)

    if request.headers.get("content-length"):
        try:
            if int(request.headers["content-length"]) > MAX_BODY_BYTES:
                raise HTTPException(status_code=413, detail="request body too large")
        except ValueError:
            pass

    raw = await _read_body_capped(request)
    try:
        body = json.loads(raw)
    except ValueError:
        raise HTTPException(status_code=400, detail="request body must be valid JSON")
    if not isinstance(body, dict) or "states" not in body:
        raise HTTPException(status_code=400, detail="body must be an object with a 'states' field")

    states = body["states"]
    _check_request_limits(states)

    if _gate is None:
        _gate = asyncio.Lock()

    try:
        # Off the event loop. A synchronous CPU call on the loop would stall
        # /health and /ready for every client until it finished.
        async with _gate:
            loop = asyncio.get_running_loop()
            results = await loop.run_in_executor(_pool, adapter.predict_batch, states)
        return {"results": results}
    except HTTPException:
        raise
    except ValueError as exc:
        # Question validation errors name the question and what to fix, and are
        # safe to return.
        raise HTTPException(status_code=422, detail=str(exc))
    except Exception:  # noqa: BLE001 -- never leak paths, shapes or OOM text
        raise HTTPException(status_code=500, detail="inference failed")


def legal_questions_public() -> dict[str, dict[str, Any]]:
    """Exposed for scripts and tests; the schema itself lives in laya_questions."""
    return legal_questions()


def main() -> None:
    import uvicorn

    uvicorn.run(
        app,
        host=os.getenv("LAYA_HOST", "0.0.0.0"),
        port=int(os.getenv("LAYA_PORT", "8000")),
        log_level=LOG_LEVEL,
    )


if __name__ == "__main__":
    main()
