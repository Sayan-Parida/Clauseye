"""Clause risk question schema for the Laya inference service.

This module is the single source of truth for the nine risk questions. It is
imported by the service (which sends them to the model) and by
scripts/validate_taxonomy.py (which measures how well the model answers them),
so the two cannot drift apart.

The Go side owns the corresponding taxonomy in backend/taxonomy.go. The two
lists must stay in the same order and use the same identifiers; validate_go_taxonomy()
in scripts/validate_taxonomy.py checks that and fails loudly if they diverge.

A note on the schema shape, because it is a deliberate choice.

Each question is a ``noul``: a binary yes/no decision expressed as a
probability. ``noul`` is short for "no/ul" in the checkpoint's question-type
vocabulary, alongside ``choice`` and ``score``. It is not "N/A".

The questions are deliberately written as bare yes/no statements with no
criteria and only A/B labels. That renders, in the checkpoint's input format, as:

    noul question: Does this clause contain a limitation of liability?
    [SEP] A: no, the statement does not hold [SEP] <clause> [SEP]

Two reasons for that rather than descriptive criteria:

1. The checkpoint's calibration is a function of the exact input format it was
   trained on. Adding descriptive criteria changes the rendered options, which
   moves the probabilities and invalidates any calibration measured against the
   current form. That experiment is worth running, but it belongs behind a flag
   and a re-calibration, not as an unremarked change.

2. Laya cannot generate prose, so all of the semantic weight of the question
   has to live in the instruction text. The explanations shown to a user are
   composed separately and deterministically in Go from the taxonomy's static
   rationale plus a verbatim quote from the clause.
"""

from __future__ import annotations

from typing import Any

# The nine categories, in taxonomy order. The Go taxonomy uses the same order.
CATEGORY_IDS: tuple[str, ...] = (
    "limitation_of_liability",
    "uncapped_liability",
    "indemnification",
    "one_sided_indemnity",
    "termination_right",
    "unilateral_termination",
    "auto_renewal",
    "jurisdiction_related",
    "unusual_obligation",
)

# Question id -> the instruction text sent to the model.
#
# These instructions are the only place the risk semantics are expressed to the
# model, so they are written as unambiguous yes/no statements about the clause
# in front of them.
_INSTRUCTIONS: dict[str, str] = {
    "limitation_of_liability": "Does this clause contain a limitation of liability?",
    "uncapped_liability": "Is liability uncapped or unlimited in this clause?",
    "indemnification": "Does this clause contain an indemnification obligation?",
    "one_sided_indemnity": "Is the indemnification obligation one-sided?",
    "termination_right": "Does this clause create a termination right?",
    "unilateral_termination": "Is termination unilateral in this clause?",
    "auto_renewal": "Does this clause contain automatic renewal?",
    "jurisdiction_related": "Does this clause concern jurisdiction or governing law?",
    "unusual_obligation": "Does this clause create an unusual or one-sided obligation?",
}

# A/B labels. These control only the text the model sees in place of the words
# true/false. The keys retain their boolean meaning and the returned `noul`
# value is always P(true); the label never inverts the answer.
_LABELS: dict[str, str] = {"false": "A", "true": "B"}


def legal_questions() -> dict[str, dict[str, Any]]:
    """Return the nine-question schema sent with every analysis window."""
    return {
        qid: {
            "type": "noul",
            "instructions": _INSTRUCTIONS[qid],
            "labels": dict(_LABELS),
        }
        for qid in CATEGORY_IDS
    }


def variant_questions() -> dict[str, dict[str, Any]]:
    """A schema using descriptive criteria instead of A/B labels.

    Not enabled by default. Provided so the calibration script can measure
    whether the descriptive form changes the model's answers, because if it does
    then the two forms are not interchangeable and the choice of one is a
    modelling decision that has to be justified with measurements.
    """
    criteria: dict[str, dict[str, str]] = {
        "limitation_of_liability": {
            "false": "no monetary cap on liability and no exclusion of indirect loss",
            "true": "liability is capped, or indirect and consequential loss is excluded",
        },
        "uncapped_liability": {
            "false": "liability is capped, limited, or excluded",
            "true": "liability is unlimited, uncapped, or without limitation as to amount",
        },
        "indemnification": {
            "false": "no obligation to cover the other party's third-party losses",
            "true": "an obligation to indemnify, defend, or hold harmless the other party",
        },
        "one_sided_indemnity": {
            "false": "any indemnity is reciprocal or mutual between the parties",
            "true": "one party indemnifies the other with no reciprocal obligation",
        },
        "termination_right": {
            "false": "no right to terminate the agreement is created",
            "true": "a right to terminate the agreement is created",
        },
        "unilateral_termination": {
            "false": "termination requires both parties, or a breach by the terminating party",
            "true": "one party may terminate on its own initiative, without cause or notice",
        },
        "auto_renewal": {
            "false": "the agreement does not renew automatically",
            "true": "the agreement renews automatically unless notice is given in time",
        },
        "jurisdiction_related": {
            "false": "the clause does not concern governing law or forum",
            "true": "the clause fixes governing law, forum, seat, or venue for disputes",
        },
        "unusual_obligation": {
            "false": "obligations are mutual and within normal market practice",
            "true": "an obligation is one-sided or outside normal market practice",
        },
    }
    return {
        qid: {
            "type": "noul",
            "instructions": _INSTRUCTIONS[qid],
            "criteria": dict(criteria[qid]),
        }
        for qid in CATEGORY_IDS
    }


QUESTION_SETS: dict[str, Any] = {
    "default": legal_questions,
    "variant": variant_questions,
}
