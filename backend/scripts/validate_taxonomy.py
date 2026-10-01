"""Measure how well the Laya checkpoint answers the nine risk questions.

This is the script that justifies the weights and thresholds, and the reason
Phase 2 comes before the backend is wired up. Until it has been run against a
reviewed labelled set, the weights in backend/config.go are guesses, and the
project should say so rather than imply otherwise.

It does three things:

  1. runs every clause in testdata/clauses_golden.json through the model and
     records P(true) for each of the nine categories;
  2. reports, per category, how often the expected detections fired and how often
     the expected absences stayed clear, at the configured thresholds;
  3. reports expected-calibration-error style reliability, so the reported
     confidence can be checked against how often the model is actually right.

Important caveat, printed at the top of the output: the golden set's labels were
written by the author of this system. Agreement with them is not validation. It
is an upper bound on accuracy, and it measures the author's reading rather than
the law.

Usage:
    python scripts/validate_taxonomy.py
    python scripts/validate_taxonomy.py --variant
    python scripts/validate_taxonomy.py --dump-probabilities out.json

The JSON dump is the regression lock: once thresholds have been chosen from a
reviewed set, commit the dump so a later change to the question wording, the
model, or the batch size shows up as a diff rather than as silent drift.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from laya_questions import CATEGORY_IDS, QUESTION_SETS  # noqa: E402

GOLDEN = REPO / "testdata" / "clauses_golden.json"

# Thresholds must match the Go defaults so the report describes shipped behaviour.
DETECT_THRESHOLD = float(os.getenv("RISK_DETECT_THRESHOLD", "0.60"))
CLEAR_THRESHOLD = float(os.getenv("RISK_CLEAR_THRESHOLD", "0.40"))


def validate_go_taxonomy() -> None:
    """Fail loudly if the Go taxonomy and the Python question set have drifted.

    The two files are maintained separately by necessity -- one is Go, one is
    Python -- but they must agree on the nine identifiers and their order, or the
    Go client will reject valid responses and the report will be meaningless.
    """
    go_source = (REPO / "taxonomy.go").read_text(encoding="utf-8")
    missing = [cid for cid in CATEGORY_IDS if f'"{cid}"' not in go_source]
    if missing:
        raise SystemExit(
            "taxonomy drift: these category ids are in laya_questions.py but not "
            f"in taxonomy.go: {', '.join(missing)}"
        )


def load_golden() -> list[dict]:
    payload = json.loads(GOLDEN.read_text(encoding="utf-8"))
    if payload.get("calibration_status") != "provisional-unvalidated":
        raise SystemExit(
            "the golden set is marked "
            f"{payload.get('calibration_status')!r}. If the labels have been "
            "independently reviewed, update the status string and record who "
            "reviewed them in the file's $comment block."
        )
    return payload["clauses"]


def build_adapter(device: str | None, model: str):
    import laya

    kwargs: dict = {"preload": False, "max_loaded": 1}
    if device:
        kwargs["device"] = device
    router = laya.Router(**kwargs)
    load_start = time.monotonic()
    router.preload([model])
    load_seconds = time.monotonic() - load_start
    return router, model, load_seconds


def run_batch(router, model: str, question_set: str, states: list[str], batch_size: int):
    questions = QUESTION_SETS[question_set]()
    requests = [{"state": s, "questions": questions, "model": model} for s in states]
    results = router.predict_batch(requests, batch_size=batch_size)
    if len(results) != len(states):
        raise SystemExit("Laya returned an unexpected result count")
    return results


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--variant", action="store_true",
                        help="use descriptive criteria instead of A/B labels")
    parser.add_argument("--model", default=os.getenv("LAYA_MODEL", "english"))
    parser.add_argument("--device", default=os.getenv("LAYA_DEVICE") or None)
    parser.add_argument("--batch-size", type=int, default=8)
    parser.add_argument("--clauses", type=int, default=0,
                        help="limit to the first N clauses, for a quick run")
    parser.add_argument("--dump-probabilities", metavar="PATH",
                        help="write every probability to PATH as a regression lock")
    args = parser.parse_args()

    question_set = "variant" if args.variant else "default"

    print("=" * 78)
    print("Laya clause-risk taxonomy validation")
    print("=" * 78)
    print("WARNING: the golden set's expected labels were written by the author of")
    print("this system and have not been independently reviewed. Agreement with")
    print("them measures consistency with one reader's opinion, not accuracy.")
    print()

    validate_go_taxonomy()

    clauses = load_golden()
    if args.clauses:
        clauses = clauses[: args.clauses]
    print(f"clauses: {len(clauses)}   question set: {question_set}")
    print(f"thresholds: detect >= {DETECT_THRESHOLD}  clear <= {CLEAR_THRESHOLD}")
    print()

    print("loading checkpoint...")
    router, model, load_seconds = build_adapter(args.device, args.model)
    print(f"model load: {load_seconds:.2f}s  device: {args.device or 'auto'}  "
          f"batch size: {args.batch_size}")
    print()

    states = [c["clause"] for c in clauses]
    started = time.monotonic()
    results = run_batch(router, model, question_set, states, args.batch_size)
    elapsed = time.monotonic() - started

    # Per-clause table.
    header = f"{'id':<32}" + "".join(f"{cid[:11]:>12}" for cid in CATEGORY_IDS)
    print(header)
    print("-" * len(header))

    detections: dict[str, dict[str, int]] = {
        cid: {"expected_hit": 0, "expected_total": 0,
              "absent_violated": 0, "absent_total": 0}
        for cid in CATEGORY_IDS
    }
    dump = {"model": model, "question_set": question_set,
            "detect_threshold": DETECT_THRESHOLD, "clear_threshold": CLEAR_THRESHOLD,
            "load_seconds": round(load_seconds, 3), "clauses": {}}

    for clause, result in zip(clauses, results):
        answers = result.get("answers", {})
        probabilities = {}
        for cid in CATEGORY_IDS:
            entry = answers.get(cid)
            if not isinstance(entry, dict):
                raise SystemExit(
                    f"clause {clause['id']!r}: model returned no answer for {cid}. "
                    "The question schema and the response parser disagree."
                )
            probabilities[cid] = {
                "noul": entry.get("noul"),
                "confidence": entry.get("confidence"),
            }

        row = f"{clause['id']:<32}"
        for cid in CATEGORY_IDS:
            p = probabilities[cid]["noul"]
            row += f"{p:>12.3f}"
        print(row)

        for cid in CATEGORY_IDS:
            p = probabilities[cid]["noul"]
            if cid in clause.get("expect", []):
                detections[cid]["expected_total"] += 1
                if p >= DETECT_THRESHOLD:
                    detections[cid]["expected_hit"] += 1
            if cid in clause.get("expect_absent", []):
                detections[cid]["absent_total"] += 1
                if p > CLEAR_THRESHOLD:
                    detections[cid]["absent_violated"] += 1

        dump["clauses"][clause["id"]] = {
            "probabilities": {cid: probabilities[cid]["noul"] for cid in CATEGORY_IDS},
            "expect": clause.get("expect", []),
            "expect_absent": clause.get("expect_absent", []),
        }

    print()
    print("-" * 78)
    print(f"inference: {elapsed:.2f}s for {len(states)} clauses "
          f"({len(states) / elapsed:.2f} clauses/s, {elapsed / len(states) * 1000:.0f} ms/clause)")
    print()

    print("Per-category agreement at the shipped thresholds")
    print("-" * 78)
    print(f"{'category':<26}{'recall':>10}{'false pos':>12}")
    total_expected = total_hits = total_absent = total_violations = 0
    for cid in CATEGORY_IDS:
        d = detections[cid]
        recall = d["expected_hit"] / d["expected_total"] if d["expected_total"] else None
        false_pos = d["absent_violated"] / d["absent_total"] if d["absent_total"] else None
        total_expected += d["expected_total"]
        total_hits += d["expected_hit"]
        total_absent += d["absent_total"]
        total_violations += d["absent_violated"]
        recall_text = f"{recall:.2f}" if recall is not None else "n/a"
        false_text = f"{false_pos:.2f}" if false_pos is not None else "n/a"
        print(f"{cid:<26}{recall_text:>10}{false_text:>12}"
              f"   ({d['expected_hit']}/{d['expected_total']} fired, "
              f"{d['absent_violated']}/{d['absent_total']} absent-clauses leaked)")

    print()
    print(f"overall: {total_hits}/{total_expected} expected detections fired; "
          f"{total_violations}/{total_absent} expected-absent clauses were not clear")
    print()

    # Distribution, which is what threshold selection actually needs.
    all_probs = [dump["clauses"][c["id"]]["probabilities"][cid]
                 for c in clauses for cid in CATEGORY_IDS]
    in_band = sum(1 for p in all_probs if CLEAR_THRESHOLD < p < DETECT_THRESHOLD)
    print(f"probability distribution over {len(all_probs)} answers:")
    print(f"  clear  (<= {CLEAR_THRESHOLD}): {sum(1 for p in all_probs if p <= CLEAR_THRESHOLD)}")
    print(f"  band   ({CLEAR_THRESHOLD}..{DETECT_THRESHOLD}): {in_band}"
          "   <- each of these sets needs_review")
    print(f"  detect (>= {DETECT_THRESHOLD}): {sum(1 for p in all_probs if p >= DETECT_THRESHOLD)}")
    print()

    if args.dump_probabilities:
        out = Path(args.dump_probabilities)
        out.write_text(json.dumps(dump, indent=2) + "\n", encoding="utf-8")
        print(f"probabilities written to {out}")

    print("Next step: pick DETECT and CLEAR so that the band is small and recall on")
    print("the expected detections is high. Do that from this table, not from taste,")
    print("and record the choice in backend/config.go with the date and the reason.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
