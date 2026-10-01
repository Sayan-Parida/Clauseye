import type { AnalyzeResponse, ApiResult, SkippedClause, StoredAnalysis } from "@/lib/risk";

/**
 * Analysis client.
 *
 * The contract file never leaves the browser: PDF text is extracted with pdfjs
 * and DOCX with mammoth, both in the page, and only the resulting clause strings
 * are sent to the API. That is a genuine privacy property of this design, and it
 * is worth preserving when changing anything below.
 */

const DEFAULT_API_URL = "https://clauseye-backend-app.azurewebsites.net/analyze";

// Vite inlines import.meta.env at build time, so there is no runtime env lookup
// and no way for a value to be supplied by a user. Overridable at build time so
// development can point at a local backend instead of production.
export const API_URL =
  (import.meta.env["VITE_ANALYZE_API_URL"] as string | undefined) ?? DEFAULT_API_URL;

/** Keep this in step with the backend's MAX_CLAUSES default. */
export const MAX_CLAUSES = 200;
/** Keep this in step with the backend's MIN_CLAUSE_CHARS default. */
export const MIN_CLAUSE_CHARS = 40;
/** Keep this in step with the backend's MAX_REQUEST_BYTES default (8 MiB). */
export const MAX_REQUEST_BYTES = 8 * 1024 * 1024;
/** Client-side file ceiling. Enforced before the file is read into memory. */
export const MAX_FILE_BYTES = 20 * 1024 * 1024;

const STORAGE_KEY = "clause-analysis-v2";
const LEGACY_STORAGE_KEY = "clause-analysis";

export type { AnalyzeResponse, ApiResult, SkippedClause, StoredAnalysis };

export class AnalysisError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(message: string, status: number, code: string) {
    super(message);
    this.name = "AnalysisError";
    this.status = status;
    this.code = code;
  }
}

async function extractPdf(file: File): Promise<string> {
  const pdfjs = await import("pdfjs-dist");
  const worker = await import("pdfjs-dist/build/pdf.worker.min.mjs?url");
  pdfjs.GlobalWorkerOptions.workerSrc = worker.default;
  const pdf = await pdfjs.getDocument({ data: await file.arrayBuffer() }).promise;
  const pages: string[] = [];
  for (let i = 1; i <= pdf.numPages; i++) {
    const page = await pdf.getPage(i);
    const content = await page.getTextContent();
    let text = "";
    let lastY: number | null = null;
    for (const item of content.items as Array<{
      str: string;
      transform: number[];
      hasEOL?: boolean;
    }>) {
      if (typeof item.str !== "string") continue;
      const y = item.transform[5] ?? 0;
      if (lastY !== null && Math.abs(y - lastY) > 14) text += "\n\n";
      else if (lastY !== null && Math.abs(y - lastY) > 1) text += "\n";
      text += item.str;
      if (item.hasEOL) text += "\n";
      lastY = y;
    }
    pages.push(text);
  }
  return pages.join("\n\n");
}

async function extractDocx(file: File): Promise<string> {
  const mammoth = await import("mammoth");
  const { value } = await mammoth.extractRawText({ arrayBuffer: await file.arrayBuffer() });
  return value;
}

export function isSupportedFile(file: File): boolean {
  return (
    file.type === "application/pdf" || file.name.toLowerCase().endsWith(".pdf") || isDocx(file)
  );
}

export function isDocx(file: File): boolean {
  const name = file.name.toLowerCase();
  return (
    file.type === "application/vnd.openxmlformats-officedocument.wordprocessingml.document" ||
    name.endsWith(".docx")
  );
}

export async function extractText(file: File): Promise<string> {
  return file.type === "application/pdf" || file.name.toLowerCase().endsWith(".pdf")
    ? extractPdf(file)
    : extractDocx(file);
}

/**
 * Structural markers that start a clause. Must stay aligned with the backend's
 * clauseNumbering pattern, since the backend strips the same markers before
 * analysis.
 */
const CLAUSE_START =
  /^\s*(?:(?:section|article|clause)\s+\d+|\d+(?:\.\d+)*\.?|\([a-z0-9]{1,4}\)|[ivx]+\.)\s+/i;

export function splitClauses(text: string): string[] {
  const lines = text.replace(/\r/g, "").split("\n");
  const blocks: string[] = [];
  let current = "";
  for (const raw of lines) {
    const line = raw.trim();
    if (!line) {
      if (current) blocks.push(current);
      current = "";
      continue;
    }
    if (CLAUSE_START.test(line) && current) {
      blocks.push(current);
      current = line;
    } else current = current ? `${current} ${line}` : line;
  }
  if (current) blocks.push(current);
  // Keep anything the backend might accept, so the client rejects nothing the
  // server would have analysed.
  return blocks
    .map((b) => b.replace(/\s+/g, " ").trim())
    .filter((b) => b.length >= MIN_CLAUSE_CHARS);
}

export type PreflightResult = { ok: true; clauseCount: number } | { ok: false; reason: string };

/**
 * Check a request before it is sent, so the user gets an accurate message
 * instead of a generic failure after a long upload.
 */
export function preflight(file: File, clauseCount: number): PreflightResult {
  if (!isSupportedFile(file)) {
    return { ok: false, reason: "Please choose a PDF or DOCX file." };
  }
  if (file.size > MAX_FILE_BYTES) {
    return {
      ok: false,
      reason: `That file is ${formatSize(file.size)}. The limit is ${formatSize(MAX_FILE_BYTES)}.`,
    };
  }
  if (clauseCount === 0) {
    return {
      ok: false,
      reason:
        "No readable clauses were found. Scanned PDFs without a text layer are not supported yet.",
    };
  }
  if (clauseCount > MAX_CLAUSES) {
    return {
      ok: false,
      reason: `This contract has ${clauseCount} clauses and the limit is ${MAX_CLAUSES}. Split it into sections and analyse them separately.`,
    };
  }
  return { ok: true, clauseCount };
}

export function estimatePayloadBytes(clauses: string[]): number {
  return clauses.reduce((total, clause) => total + clause.length * 2 + 3, 0);
}

export function formatSize(bytes: number): string {
  if (bytes < 1024 * 1024) return `${Math.max(1, Math.round(bytes / 1024))} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

export async function analyzeClauses(clauses: string[]): Promise<AnalyzeResponse> {
  const payload = estimatePayloadBytes(clauses);
  if (payload > MAX_REQUEST_BYTES) {
    throw new AnalysisError(
      `This contract is too large to send in one request (${formatSize(payload)}). Split it into sections.`,
      413,
      "payload_too_large",
    );
  }

  let response: Response;
  try {
    response = await fetch(API_URL, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ clauses }),
    });
  } catch {
    throw new AnalysisError(
      "We couldn't reach the analysis service. Check your connection and try again.",
      0,
      "network_error",
    );
  }

  if (!response.ok) {
    const body = await response.json().catch(() => null);
    throw new AnalysisError(
      body?.error ?? `The analysis service returned an error (${response.status}).`,
      response.status,
      body?.code ?? "unknown",
    );
  }

  const data = (await response.json()) as AnalyzeResponse;
  if (data.schema_version !== 2 || !Array.isArray(data.results)) {
    throw new AnalysisError(
      "The analysis service returned an unexpected response.",
      502,
      "bad_schema",
    );
  }
  return data;
}

export function saveAnalysis(data: StoredAnalysis): void {
  sessionStorage.setItem(STORAGE_KEY, JSON.stringify(data));
}

/**
 * Load a stored analysis, falling back to the schema v1 cache.
 *
 * The fallback exists because a report can outlive a deploy: someone who ran an
 * analysis before the upgrade still has v1 data in sessionStorage, and showing
 * them the old, simpler report beats showing them an error.
 */
export function loadAnalysis(): StoredAnalysis | null {
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY);
    if (raw) return JSON.parse(raw) as StoredAnalysis;

    const legacy = sessionStorage.getItem(LEGACY_STORAGE_KEY);
    if (!legacy) return null;
    const parsed = JSON.parse(legacy) as { fileName: string; results: ApiResult[] };
    return {
      fileName: parsed.fileName,
      analyzedAt: 0,
      results: (parsed.results ?? []).map((result, index) => ({
        clause: result.clause ?? "",
        clause_index: index,
        risk_level: result.risk_level ?? "low",
        risk_score: 0,
        confidence: Number(result.confidence) || 0,
        needs_review: false,
        explanation: result.reason ?? "",
        reason: result.reason ?? "",
        issues: [],
      })),
    };
  } catch {
    return null;
  }
}

export function clearAnalysis(): void {
  sessionStorage.removeItem(STORAGE_KEY);
  sessionStorage.removeItem(LEGACY_STORAGE_KEY);
}
