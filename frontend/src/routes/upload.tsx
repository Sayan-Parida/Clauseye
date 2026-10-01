import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useRef, useState, type DragEvent } from "react";
import { ArrowLeft, FileText, LockKeyhole, Trash2, UploadCloud } from "lucide-react";
import { SiteHeader } from "@/components/site-header";
import { Button } from "@/components/ui/button";
import {
  AnalysisError,
  MAX_FILE_BYTES,
  analyzeClauses,
  extractText,
  formatSize,
  isSupportedFile,
  preflight,
  saveAnalysis,
  splitClauses,
} from "@/lib/analysis";

export const Route = createFileRoute("/upload")({
  head: () => ({
    meta: [
      { title: "Upload a contract — Clause" },
      {
        name: "description",
        content: "Upload a PDF or DOCX contract for private, on-premises clause risk analysis.",
      },
      { property: "og:title", content: "Upload a contract — Clause" },
      {
        property: "og:description",
        content: "Upload a PDF or DOCX contract for private, on-premises clause risk analysis.",
      },
      { property: "og:type", content: "website" },
      { name: "twitter:card", content: "summary_large_image" },
    ],
  }),
  component: UploadPage,
});

function UploadPage() {
  const navigate = useNavigate();
  const inputRef = useRef<HTMLInputElement>(null);
  const [file, setFile] = useState<File | null>(null);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [dragging, setDragging] = useState(false);
  const [loading, setLoading] = useState(false);

  function chooseFile(nextFile?: File) {
    if (!nextFile) return;
    if (!isSupportedFile(nextFile)) {
      setFile(null);
      setError("Please choose a PDF or DOCX file.");
      return;
    }
    // Checked before anything reads the file, because the extraction libraries
    // buffer the whole thing into memory.
    if (nextFile.size > MAX_FILE_BYTES) {
      setFile(null);
      setError(
        `That file is ${formatSize(nextFile.size)}. The limit is ${formatSize(MAX_FILE_BYTES)}.`,
      );
      return;
    }
    setFile(nextFile);
    setError("");
    setNotice("");
  }

  function onDrop(event: DragEvent<HTMLDivElement>) {
    event.preventDefault();
    setDragging(false);
    chooseFile(event.dataTransfer.files[0]);
  }

  async function analyze() {
    if (!file) return;
    setError("");
    setNotice("");

    let clauses: string[];
    try {
      clauses = splitClauses(await extractText(file));
    } catch {
      setError("We couldn't read text from this file. Try another PDF or DOCX.");
      return;
    }

    const check = preflight(file, clauses.length);
    if (!check.ok) {
      setError(check.reason);
      return;
    }

    setLoading(true);
    // Measured, because the whole point of the notice is that this can take a
    // while and silence would read as a hang.
    setNotice(
      `Analysing ${clauses.length} clauses. This can take a couple of minutes while the model runs on our servers.`,
    );

    try {
      const data = await analyzeClauses(clauses);
      saveAnalysis({
        fileName: file.name,
        analyzedAt: Date.now(),
        results: data.results,
        skipped: data.skipped ?? [],
        meta: data.meta,
        warnings: data.warnings,
      });
      navigate({ to: "/results" });
    } catch (e) {
      setLoading(false);
      if (e instanceof AnalysisError) {
        // The service returns specific messages for the failures a user can
        // actually act on: too many clauses, body too large, engine down.
        setError(e.status === 0 ? e.message : `${e.message}`);
      } else {
        setError("We couldn't reach the analysis service. Check your connection and try again.");
      }
    }
  }

  return (
    <div className="min-h-screen bg-background">
      <SiteHeader />
      <main className="mx-auto max-w-3xl px-5 py-12 sm:px-8 sm:py-20">
        <a
          href="/"
          className="mb-10 inline-flex items-center gap-2 text-sm text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="size-4" /> Back
        </a>
        <div className="mb-10">
          <p className="text-sm font-medium text-risk-low-foreground">New analysis</p>
          <h1 className="mt-3 font-display text-5xl leading-tight sm:text-6xl">
            Upload your contract.
          </h1>
          <p className="mt-4 max-w-xl text-lg text-muted-foreground">
            We'll scan each clause and surface the risks that deserve your attention.
          </p>
        </div>

        {loading ? (
          <div
            className="border border-foreground bg-card p-7 sm:p-10"
            role="status"
            aria-live="polite"
          >
            <div className="flex items-start gap-4">
              <div className="grid size-12 shrink-0 place-items-center rounded-full bg-secondary">
                <FileText className="size-5" />
              </div>
              <div className="min-w-0 flex-1">
                <h2 className="text-lg font-semibold">Reviewing {file?.name}</h2>
                <p className="mt-1 text-sm text-muted-foreground">
                  Identifying clauses and scoring each risk category. This runs on our own hardware,
                  so the document is not sent to a third party.
                </p>
              </div>
            </div>
            <div className="mt-8 h-1 overflow-hidden bg-secondary">
              <div className="h-full animate-scan-progress bg-foreground" />
            </div>
            <p className="mt-3 text-xs text-muted-foreground">Larger contracts take longer.</p>
          </div>
        ) : (
          <>
            <div
              onDragOver={(event) => {
                event.preventDefault();
                setDragging(true);
              }}
              onDragLeave={() => setDragging(false)}
              onDrop={onDrop}
              onClick={() => inputRef.current?.click()}
              onKeyDown={(event) => {
                if (event.key === "Enter" || event.key === " ") inputRef.current?.click();
              }}
              role="button"
              tabIndex={0}
              aria-label="Choose a contract to upload"
              className={`grid min-h-72 cursor-pointer place-items-center border border-dashed p-8 text-center transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${dragging ? "border-foreground bg-secondary" : "border-input bg-card hover:border-foreground"}`}
            >
              <div>
                <span className="mx-auto grid size-14 place-items-center rounded-full bg-secondary">
                  <UploadCloud className="size-6" />
                </span>
                <h2 className="mt-5 text-lg font-semibold">Drag and drop your contract</h2>
                <p className="mt-2 text-sm text-muted-foreground">Or click to browse</p>
                <p className="mt-5 text-xs text-muted-foreground">
                  PDF or DOCX · up to {formatSize(MAX_FILE_BYTES)}
                </p>
                <input
                  ref={inputRef}
                  type="file"
                  accept=".pdf,.docx,application/pdf,application/vnd.openxmlformats-officedocument.wordprocessingml.document"
                  className="sr-only"
                  onChange={(event) => chooseFile(event.target.files?.[0])}
                />
              </div>
            </div>
            {error ? (
              <p className="mt-3 text-sm text-destructive" role="alert">
                {error}
              </p>
            ) : null}
            {notice ? (
              <p className="mt-3 text-sm text-muted-foreground" role="status">
                {notice}
              </p>
            ) : null}
            {file ? (
              <div className="mt-4 flex items-center gap-4 border border-border bg-card p-4">
                <FileText className="size-5 shrink-0" />
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-medium">{file.name}</p>
                  <p className="text-xs text-muted-foreground">{formatSize(file.size)}</p>
                </div>
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label="Remove file"
                  onClick={() => {
                    setFile(null);
                    setError("");
                  }}
                >
                  <Trash2 />
                </Button>
              </div>
            ) : null}
            <Button size="lg" className="mt-6 w-full" disabled={!file} onClick={analyze}>
              Analyze contract
            </Button>
          </>
        )}
        <p className="mt-6 flex items-center justify-center gap-2 text-center text-xs text-muted-foreground">
          <LockKeyhole className="size-3.5 shrink-0" />
          Your document is read in your browser, then analysed on our own infrastructure. It is not
          sent to an external AI provider.
        </p>
      </main>
    </div>
  );
}
