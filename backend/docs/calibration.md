# Calibration status: the weights and thresholds are provisional

**The numbers in `config.go` are unvalidated engineering defaults.** They were
chosen so the aggregation is monotone, bounded and explainable. They were *not*
derived from reviewed contracts, because no such corpus exists in this
repository.

Every API response says so:

```json
"meta": { "calibration": "provisional-unvalidated" }
```

The frontend surfaces it too, in the risk meter caption and the results
footer. If that string ever changes to `validated`, it must be because someone
reviewed a labelled set and re-derived the numbers, not because the defaults
happened to look reasonable.

## How to re-derive them

```powershell
cd backend
.venv-laya\Scripts\python.exe scripts\validate_taxonomy.py \
  --dump-probabilities testdata\probabilities_snapshot.json
```

The script prints per-clause probabilities, per-category recall and
false-positive rate, and the distribution across the detect/clear bands. Choose
`RISK_DETECT_THRESHOLD`, `RISK_CLEAR_THRESHOLD` and the group weights from that
table, then record the choice and its date here.

## The golden set is not ground truth

`testdata/clauses_golden.json` holds 31 clauses with expected categories. Those
labels were written by the author of this system and have **not** been reviewed
by anyone else. Agreement with them measures consistency with one reader's
opinion of what a clause means — it is an upper bound on accuracy, not a
validation of it.

Two labels in it were already found wrong by the first validator run and
corrected: `liability-cap-asymmetric` and `liability-multiple-caps` both
originally asserted that the clause contained *no* limitation of liability,
while the clause text says "shall not exceed" outright. The model was right and
the labels were wrong. Both entries carry a note saying so.

Before these numbers are quoted to anyone as evidence the system works, every
label needs review by a qualified lawyer who did not build this, and the set
needs to grow well past 31 clauses.

## Measured per-category reliability

At the shipped thresholds (detect ≥ 0.60, clear ≤ 0.40), over 31 clauses:

| Category | Recall | False positives | Reading |
|---|---|---|---|
| `auto_renewal` | 1.00 | 0.09 | usable |
| `jurisdiction_related` | 1.00 | n/a | usable |
| `uncapped_liability` | 1.00 | 0.33 | usable, watch leakage |
| `indemnification` | 1.00 | 0.38 | usable, watch leakage |
| `limitation_of_liability` | 0.62 | 0.33 | weak |
| `termination_right` | 1.00 | 0.44 | **weak: fires too broadly** |
| `unilateral_termination` | 1.00 | 0.47 | **weak: fires too broadly** |
| `one_sided_indemnity` | 0.33 | 0.00 | **weak: barely fires at all** |
| `unusual_obligation` | 0.25 | 0.00 | **weak: barely fires at all** |

Overall: 30 of 41 expected detections fired; 22 of 64 expected-absent clauses
were not clear.

These are carried in the taxonomy as `Category.Reliability` and returned on
every issue as `reliability`, so the UI can label a weak category rather than
present it as though it were as trustworthy as a measured one.

## What the measurements actually say

**Four of the nine categories are not fit for purpose as written.**

- `unilateral_termination` and `termination_right` overlap heavily. The model
  reads any termination language as "unilateral", scoring 0.55 on a mutual
  termination for unremedied material breach and 0.59 on an asymmetric
  notice-period clause. They cannot be separated by these two questions.
- `one_sided_indemnity` scores **0.047** on a textbook one-sided indemnity with
  no reciprocal obligation, and 0.137 on "indemnify the Supplier at its sole
  expense". The question is close to asking the model whether the clause *says*
  it is one-sided.
- `unusual_obligation` fires on 2 of 8 clauses where it should, including
  scoring 0.125 on a best-endeavours-with-no-charge obligation.

This is a **question-design problem, not a threshold problem.** Raising the
detect threshold would improve the false-positive rates and destroy the recall
on the same categories at the same time. The next substantive change should be
to the instruction wording in `laya_questions.py`, re-validated against a
reviewed set — not to the weights.

`scripts/validate_taxonomy.py --variant` exists for exactly this: it runs a
schema using descriptive criteria instead of A/B labels so the two can be
compared. It is not the default, because changing the rendered option text
changes the model's calibration and any figure measured under one form does not
transfer to the other.

## The severity floor

The default aggregator is `noisy-or-floored-v1`. The floor was not a tuning
choice; it fixed a provable defect found by running the real pipeline:

> A textbook unilateral-termination clause ("may terminate at any time and
> without notice, in its sole discretion") scored **0.15 and was reported "low"**.

Under un-floored absolute noisy-OR, a lone group's score is capped at that
group's weight. With weights summing to 1.0 across six groups, that makes four
of the nine categories *mathematically incapable* of being reported above "low"
on their own, and caps a certain uncapped liability at 0.30 (medium). The
dilution cannot be tuned away because raising a weight lowers the cap on
everything else.

The floor rescales the strongest contributing group by the largest weight, so
severity ordering is preserved:

| Category alone, at certainty | Score | Level |
|---|---|---|
| `uncapped_liability` | 1.00 | critical |
| `one_sided_indemnity` | 0.83 | critical |
| `unilateral_termination` | 0.67 | high |
| `auto_renewal` | 0.33 | medium |
| `jurisdiction_related` | 0.17 | low |

A jurisdiction-only clause staying "low" is intentional: it carries the lowest
weight because a domestic governing-law clause is close to boilerplate. The
floor removes the dilution without flattening the taxonomy.

Verified end to end against the running stack after the change:

| Clause | Before | After |
|---|---|---|
| unilateral termination at any time without notice | low, 0.15 | **high, 0.50** |
| indemnity + uncapped liability | medium, 0.31 | **high, 0.59** |
| liability cap (protective) | low, 0.06 | low, 0.06 |

`noisy-or-v1` and `weighted-sum-v1` remain selectable via `RISK_AGGREGATOR` so
all three can be compared on a reviewed set.

## A note on the model's own calibration

The checkpoint ships a temperature of 0.1006 for `choice:11+`, outside the
accepted [0.5, 5] range, and the SDK clamps it to 0.5 and warns that confidence
from those entries is uncalibrated.

**This does not affect us.** All nine questions are `noul`, whose temperature is
1.98 and therefore in range. The warning fires on checkpoint load regardless, and
is safe to ignore for this question set.

## What a reviewed calibration would still not fix

Even with perfect thresholds, four of the categories answer the wrong question.
The measurements above are the reason the results page labels a finding
"less reliable" rather than presenting all nine categories as equally
trustworthy, and the reason the aggregation is deterministic Go code a reviewer
can read rather than learned weights nobody can inspect.
