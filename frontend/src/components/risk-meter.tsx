import { toRisk } from "@/lib/risk";

/**
 * Overall risk score meter.
 *
 * Renders the score as a bar with a fraction, never as a bare percentage.
 *
 * `risk_score` is an aggregation output: a noisy-OR over weighted, guarded group
 * risks. It is bounded to [0,1] but it is not a probability, so "78%" would be
 * a false claim about the chance that a clause is risky. The bar position is
 * honest about magnitude; the label says what the number is.
 */
export function RiskMeter({
  score,
  level,
  calibration,
  className,
}: {
  score: number;
  level: string;
  calibration?: string | undefined;
  className?: string | undefined;
}) {
  const pct = Math.max(0, Math.min(1, Number.isFinite(score) ? score : 0)) * 100;
  const risk = toRisk(level);

  return (
    <div className={className}>
      <div className="flex items-baseline justify-between gap-3">
        <span className="text-sm font-semibold">Overall risk score</span>
        <span className="font-mono text-sm tabular-nums">{score.toFixed(2)}</span>
      </div>
      <div
        className="mt-2 h-1.5 w-full bg-secondary"
        role="meter"
        aria-valuenow={Number(score.toFixed(2))}
        aria-valuemin={0}
        aria-valuemax={1}
        aria-label={`Overall risk score ${score.toFixed(2)} out of 1, level ${risk}`}
      >
        <div
          className={`h-full transition-[width] duration-500 ${levelColor(risk)}`}
          style={{ width: `${pct}%` }}
        />
      </div>
      <p className="mt-1.5 text-[0.6875rem] leading-4 text-muted-foreground">
        A weighted index, not a probability.
        {calibration === "provisional-unvalidated"
          ? " Weights are not yet validated against reviewed contracts."
          : ""}
      </p>
    </div>
  );
}

function levelColor(risk: ReturnType<typeof toRisk>): string {
  switch (risk) {
    case "Critical":
      return "bg-risk-critical";
    case "High":
      return "bg-risk-high";
    case "Medium":
      return "bg-risk-medium";
    default:
      return "bg-risk-low";
  }
}
