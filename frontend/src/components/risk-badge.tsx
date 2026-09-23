import { cn } from "@/lib/utils";

export type Risk = "Low" | "Medium" | "High" | "Critical";

const styles: Record<Risk, string> = {
  Low: "bg-risk-low text-risk-low-foreground",
  Medium: "bg-risk-medium text-risk-medium-foreground",
  High: "bg-risk-high text-risk-high-foreground",
  Critical: "bg-risk-critical text-risk-critical-foreground",
};

export function RiskBadge({ risk }: { risk: Risk }) {
  return (
    <span className={cn("inline-flex min-w-16 items-center justify-center rounded-full px-2.5 py-1 text-xs font-semibold", styles[risk])}>
      {risk}
    </span>
  );
}