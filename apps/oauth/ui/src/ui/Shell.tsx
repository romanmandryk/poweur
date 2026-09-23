import type { ReactNode } from "react";
import { KeyRound } from "lucide-react";
import type { Page } from "../lib/page";
import { cn } from "../lib/cn";

/**
 * Every page: a quiet header (who you are, when signed in), the content, and
 * the footer links. `narrow` is for the sign-in flow — one column, one task.
 */
export function Shell({ page, narrow = false, children }: { page: Page; narrow?: boolean; children: ReactNode }) {
  const session = page.session;
  return (
    <div className="flex min-h-screen flex-col">
      <header className="sticky top-0 z-10 border-b border-sep bg-chrome backdrop-blur-xl">
        <div className="mx-auto flex h-14 max-w-4xl items-center gap-3 px-4">
          <a href="/" className="flex items-center gap-2 text-fg no-underline">
            <span className="flex size-8 items-center justify-center rounded-[10px] bg-linear-135 from-accent to-accent-2 text-white">
              <KeyRound className="size-4" strokeWidth={2.2} aria-hidden="true" />
            </span>
            <span className="text-[15px] font-bold tracking-tight">{page.service.name}</span>
          </a>
          <div className="flex-1" />
          {session && (
            <nav className="flex items-center gap-1 text-sm" aria-label="Account">
              <a href="/account" id="nav-identity" className="max-w-[40vw] truncate rounded-full bg-surface-2 px-3 py-1.5 font-semibold text-fg no-underline">
                {session.identity}
              </a>
              <form method="post" action="/logout">
                <button type="submit" id="nav-signout" className="rounded-full px-3 py-1.5 font-medium text-muted hover:text-fg">
                  Sign out
                </button>
              </form>
            </nav>
          )}
        </div>
      </header>
      <main className={cn("mx-auto w-full flex-1 px-4 py-8 sm:py-12", narrow ? "max-w-[440px]" : "max-w-3xl")}>{children}</main>
      <footer className="mx-auto flex w-full max-w-4xl flex-wrap justify-center gap-x-5 gap-y-1 px-4 pt-4 pb-8 text-[13px] text-muted">
        <a href="/" className="text-muted">About</a>
        <a href="/privacy" className="text-muted">Privacy</a>
        <a href="/security" className="text-muted">Security</a>
        <a href={page.service.contact || "/abuse"} className="text-muted">Report an app</a>
      </footer>
    </div>
  );
}

/** A page's title block. */
export function PageHead({ title, subtitle, className }: { title: ReactNode; subtitle?: ReactNode; className?: string }) {
  return (
    <div className={cn("mb-6", className)}>
      <h1 className="text-[26px] leading-tight font-extrabold tracking-[-.4px]">{title}</h1>
      {subtitle && <p className="mt-1.5 text-[15px] text-muted">{subtitle}</p>}
    </div>
  );
}
