import os
from typing import Any

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field

import laya


MODEL = os.getenv("LAYA_MODEL", "english")
MODEL_PATH = os.getenv("LAYA_MODEL_PATH") or None
DEVICE = os.getenv("LAYA_DEVICE") or None
MAX_STATES = int(os.getenv("LAYA_MAX_STATES", "50"))
BATCH_SIZE = int(os.getenv("LAYA_BATCH_SIZE", "50"))


class BatchRequest(BaseModel):
    states: list[str] = Field(min_length=1, max_length=MAX_STATES)


class Adapter:
    def __init__(self) -> None:
        kwargs: dict[str, Any] = {"preload": False, "max_loaded": 1}
        if DEVICE:
            kwargs["device"] = DEVICE
        if MODEL_PATH:
            kwargs["models"] = {MODEL: MODEL_PATH}
        self.router = laya.Router(**kwargs)
        self.model = MODEL
        self.router.preload([self.model])

    def predict_batch(self, states: list[str]) -> list[dict[str, Any]]:
        requests = [
            {
                "state": state,
                "questions": legal_questions(),
                "model": self.model,
            }
            for state in states
        ]
        results = self.router.predict_batch(requests, batch_size=BATCH_SIZE)
        if len(results) != len(states):
            raise RuntimeError("Laya returned an unexpected result count")
        return [normalize_result(result) for result in results]


def legal_questions() -> dict[str, dict[str, Any]]:
    return {
        "limitation_of_liability": {
            "type": "noul",
            "instructions": "Does this clause contain a limitation of liability?",
            "labels": {"false": "A", "true": "B"},
        },
        "uncapped_liability": {
            "type": "noul",
            "instructions": "Is liability uncapped or unlimited in this clause?",
            "labels": {"false": "A", "true": "B"},
        },
        "indemnification": {
            "type": "noul",
            "instructions": "Does this clause contain an indemnification obligation?",
            "labels": {"false": "A", "true": "B"},
        },
        "one_sided_indemnity": {
            "type": "noul",
            "instructions": "Is the indemnification obligation one-sided?",
            "labels": {"false": "A", "true": "B"},
        },
        "termination_right": {
            "type": "noul",
            "instructions": "Does this clause create a termination right?",
            "labels": {"false": "A", "true": "B"},
        },
        "unilateral_termination": {
            "type": "noul",
            "instructions": "Is termination unilateral in this clause?",
            "labels": {"false": "A", "true": "B"},
        },
        "auto_renewal": {
            "type": "noul",
            "instructions": "Does this clause contain automatic renewal?",
            "labels": {"false": "A", "true": "B"},
        },
        "jurisdiction_related": {
            "type": "noul",
            "instructions": "Does this clause concern jurisdiction or governing law?",
            "labels": {"false": "A", "true": "B"},
        },
        "unusual_obligation": {
            "type": "noul",
            "instructions": "Does this clause create an unusual or one-sided obligation?",
            "labels": {"false": "A", "true": "B"},
        },
    }


def normalize_result(result: dict[str, Any]) -> dict[str, Any]:
    answers = result.get("answers")
    if not isinstance(answers, dict):
        raise RuntimeError("Laya result omitted answers")
    return {
        "answers": answers,
        "routing": result.get("routing"),
        "usage": result.get("usage"),
    }


app = FastAPI(title="Clauseye Laya Adapter", version="0.1.0")
adapter = Adapter()


@app.get("/health")
def health() -> dict[str, Any]:
    return {"status": "ok", "model": MODEL, "model_path": MODEL_PATH, "device": DEVICE or "auto"}


@app.post("/analyze-batch")
def analyze_batch(request: BatchRequest) -> dict[str, Any]:
    if any(not state.strip() for state in request.states):
        raise HTTPException(status_code=400, detail="states must not contain empty strings")
    try:
        return {"results": adapter.predict_batch(request.states)}
    except ValueError as exc:
        raise HTTPException(status_code=422, detail=str(exc)) from exc
    except Exception as exc:
        raise HTTPException(status_code=500, detail="Laya inference failed") from exc
