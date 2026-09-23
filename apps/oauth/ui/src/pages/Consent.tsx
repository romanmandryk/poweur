import { BadgeCheck, TriangleAlert } from "lucide-react";
import type { ConsentData, Page } from "../lib/page";
import { Button } from "../ui/Button";
import { Card, Chip, Monogram } from "../ui/Display";
import { Shell } from "../ui/Shell";

const RELEASES: Record<string, { title: string; detail: string }> = {
  poweur_id: { title: "Your Poweur ID", detail: "and its key fingerprint" },
  profile: { title: "Your name and photo", detail: "from your public profile" },
};

export function Consent({ page }: { page: Page<ConsentData> }) {
  const d = page.data;
  const c = d.client;
  return (
    <Shell page={page} narrow>
      <Card className="animate-fade-in-up">
        <div className="flex flex-col items-center text-center">
          <Monogram text={c.name} className="mb-4 size-14 text-2xl" />
          <h1 className="text-[24px] leading-tight font-extrabold tracking-[-.3px]">Allow {c.name}?</h1>
          {c.host && (
            <p className="mt-2 flex items-center gap-1.5 text-sm text-muted">
              Returns to <strong className="text-fg">{c.host}</strong>
              {c.verifiedHost && (
                <Chip tone="success">
                  <BadgeCheck className="size-3.5" aria-hidden="true" /> verified
                </Chip>
              )}
            </p>
          )}
        </div>

        {c.registeredBy && c.registeredBy.length > 0 && (
          <p id="consent-unreviewed" className="mt-4 flex items-start gap-2 rounded-control bg-warning/12 px-3.5 py-2.5 text-[13px] text-fg">
            <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warning" aria-hidden="true" />
            <span>
              Registered by <strong>{c.registeredBy.join(", ")}</strong>. {page.service.name} has not reviewed it.
            </span>
          </p>
        )}

        <form method="post" action={`/t/${d.txn}/consent`} className="mt-6">
          <p className="mb-2 text-[13px] font-semibold text-muted">
            Signing in as <span className="text-fg">{d.identity}</span>. {c.name} will also see:
          </p>
          <ul className="divide-y divide-sep rounded-control bg-surface-2">
            {d.indieAuth && (
              <li className="flex items-center gap-3 px-4 py-3">
                <input type="checkbox" checked disabled className="size-5 accent-accent" aria-label="Your Poweur ID (always shared)" />
                <span className="text-[15px]">
                  <span className="font-semibold">Your Poweur ID</span>
                  <span className="block text-[13px] text-muted">IndieAuth always shares {d.profileURL}</span>
                </span>
              </li>
            )}
            {d.optional.map((scope) => (
              <li key={scope}>
                <label className="flex cursor-pointer items-center gap-3 px-4 py-3">
                  <input type="checkbox" name={`release_${scope}`} defaultChecked className="size-5 accent-accent" />
                  <span className="text-[15px]">
                    <span className="font-semibold">{RELEASES[scope]?.title ?? scope}</span>
                    <span className="block text-[13px] text-muted">{RELEASES[scope]?.detail}</span>
                  </span>
                </label>
              </li>
            ))}
            {!d.indieAuth && d.optional.length === 0 && (
              <li className="px-4 py-3 text-[15px] text-muted">Nothing else — not even your Poweur ID.</li>
            )}
          </ul>
          <p className="mt-3 text-[13px] text-muted">No access to your messages or files.</p>
          <div className="mt-6 grid grid-cols-2 gap-3">
            <Button type="submit" name="decision" value="deny" variant="secondary" id="consent-deny">
              Deny
            </Button>
            <Button type="submit" name="decision" value="allow" id="consent-allow">
              Allow
            </Button>
          </div>
        </form>
      </Card>
      <p className="mt-4 text-center text-[13px]">
        <a href={page.service.contact || "/abuse"} className="text-muted">
          Report this app
        </a>
      </p>
    </Shell>
  );
}
