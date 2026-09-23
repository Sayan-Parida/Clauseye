import { createFileRoute, Link } from "@tanstack/react-router";
import { useEffect, useMemo, useState } from "react";
import { loadAnalysis, toRisk, type ApiResult } from "@/lib/analysis";
import { ChevronDown, FileSearch, RotateCcw, ThumbsDown, ThumbsUp } from "lucide-react";
import { RiskBadge, type Risk } from "@/components/risk-badge";
import { SiteHeader } from "@/components/site-header";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/results")({
  head: () => ({ meta: [
    { title: "Contract risk report — Clause" },
    { name: "description", content: "Review clause-level contract risks, confidence scores, reasons, and overrides." },
    { property: "og:title", content: "Contract risk report — Clause" },
    { property: "og:description", content: "Review clause-level contract risks, confidence scores, reasons, and overrides." },
    { property: "og:type", content: "website" },
    { name: "twitter:card", content: "summary_large_image" },
  ]}),
  component: ResultsPage,
});

type Finding = { id: number; title: string; excerpt: string; full: string; risk: Risk; confidence: number; reason: string };

function toFindings(results: ApiResult[]): Finding[] {
  return results.map((r, i) => {
    const text = (r.clause ?? "").trim();
    const conf = Number(r.confidence) || 0;
    return {
      id: i + 1,
      title: `Clause ${i + 1}`,
      excerpt: text,
      full: text,
      risk: toRisk(r.risk_level),
      confidence: Math.round(conf <= 1 ? conf * 100 : conf),
      reason: r.reason ?? "",
    };
  });
}

type Filter = "All" | "High" | "Critical";

function ResultsPage() {
  const [findings, setFindings] = useState<Finding[]>([]);
  const [fileName, setFileName] = useState("");
  const [ready, setReady] = useState(false);
  const [filter, setFilter] = useState<Filter>("All");
  const [expanded, setExpanded] = useState<number | null>(null);
  const [feedback, setFeedback] = useState<"up" | "down" | null>(null);
  useEffect(() => {
    const data = loadAnalysis();
    if (data) { setFindings(toFindings(data.results)); setFileName(data.fileName); }
    setReady(true);
  }, []);
  const filtered = useMemo(() => findings.filter((item) => filter === "All" || item.risk === filter), [filter, findings]);
  const counts = { All: findings.length, High: findings.filter((item) => item.risk === "High").length, Critical: findings.filter((item) => item.risk === "Critical").length };
  const attention = findings.filter((item) => item.risk === "High" || item.risk === "Critical").length;
  const avgConfidence = findings.length ? Math.round(findings.reduce((s, f) => s + f.confidence, 0) / findings.length) : 0;

  function updateRisk(id: number, risk: Risk) {
    setFindings((current) => current.map((item) => item.id === id ? { ...item, risk } : item));
  }

  if (ready && findings.length === 0) {
    return (
      <div className="min-h-screen bg-background">
        <SiteHeader />
        <main className="mx-auto max-w-3xl px-5 py-20 sm:px-8">
          <h1 className="font-display text-5xl sm:text-6xl">No report yet</h1>
          <p className="mt-4 text-lg text-muted-foreground">Upload a contract to see its clause-level risk report.</p>
          <Button asChild size="lg" className="mt-8"><Link to="/upload">Analyze a contract</Link></Button>
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
            <p className="flex items-center gap-2 text-sm font-medium text-risk-low-foreground"><FileSearch className="size-4" /> Analysis complete</p>
            <h1 className="mt-3 font-display text-5xl sm:text-6xl">Contract risk report</h1>
            <p className="mt-3 text-sm text-muted-foreground">{fileName} · {findings.length} clauses reviewed</p>
          </div>
          <Button asChild variant="outline"><Link to="/upload"><RotateCcw /> Analyze another contract</Link></Button>
        </div>

        <div className="grid gap-3 border-b border-border py-6 sm:grid-cols-3">
          <div><p className="text-3xl font-semibold">{findings.length}</p><p className="text-sm text-muted-foreground">Clauses found</p></div>
          <div><p className="text-3xl font-semibold">{attention}</p><p className="text-sm text-muted-foreground">Need attention</p></div>
          <div><p className="text-3xl font-semibold">{avgConfidence}%</p><p className="text-sm text-muted-foreground">Average confidence</p></div>
        </div>

        <div className="flex flex-col gap-5 py-8 sm:flex-row sm:items-center sm:justify-between">
          <div><h2 className="text-xl font-semibold">Clause findings</h2><p className="mt-1 text-sm text-muted-foreground">Expand a clause to review or override its score.</p></div>
          <div className="inline-flex w-fit rounded-md border border-border bg-card p-1" aria-label="Risk filters">
            {(["All", "High", "Critical"] as Filter[]).map((item) => (
              <Button key={item} size="sm" variant={filter === item ? "default" : "ghost"} onClick={() => setFilter(item)}>{item} <span className="opacity-60">{counts[item]}</span></Button>
            ))}
          </div>
        </div>

        <div className="overflow-hidden border-y border-border bg-card">
          <div className="hidden grid-cols-[minmax(260px,2fr)_110px_110px_minmax(220px,1.2fr)_30px] gap-5 border-b border-border bg-secondary px-5 py-3 text-xs font-semibold text-muted-foreground lg:grid">
            <span>Clause text</span><span>Risk level</span><span>Confidence</span><span>Reason</span><span />
          </div>
          {filtered.map((item) => {
            const isOpen = expanded === item.id;
            return (
              <div key={item.id} className="border-b border-border last:border-b-0">
                <button onClick={() => setExpanded(isOpen ? null : item.id)} aria-expanded={isOpen} className="grid w-full cursor-pointer gap-4 px-5 py-5 text-left transition-colors hover:bg-secondary/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring lg:grid-cols-[minmax(260px,2fr)_110px_110px_minmax(220px,1.2fr)_30px] lg:items-center lg:gap-5">
                  <div className="min-w-0"><p className="font-semibold">{item.title}</p><p className="mt-1 line-clamp-2 text-sm leading-relaxed text-muted-foreground">{item.excerpt}</p></div>
                  <div className="flex items-center justify-between lg:block"><span className="text-xs text-muted-foreground lg:hidden">Risk</span><RiskBadge risk={item.risk} /></div>
                  <div className="flex items-center justify-between lg:block"><span className="text-xs text-muted-foreground lg:hidden">Confidence</span><span className="text-sm font-medium">{item.confidence}%</span></div>
                  <div className="hidden text-sm leading-relaxed text-muted-foreground lg:block">{item.reason}</div>
                  <ChevronDown className={cn("size-4 transition-transform", isOpen && "rotate-180")} />
                </button>
                {isOpen && (
                  <div className="grid gap-6 border-t border-border bg-secondary/45 px-5 py-6 lg:grid-cols-[2fr_1.2fr]">
                    <div><p className="mb-2 text-xs font-semibold uppercase text-muted-foreground">Full clause</p><p className="text-sm leading-7">{item.full}</p></div>
                    <div>
                      <p className="mb-2 text-xs font-semibold uppercase text-muted-foreground">Why it was flagged</p><p className="text-sm leading-6 text-muted-foreground">{item.reason}</p>
                      <div className="mt-5 max-w-52"><label className="mb-2 block text-xs font-semibold" htmlFor={`risk-${item.id}`}>Override risk level</label><Select value={item.risk} onValueChange={(value) => updateRisk(item.id, value as Risk)}><SelectTrigger id={`risk-${item.id}`}><SelectValue /></SelectTrigger><SelectContent>{(["Low", "Medium", "High", "Critical"] as Risk[]).map((risk) => <SelectItem key={risk} value={risk}>{risk}</SelectItem>)}</SelectContent></Select></div>
                    </div>
                  </div>
                )}
              </div>
            );
          })}
        </div>

        <div className="flex flex-col items-center justify-between gap-6 py-10 sm:flex-row">
          <div className="flex items-center gap-3"><span className="text-sm font-medium">Was this useful?</span><Button variant={feedback === "up" ? "default" : "outline"} size="icon" aria-label="Yes, this was useful" onClick={() => setFeedback("up")}><ThumbsUp /></Button><Button variant={feedback === "down" ? "default" : "outline"} size="icon" aria-label="No, this was not useful" onClick={() => setFeedback("down")}><ThumbsDown /></Button>{feedback && <span className="text-sm text-muted-foreground">Thanks.</span>}</div>
          <p className="text-xs text-muted-foreground">AI output may contain errors. Review before relying on it.</p>
        </div>
      </main>
    </div>
  );
}