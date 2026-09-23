export function SiteFooter() {
  return (
    <footer className="border-t border-border">
      <div className="mx-auto flex max-w-7xl flex-col gap-4 px-5 py-7 text-sm text-muted-foreground sm:flex-row sm:items-center sm:justify-between sm:px-8">
        <p>© 2026 Clause</p>
        <nav aria-label="Footer" className="flex gap-6">
          <a href="#privacy" className="transition-colors hover:text-foreground">Privacy</a>
          <a href="#terms" className="transition-colors hover:text-foreground">Terms</a>
          <a href="#contact" className="transition-colors hover:text-foreground">Contact</a>
        </nav>
      </div>
    </footer>
  );
}