import { createFileRoute, Link } from "@tanstack/react-router";
import { useEffect, useMemo, useState } from "react";
import { loadAnalysis } from "@/lib/analysis";
import {
  ChevronDown,
  FileSearch,
  RotateCcw,
  ThumbsDown,
  ThumbsUp,
  TriangleAlert,
} from "lucide-react";
import {
  detectedCount,
  formatDuration,
  reviewableIssues,
  skipReasonLabel,
  toRisk,
  type ApiResult,
  type StoredAnalysis,
} from "@/lib/risk";
import { RiskBadge, type Risk } from "@/components/risk-badge";
import { ClauseFindings } from "@/components/clause-findings";
import { RiskMeter } from "@/components/risk-meter";
import { SiteHeader } from "@/components/site-header";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/results")({
  head: () => ({
    meta: [
      { title: "Contract risk report — Clause" },
      {
        name: "description",
        content: "Review clause-level contract risks, per-category findings, and overrides.",
      },
      { property: "og:title", content: "Contract risk report — Clause" },
      {
        property: "og:description",
        content: "Review clause-level contract risks, per-category findings, and overrides.",
      },
      { property: "og:type", content: "website" },
      { name: "twitter:card", content: "summary_large_image" },
    ],
  }),
  component: ResultsPage,
});

type Finding = ApiResult & { id: number; risk: Risk; override?: Risk };

function toFindings(results: ApiResult[]): Finding[] {
  return results.map((result, index) => ({
    ...result,
    id: index + 1,
    clause_index: result.clause_index ?? index,
    risk_score: Number(result.risk_score) || 0,
    confidence: Number(result.confidence) || 0,
    risk: toRisk(result.risk_level),
  }));
}

type Filter = "All" | "High" | "Critical" | "Flagged";

function ResultsPage() {
  const [findings, setFindings] = useState<Finding[]>([]);
  const [fileName, setFileName] = useState("");
  const [stored, setStored] = useState<StoredAnalysis | null>(null);
  const [ready, setReady] = useState(false);
  const [filter, setFilter] = useState<Filter>("All");
  const [expanded, setExpanded] = useState<number | null>(null);
  const [feedback, setFeedback] = useState<"up" | "down" | null>(null);

  useEffect(() => {
    const data = loadAnalysis();
    if (data) {
      setFindings(toFindings(data.results));
      setFileName(data.fileName);
      setStored(data);
    }
    setReady(true);
  }, []);

  const filtered = useMemo(
    () =>
      findings.filter((item) => {
        if (filter === "All") return true;
        if (filter === "Flagged") return detectedCount(item) > 0;
        return item.risk === filter;
      }),
    [filter, findings],
  );

  const counts = {
    All: findings.length,
    High: findings.filter((i) => i.risk === "High").length,
    Critical: findings.filter((i) => i.risk === "Critical").length,
    Flagged: findings.filter((i) => detectedCount(i) > 0).length,
  } as const;

  const flagged = counts.Flagged;
  const uncertain = findings.filter((i) => i.needs_review).length;
  const meta = stored?.meta;

  function updateRisk(id: number, risk: Risk) {
    setFindings((current) =>
      current.map((item) => (item.id === id ? { ...item, risk, override: risk } : item)),
    );
  }

  if (ready && findings.length === 0) {
    return (
      <div className="min-h-screen bg-background">
        <SiteHeader />
        <main className="mx-auto max-w-3xl px-5 py-20 sm:px-8">
          <h1 className="font-display text-5xl sm:text-6xl">No report yet</h1>
          <p className="mt-4 text-lg text-muted-foreground">
            Upload a contract to see its clause-level risk report.
          </p>
          <Button asChild size="lg" className="mt-8">
            <Link to="/upload">Analyze a contract</Link>
          </Button>
        </main>
      </div>
    );
  }

  return (
    <div className="min-h-screen bg-background">
      <SiteHeader />
      <main className="mx-auto max-w-7xl px-5 py-10 sm:px-8 sm:py-14">
        <div className="flex flex-col gap-8 border-b border-foreground pb-8 lg:flex-row lg:items-end lg:justify-between">
          <div>
            <p className="flex items-center gap-2 text-sm font-medium text-risk-low-foreground">
              <FileSearch className="size-4" /> Analysis complete
            </p>
            <h1 className="mt-3 font-display text-5xl sm:text-6xl">Contract risk report</h1>
            <p className="mt-3 text-sm text-muted-foreground">
              {fileName} · {findings.length} clauses reviewed
              {meta ? ` · analysed in ${formatDuration(meta.duration_ms)}` : ""}
            </p>
          </div>
          <Button asChild variant="outline">
            <Link to="/upload">
              <RotateCcw /> Analyze another contract
            </Link>
          </Button>
        </div>

        {(stored?.warnings?.length ?? 0) > 0 ||
        (stored?.skipped?.length ?? 0) > 0 ||
        (meta?.partial ?? false) ? (
          <div className="mt-6 border border-border bg-card p-4 text-sm" role="status">
            <p className="flex items-start gap-2">
              <TriangleAlert className="mt-0.5 size-4 shrink-0 text-risk-medium-foreground" />
              <span>
                {meta?.partial ? "Part of this contract was not analysed. " : ""}
                {(stored?.skipped?.length ?? 0) > 0
                  ? `${stored?.skipped?.length} clause${stored?.skipped?.length === 1 ? " was" : "s were"} skipped: ${[
                      ...new Set(stored!.skipped!.map((s) => skipReasonLabel(s.reason))),
                    ].join(", ")}. `
                  : ""}
                {stored?.warnings?.join(" ")}
              </span>
            </p>
          </div>
        ) : null}

        <div className="grid gap-3 border-b border-border py-6 sm:grid-cols-4">
          <div>
            <p className="text-3xl font-semibold">{findings.length}</p>
            <p className="text-sm text-muted-foreground">Clauses found</p>
          </div>
          <div>
            <p className="text-3xl font-semibold">{flagged}</p>
            <p className="text-sm text-muted-foreground">Clauses flagged</p>
          </div>
          <div>
            <p className="text-3xl font-semibold">
              {findings.filter((i) => i.risk === "High" || i.risk === "Critical").length}
            </p>
            <p className="text-sm text-muted-foreground">Need attention</p>
          </div>
          <div>
            <p className="text-3xl font-semibold">{uncertain}</p>
            <p className="text-sm text-muted-foreground">Need reading</p>
          </div>
        </div>

        <div className="border-b border-border py-6">
          <RiskMeter
            score={
              findings.length ? findings.reduce((s, f) => s + f.risk_score, 0) / findings.length : 0
            }
            level={worstLevel(findings)}
            calibration={meta?.calibration}
            className="max-w-lg"
          />
          <p className="mt-2 max-w-lg text-xs leading-4 text-muted-foreground">
            The mean of the per-clause scores. It is not the risk of the contract as a whole, which
            is not something a weighted index can express.
          </p>
        </div>

        <div className="flex flex-col gap-5 py-8 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <h2 className="text-xl font-semibold">Clause findings</h2>
            <p className="mt-1 text-sm text-muted-foreground">
              Expand a clause to see what was flagged and why.
            </p>
          </div>
          <div
            className="inline-flex w-fit rounded-md border border-border bg-card p-1"
            aria-label="Risk filters"
          >
            {(["All", "Flagged", "High", "Critical"] as Filter[]).map((item) => (
              <Button
                key={item}
                size="sm"
                variant={filter === item ? "default" : "ghost"}
                onClick={() => setFilter(item)}
              >
                {item} <span className="opacity-60">{counts[item]}</span>
              </Button>
            ))}
          </div>
        </div>

        <div className="overflow-hidden border-y border-border bg-card">
          <div className="hidden grid-cols-[minmax(260px,2fr)_110px_110px_minmax(220px,1.2fr)_30px] gap-5 border-b border-border bg-secondary px-5 py-3 text-xs font-semibold text-muted-foreground lg:grid">
            <span>Clause text</span>
            <span>Risk level</span>
            <span>Findings</span>
            <span>Score</span>
            <span />
          </div>

          {filtered.map((item) => {
            const isOpen = expanded === item.id;
            const issues = reviewableIssues(item);
            const flaggedHere = detectedCount(item);
            return (
              <div key={item.id} className="border-b border-border last:border-b-0">
                <button
                  onClick={() => setExpanded(isOpen ? null : item.id)}
                  aria-expanded={isOpen}
                  className="grid w-full cursor-pointer gap-4 px-5 py-5 text-left transition-colors hover:bg-secondary/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring lg:grid-cols-[minmax(260px,2fr)_110px_110px_minmax(220px,1.2fr)_30px] lg:items-center lg:gap-5"
                >
                  <div className="min-w-0">
                    <p className="font-semibold">Clause {item.id}</p>
                    <p className="mt-1 line-clamp-2 text-sm leading-relaxed text-muted-foreground">
                      {item.clause}
                    </p>
                  </div>
                  <div className="flex items-center justify-between lg:block">
                    <span className="text-xs text-muted-foreground lg:hidden">Risk</span>
                    <RiskBadge risk={item.risk} />
                  </div>
                  <div className="flex items-center justify-between lg:block">
                    <span className="text-xs text-muted-foreground lg:hidden">Findings</span>
                    <span className="text-sm font-medium">
                      {flaggedHere > 0 ? (
                        <span className="text-risk-high-foreground">{flaggedHere} flagged</span>
                      ) : issues.length > 0 ? (
                        <span className="text-muted-foreground">{issues.length} uncertain</span>
                      ) : (
                        <span className="text-muted-foreground">none</span>
                      )}
                    </span>
                  </div>
                  <div className="hidden text-sm leading-relaxed text-muted-foreground lg:block">
                    <span className="font-mono tabular-nums">{item.risk_score.toFixed(2)}</span>
                    {item.needs_review ? (
                      <span className="ml-2 text-xs text-risk-medium-foreground">read this</span>
                    ) : null}
                  </div>
                  <ChevronDown
                    className={cn("size-4 transition-transform", isOpen && "rotate-180")}
                  />
                </button>

                {isOpen ? (
                  <div className="grid gap-6 border-t border-border bg-secondary/45 px-5 py-6 lg:grid-cols-[2fr_1.4fr]">
                    <div>
                      <p className="mb-2 text-xs font-semibold uppercase text-muted-foreground">
                        Full clause
                      </p>
                      <p className="text-sm leading-7">{item.clause}</p>
                    </div>
                    <div>
                      <p className="mb-3 text-xs font-semibold uppercase text-muted-foreground">
                        What was flagged
                      </p>
                      {item.explanation ? (
                        <p className="mb-4 text-sm leading-6 text-muted-foreground">
                          {item.explanation}
                        </p>
                      ) : null}
                      <ClauseFindings result={item} />
                      <div className="mt-5 max-w-52">
                        <label
                          className="mb-2 block text-xs font-semibold"
                          htmlFor={`risk-${item.id}`}
                        >
                          Override risk level
                        </label>
                        <Select
                          value={item.risk}
                          onValueChange={(value) => updateRisk(item.id, value as Risk)}
                        >
                          <SelectTrigger id={`risk-${item.id}`}>
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            {(["Low", "Medium", "High", "Critical"] as Risk[]).map((risk) => (
                              <SelectItem key={risk} value={risk}>
                                {risk}
                              </SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                        {item.override ? (
                          <p className="mt-1.5 text-xs text-muted-foreground">
                            Overridden from {item.risk_level}. Not saved anywhere yet.
                          </p>
                        ) : null}
                      </div>
                    </div>
                  </div>
                ) : null}
              </div>
            );
          })}
        </div>

        <div className="flex flex-col items-center justify-between gap-6 py-10 sm:flex-row">
          <div className="flex items-center gap-3">
            <span className="text-sm font-medium">Was this useful?</span>
            <Button
              variant={feedback === "up" ? "default" : "outline"}
              size="icon"
              aria-label="Yes, this was useful"
              onClick={() => setFeedback("up")}
            >
              <ThumbsUp />
            </Button>
            <Button
              variant={feedback === "down" ? "default" : "outline"}
              size="icon"
              aria-label="No, this was not useful"
              onClick={() => setFeedback("down")}
            >
              <ThumbsDown />
            </Button>
            {feedback ? <span className="text-sm text-muted-foreground">Thanks.</span> : null}
          </div>
          <p className="text-xs text-muted-foreground">
            Automated triage, not legal advice. Verify anything you rely on.
            {meta?.calibration === "provisional-unvalidated"
              ? " Category weights are not yet validated against reviewed contracts."
              : ""}
          </p>
        </div>
      </main>
    </div>
  );
}

function worstLevel(findings: Finding[]): string {
  const order = ["low", "medium", "high", "critical"];
  let worst = "low";
  for (const item of findings) {
    if (order.indexOf(item.risk.toLowerCase()) > order.indexOf(worst)) {
      worst = item.risk.toLowerCase();
    }
  }
  return worst;
}
