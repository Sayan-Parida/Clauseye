import { AlertTriangle, Quote, Sparkles } from "lucide-react";
import { formatConfidence, percent, type ApiResult, type Issue } from "@/lib/risk";
import { cn } from "@/lib/utils";

/**
 * The per-clause findings panel.
 *
 * Design rules this component follows, each of which exists because of a
 * specific failure mode:
 *
 *  - Probability is shown as a probability. Confidence is shown separately and
 *    labelled "model certainty", because the model reports confidence as
 *    max(p, 1-p): a category the system considers *clear* can carry 94%
 *    certainty. Showing that next to a non-finding would read as "94% risky".
 *
 *  - Evidence is quoted verbatim from the clause. It is extracted by a
 *    deterministic pattern match on the server, never generated, so it is
 *    either an exact substring of the user's own contract or absent.
 *
 *  - When no evidence matched, that is stated rather than hidden. A finding
 *    resting on the model alone is weaker than one corroborated by the clause
 *    text, and the reader is entitled to know which they are looking at.
 *
 *  - A category measured as unreliable against the golden set is labelled. Two
 *    of the nine currently are, and presenting them identically to the ones
 *    that separate cleanly would overstate what the system knows.
 */
export function ClauseFindings({ result }: { result: ApiResult }) {
  const issues = (result.issues ?? []).filter((issue) => issue.state !== "clear");

  if (issues.length === 0) {
    return (
      <p className="text-sm leading-6 text-muted-foreground">
        No risk category was detected above the review threshold for this clause.
      </p>
    );
  }

  return (
    <ul className="space-y-5">
      {issues.map((issue) => (
        <li key={issue.category} className="border-l-2 border-border pl-4">
          <FindingHeader issue={issue} />
          {issue.explanation ? (
            <p className="mt-2 text-sm leading-6 text-foreground">{issue.explanation}</p>
          ) : null}
          {issue.evidence_found && issue.evidence ? (
            <Evidence evidence={issue.evidence} />
          ) : (
            <Uncorroborated />
          )}
        </li>
      ))}
      {result.needs_review ? <NeedsReview /> : null}
    </ul>
  );
}

function FindingHeader({ issue }: { issue: Issue }) {
  const detected = issue.state === "detected";
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
      <span className="text-sm font-semibold">{issue.label}</span>
      <StateChip issue={issue} />
      <span
        className="text-xs font-medium text-muted-foreground"
        title="How likely the model considers this category to apply to this clause."
      >
        {percent(issue.probability)} likely
      </span>
      <span
        className="text-xs text-muted-foreground"
        title="The model's certainty in that answer, not risk likelihood."
      >
        {formatConfidence(issue.confidence)}
      </span>
      {issue.reliability === "weak" ? (
        <span
          className="inline-flex items-center gap-1 rounded-full bg-risk-medium px-2 py-0.5 text-[0.6875rem] font-semibold text-risk-medium-foreground"
          title="This category fired on clauses where it should have been clear, or rarely fired where it should have, when measured against the validation set. Treat it as a prompt to look, not a conclusion."
        >
          <AlertTriangle className="size-3" />
          less reliable
        </span>
      ) : null}
      {issue.polarity === "protective" ? (
        <span className="inline-flex items-center gap-1 rounded-full bg-risk-low px-2 py-0.5 text-[0.6875rem] font-semibold text-risk-low-foreground">
          protective term
        </span>
      ) : null}
      {!detected ? <span className="text-xs text-muted-foreground">not confirmed</span> : null}
    </div>
  );
}

function StateChip({ issue }: { issue: Issue }) {
  const base =
    "inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[0.6875rem] font-semibold";
  if (issue.state === "detected") {
    return <span className={cn(base, "bg-risk-high text-risk-high-foreground")}>flagged</span>;
  }
  return (
    <span
      className={cn(base, "bg-secondary text-muted-foreground")}
      title="The model could not decide this category. It sits between the clear and detect thresholds."
    >
      uncertain
    </span>
  );
}

function Evidence({ evidence }: { evidence: string }) {
  return (
    <figure className="mt-3 border-l-2 border-accent bg-secondary/45 px-3 py-2">
      <figcaption className="mb-1 flex items-center gap-1.5 text-[0.6875rem] font-semibold uppercase tracking-wide text-muted-foreground">
        <Quote className="size-3" />
        From your contract
      </figcaption>
      <blockquote className="text-sm leading-6 text-foreground">“{evidence}”</blockquote>
    </figure>
  );
}

function Uncorroborated() {
  return (
    <p className="mt-3 text-xs leading-5 text-muted-foreground">
      No matching language was found in this clause to corroborate the flag, so this finding rests
      on the model alone.
    </p>
  );
}

function NeedsReview() {
  return (
    <p className="flex items-start gap-2 text-xs leading-5 text-muted-foreground">
      <Sparkles className="mt-0.5 size-3 shrink-0" />
      At least one category was in the uncertain band, or the model was not confident enough. Read
      this clause rather than relying on the score.
    </p>
  );
}
