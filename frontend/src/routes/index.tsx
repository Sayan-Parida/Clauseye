import { createFileRoute, Link } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";
import { ArrowRight, Check, FileUp, Gauge, SearchCheck, ShieldCheck } from "lucide-react";
import { SiteHeader } from "@/components/site-header";
import { SiteFooter } from "@/components/site-footer";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

export const Route = createFileRoute("/")({
  head: () => ({
    meta: [
      { title: "Clause — AI contract risk analysis" },
      { name: "description", content: "Review contracts in seconds with AI-powered clause risk triage for legal teams." },
      { property: "og:title", content: "Clause — AI contract risk analysis" },
      { property: "og:description", content: "Review contracts in seconds with AI-powered clause risk triage for legal teams." },
      { property: "og:type", content: "website" },
      { name: "twitter:card", content: "summary_large_image" },
    ],
  }),
  component: Index,
});

const steps = [
  { number: "01", title: "Upload", detail: "Add a PDF or DOCX", icon: FileUp },
  { number: "02", title: "Get risk scores", detail: "AI reviews every clause", icon: Gauge },
  { number: "03", title: "Review flags", detail: "Focus on what matters", icon: SearchCheck },
];

function Index() {
  const [email, setEmail] = useState("");
  const [joined, setJoined] = useState(false);
  const [error, setError] = useState("");

  function submitWaitlist(event: FormEvent) {
    event.preventDefault();
    if (!/^\S+@\S+\.\S+$/.test(email)) {
      setError("Enter a valid work email.");
      return;
    }
    setError("");
    setJoined(true);
  }

  return (
    <div className="min-h-screen bg-background">
      <SiteHeader />
      <main>
        <section className="mx-auto grid min-h-[calc(100vh-4rem)] max-w-7xl content-between px-5 py-12 sm:px-8 sm:py-16 lg:py-20">
          <div className="grid items-end gap-12 lg:grid-cols-[1.35fr_0.65fr] lg:gap-20">
            <div>
              <p className="mb-8 flex items-center gap-2 text-sm font-medium text-muted-foreground">
                <ShieldCheck className="size-4 text-risk-low-foreground" />
                Built for fast first-pass review
              </p>
              <h1 className="max-w-4xl font-display text-6xl leading-[0.94] font-medium sm:text-7xl lg:text-[6.5rem]">
                Review contracts in seconds, not hours.
              </h1>
            </div>
            <div className="max-w-md pb-2">
              <p className="text-xl leading-relaxed text-muted-foreground">
                AI-powered clause risk triage for legal teams.
              </p>
              <Button asChild size="lg" className="mt-8 w-full sm:w-auto">
                <Link to="/upload">Analyze a contract — free <ArrowRight /></Link>
              </Button>
              <p className="mt-5 text-xs leading-relaxed text-muted-foreground">
                Your document never leaves your browser. Only anonymized text is analyzed.
              </p>
            </div>
          </div>

          <div className="mt-20 border-t border-foreground lg:mt-24">
            <div className="grid md:grid-cols-3">
              {steps.map((step, index) => (
                <div key={step.number} className={`group flex min-h-40 items-start gap-5 py-7 md:px-7 ${index === 0 ? "md:pl-0" : "border-t border-border md:border-t-0 md:border-l"}`}>
                  <step.icon className="mt-1 size-5 shrink-0" strokeWidth={1.5} />
                  <div className="flex-1">
                    <div className="flex justify-between gap-4">
                      <h2 className="text-lg font-semibold">{step.title}</h2>
                      <span className="font-mono text-xs text-muted-foreground">{step.number}</span>
                    </div>
                    <p className="mt-2 text-sm text-muted-foreground">{step.detail}</p>
                  </div>
                </div>
              ))}
            </div>
          </div>
        </section>

        <section className="border-y border-foreground bg-primary text-primary-foreground">
          <div className="mx-auto grid max-w-7xl gap-10 px-5 py-16 sm:px-8 md:grid-cols-2 md:items-center md:py-20">
            <div>
              <p className="text-sm text-primary-foreground/60">Private beta</p>
              <h2 className="mt-3 max-w-lg font-display text-4xl leading-tight sm:text-5xl">Better reviews start with better focus.</h2>
            </div>
            <div className="md:justify-self-end">
              {joined ? (
                <div className="flex items-center gap-3 text-lg"><span className="grid size-8 place-items-center rounded-full bg-accent text-accent-foreground"><Check className="size-4" /></span> You’re on the list.</div>
              ) : (
                <form onSubmit={submitWaitlist} className="w-full md:w-[28rem]" noValidate>
                  <label htmlFor="email" className="mb-3 block text-sm">Join the waitlist</label>
                  <div className="flex flex-col gap-2 sm:flex-row">
                    <Input id="email" type="email" value={email} onChange={(event) => setEmail(event.target.value)} placeholder="you@company.com" className="h-12 border-primary-foreground/35 bg-primary text-primary-foreground placeholder:text-primary-foreground/45 focus-visible:ring-primary-foreground" />
                    <Button type="submit" variant="secondary" size="lg">Join</Button>
                  </div>
                  {error && <p className="mt-2 text-sm text-risk-critical">{error}</p>}
                </form>
              )}
            </div>
          </div>
        </section>
      </main>
      <SiteFooter />
    </div>
  );
}