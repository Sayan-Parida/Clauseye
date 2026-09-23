import { Link } from "@tanstack/react-router";

export function SiteHeader() {
  return (
    <header className="border-b border-border">
      <div className="mx-auto flex h-16 max-w-7xl items-center justify-between px-5 sm:px-8">
        <Link to="/" className="inline-flex items-center gap-2.5 font-semibold text-foreground">
          <span className="grid size-7 place-items-center rounded-sm bg-primary text-xs text-primary-foreground">C</span>
          <span className="text-lg">Clause</span>
        </Link>
        <Link
          to="/upload"
          className="text-sm font-medium text-muted-foreground transition-colors hover:text-foreground"
        >
          Analyze a contract
        </Link>
      </div>
    </header>
  );
}