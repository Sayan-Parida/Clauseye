import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useRef, useState, type DragEvent } from "react";
import { ArrowLeft, FileText, LockKeyhole, Trash2, UploadCloud } from "lucide-react";
import { SiteHeader } from "@/components/site-header";
import { Button } from "@/components/ui/button";
import { analyzeClauses, extractText, saveAnalysis, splitClauses } from "@/lib/analysis";

export const Route = createFileRoute("/upload")({
  head: () => ({ meta: [
    { title: "Upload a contract — Clause" },
    { name: "description", content: "Upload a PDF or DOCX contract for a private, instant mock risk analysis." },
    { property: "og:title", content: "Upload a contract — Clause" },
    { property: "og:description", content: "Upload a PDF or DOCX contract for a private, instant mock risk analysis." },
    { property: "og:type", content: "website" },
    { name: "twitter:card", content: "summary_large_image" },
  ]}),
  component: UploadPage,
});

function formatSize(bytes: number) {
  return bytes < 1024 * 1024 ? `${Math.max(1, Math.round(bytes / 1024))} KB` : `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

function UploadPage() {
  const navigate = useNavigate();
  const inputRef = useRef<HTMLInputElement>(null);
  const [file, setFile] = useState<File | null>(null);
  const [error, setError] = useState("");
  const [dragging, setDragging] = useState(false);
  const [loading, setLoading] = useState(false);

  function chooseFile(nextFile?: File) {
    if (!nextFile) return;
    const valid = nextFile.type === "application/pdf" || nextFile.name.toLowerCase().endsWith(".docx");
    if (!valid) {
      setFile(null);
      setError("Please choose a PDF or DOCX file.");
      return;
    }
    setFile(nextFile);
    setError("");
  }

  function onDrop(event: DragEvent<HTMLDivElement>) {
    event.preventDefault();
    setDragging(false);
    chooseFile(event.dataTransfer.files[0]);
  }

  async function analyze() {
    if (!file) return;
    setLoading(true);
    setError("");
    let clauses: string[];
    try {
      clauses = splitClauses(await extractText(file));
    } catch {
      setLoading(false);
      setError("We couldn't read text from this file. Try another PDF or DOCX.");
      return;
    }
    if (clauses.length === 0) {
      setLoading(false);
      setError("No readable clauses were found. Scanned PDFs without text aren't supported yet.");
      return;
    }
    try {
      const results = await analyzeClauses(clauses);
      saveAnalysis({ fileName: file.name, results });
      navigate({ to: "/results" });
    } catch (e) {
      setLoading(false);
      setError(e instanceof Error && e.message.startsWith("The analysis") ? `${e.message} Please try again.` : "We couldn't reach the analysis service. Check your connection and try again.");
    }
  }

  return (
    <div className="min-h-screen bg-background">
      <SiteHeader />
      <main className="mx-auto max-w-3xl px-5 py-12 sm:px-8 sm:py-20">
        <a href="/" className="mb-10 inline-flex items-center gap-2 text-sm text-muted-foreground hover:text-foreground"><ArrowLeft className="size-4" /> Back</a>
        <div className="mb-10">
          <p className="text-sm font-medium text-risk-low-foreground">New analysis</p>
          <h1 className="mt-3 font-display text-5xl leading-tight sm:text-6xl">Upload your contract.</h1>
          <p className="mt-4 max-w-xl text-lg text-muted-foreground">We’ll scan each clause and surface the risks that deserve your attention.</p>
        </div>

        {loading ? (
          <div className="border border-foreground bg-card p-7 sm:p-10" role="status" aria-live="polite">
            <div className="flex items-start gap-4">
              <div className="grid size-12 shrink-0 place-items-center rounded-full bg-secondary"><FileText className="size-5" /></div>
              <div className="min-w-0 flex-1">
                <h2 className="text-lg font-semibold">Reviewing {file?.name}</h2>
                <p className="mt-1 text-sm text-muted-foreground">Identifying clauses and comparing risk patterns…</p>
              </div>
            </div>
            <div className="mt-8 h-1 overflow-hidden bg-secondary"><div className="h-full animate-scan-progress bg-foreground" /></div>
            <p className="mt-3 text-xs text-muted-foreground">This usually takes a few seconds.</p>
          </div>
        ) : (
          <>
            <div
              onDragOver={(event) => { event.preventDefault(); setDragging(true); }}
              onDragLeave={() => setDragging(false)}
              onDrop={onDrop}
              onClick={() => inputRef.current?.click()}
              onKeyDown={(event) => { if (event.key === "Enter" || event.key === " ") inputRef.current?.click(); }}
              role="button"
              tabIndex={0}
              className={`grid min-h-72 cursor-pointer place-items-center border border-dashed p-8 text-center transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${dragging ? "border-foreground bg-secondary" : "border-input bg-card hover:border-foreground"}`}
            >
              <div>
                <span className="mx-auto grid size-14 place-items-center rounded-full bg-secondary"><UploadCloud className="size-6" /></span>
                <h2 className="mt-5 text-lg font-semibold">Drag and drop your contract</h2>
                <p className="mt-2 text-sm text-muted-foreground">Or click to browse</p>
                <p className="mt-5 text-xs text-muted-foreground">PDF or DOCX · up to 20 MB</p>
                <input ref={inputRef} type="file" accept=".pdf,.docx,application/pdf,application/vnd.openxmlformats-officedocument.wordprocessingml.document" className="sr-only" onChange={(event) => chooseFile(event.target.files?.[0])} />
              </div>
            </div>
            {error && <p className="mt-3 text-sm text-destructive" role="alert">{error}</p>}
            {file && (
              <div className="mt-4 flex items-center gap-4 border border-border bg-card p-4">
                <FileText className="size-5 shrink-0" />
                <div className="min-w-0 flex-1"><p className="truncate text-sm font-medium">{file.name}</p><p className="text-xs text-muted-foreground">{formatSize(file.size)}</p></div>
                <Button variant="ghost" size="icon" aria-label="Remove file" onClick={() => setFile(null)}><Trash2 /></Button>
              </div>
            )}
            <Button size="lg" className="mt-6 w-full" disabled={!file} onClick={analyze}>Analyze contract</Button>
          </>
        )}
        <p className="mt-6 flex items-center justify-center gap-2 text-xs text-muted-foreground"><LockKeyhole className="size-3.5" /> Your document stays in your browser.</p>
      </main>
    </div>
  );
}