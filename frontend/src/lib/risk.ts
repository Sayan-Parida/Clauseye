import type { Risk } from "@/components/risk-badge";

/**
 * Types for analyze schema v2.
 *
 * Two fields exist to prevent a specific misreading, and the UI depends on both
 * of them.
 *
 * The model answers each category with a probability of the risk applying
 * (P(true)) and separately with its confidence in that answer, which for a
 * binary question is max(p, 1-p). A category the system did *not* flag therefore
 * still carries a high confidence. Rendering that confidence next to a finding
 * with `detected: false` would tell a lawyer a clause is "94% risky" when the
 * system considers it clear. So:
 *
 *   - `probability` is always shown when discussing risk likelihood
 *   - `confidence` is labelled as model certainty, never as a risk percentage
 *
 * The same applies to `risk_score`: it is a weighted aggregation output, not a
 * probability, and is rendered as a bar rather than as a percentage.
 */

export const SCHEMA_VERSION = 2;

export type ClauseState = "detected" | "uncertain" | "clear";
export type Polarity = "risk" | "protective" | "context";
export type Reliability = "measured" | "weak";

export type Issue = {
  category: string;
  label: string;
  polarity: Polarity;
  reliability: Reliability;
  state: ClauseState;
  detected: boolean;
  probability: number;
  confidence: number;
  weight: number;
  group_risk: number;
  contributes: boolean;
  explanation: string;
  evidence?: string;
  evidence_label?: string;
  evidence_found: boolean;
};

export type SkippedClause = { clause_index: number; reason: string; detail?: string };

export type ApiResult = {
  clause: string;
  clause_index: number;
  risk_level: string;
  risk_score: number;
  confidence: number;
  needs_review: boolean;
  explanation: string;
  reason: string;
  issues: Issue[];
  location?: { clause_index: number };
};

export type ApiMeta = {
  engine: string;
  model?: string;
  aggregator: string;
  calibration: string;
  clauses_received: number;
  clauses_analyzed: number;
  clauses_skipped: number;
  windows_analyzed: number;
  duration_ms: number;
  partial: boolean;
};

export type ApiWarning = { code: string; message: string };

export type AnalyzeResponse = {
  schema_version: number;
  results: ApiResult[];
  skipped: SkippedClause[];
  meta: ApiMeta;
  warnings?: string[];
};

/** Schema v1 shape, still accepted so a stale cached report renders. */
export type LegacyApiResult = {
  clause: string;
  risk_level: string;
  confidence: number;
  reason: string;
};

export type StoredAnalysis = {
  fileName: string;
  analyzedAt: number;
  results: ApiResult[];
  // Optional fields are typed `| undefined` because tsconfig enables
  // exactOptionalPropertyTypes, which distinguishes "absent" from "present and
  // undefined". Passing an optional value straight through therefore needs the
  // type to admit undefined explicitly.
  skipped?: SkippedClause[] | undefined;
  meta?: ApiMeta | undefined;
  warnings?: string[] | undefined;
};

/** Map an API risk level onto the badge type. Anything unrecognised is low. */
export function toRisk(level: string | undefined): Risk {
  switch (level?.toLowerCase()) {
    case "critical":
      return "Critical";
    case "high":
      return "High";
    case "medium":
      return "Medium";
    default:
      return "Low";
  }
}

/**
 * Findings a reviewer should look at: detected first, then uncertain.
 *
 * `uncertain` is included deliberately. A category the model could not decide is
 * information, not noise, and hiding it would recreate the false confidence the
 * backend's hysteresis band exists to avoid.
 */
export function reviewableIssues(result: ApiResult): Issue[] {
  const issues = Array.isArray(result.issues) ? result.issues : [];
  return issues
    .filter((issue) => issue.state !== "clear")
    .sort((a, b) => {
      if (a.state !== b.state) return a.state === "detected" ? -1 : 1;
      return b.probability - a.probability;
    });
}

export function detectedCount(result: ApiResult): number {
  return reviewableIssues(result).filter((issue) => issue.detected).length;
}

/** Format a 0..1 probability as a whole percentage. */
export function percent(value: number): string {
  if (!Number.isFinite(value)) return "—";
  return `${Math.round(Math.max(0, Math.min(1, value)) * 100)}%`;
}

/**
 * Render a confidence value.
 *
 * Always labelled, never presented bare. See the note at the top of the file.
 */
export function formatConfidence(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return "not reported";
  return `${Math.round(Math.min(1, value) * 100)}% model certainty`;
}

export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return "—";
  if (ms < 1000) return `${ms} ms`;
  const seconds = ms / 1000;
  if (seconds < 60) return `${seconds.toFixed(1)} s`;
  return `${Math.floor(seconds / 60)} min ${Math.round(seconds % 60)} s`;
}

/** Human label for a skip reason. */
export function skipReasonLabel(reason: string): string {
  switch (reason) {
    case "too_short":
      return "too short to assess";
    case "no_contract_signal":
      return "no contractual language";
    default:
      return reason.replace(/_/g, " ");
  }
}
