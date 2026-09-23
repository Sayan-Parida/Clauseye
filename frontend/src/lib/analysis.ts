import type { Risk } from "@/components/risk-badge";

export const API_URL = "https://clauseye-backend-app.azurewebsites.net/analyze";
const STORAGE_KEY = "clause-analysis";

export type ApiResult = { clause: string; risk_level: string; confidence: number; reason: string };
export type StoredAnalysis = { fileName: string; results: ApiResult[] };

async function extractPdf(file: File) {
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
    for (const item of content.items as Array<{ str: string; transform: number[]; hasEOL?: boolean }>) {
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

async function extractDocx(file: File) {
  const mammoth = await import("mammoth");
  const { value } = await mammoth.extractRawText({ arrayBuffer: await file.arrayBuffer() });
  return value;
}

export async function extractText(file: File) {
  const isPdf = file.type === "application/pdf" || file.name.toLowerCase().endsWith(".pdf");
  return isPdf ? extractPdf(file) : extractDocx(file);
}

const CLAUSE_START = /^\s*(?:(?:section|article|clause)\s+\d+|\d+(?:\.\d+)*\.?|\([a-z0-9]{1,4}\)|[ivx]+\.)\s+/i;

export function splitClauses(text: string) {
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
  return blocks.map((b) => b.replace(/\s+/g, " ").trim()).filter((b) => b.length >= 25);
}

export async function analyzeClauses(clauses: string[]) {
  const res = await fetch(API_URL, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ clauses }),
  });
  if (!res.ok) throw new Error(`The analysis service returned an error (${res.status}).`);
  const data = (await res.json()) as { results?: ApiResult[] };
  if (!Array.isArray(data.results)) throw new Error("The analysis service returned an unexpected response.");
  return data.results;
}

export function saveAnalysis(data: StoredAnalysis) {
  sessionStorage.setItem(STORAGE_KEY, JSON.stringify(data));
}

export function loadAnalysis(): StoredAnalysis | null {
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY);
    return raw ? (JSON.parse(raw) as StoredAnalysis) : null;
  } catch {
    return null;
  }
}

export function toRisk(level: string): Risk {
  const l = level?.toLowerCase();
  if (l === "critical") return "Critical";
  if (l === "high") return "High";
  if (l === "medium") return "Medium";
  return "Low";
}
