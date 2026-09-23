import type { ReactNode } from "react";
import { AppWindow, History } from "lucide-react";
import { formatDate, scopeLabel } from "../lib/format";
import type { AccountData, Page } from "../lib/page";
import { Banner, Card, Chip, Monogram } from "../ui/Display";
import { PageHead, Shell } from "../ui/Shell";

export function Account({ page }: { page: Page<AccountData> }) {
  const d = page.data;
  return (
    <Shell page={page}>
      <PageHead title="Your apps" subtitle="Apps that sign you in with your Poweur ID." />
      {d.notice === "revoked" && (
        <Banner className="mb-4">Revoked. That app will ask again next time you sign in.</Banner>
      )}
      <Card className="p-0 sm:p-0">
        {d.consents.length ? (
          <ul id="authorized-apps" className="divide-y divide-sep">
            {d.consents.map((c) => (
              <li key={c.clientId} className="flex items-center gap-3.5 px-5 py-4">
                <Monogram text={c.clientName} className="size-10 rounded-[12px] text-base" />
                <div className="min-w-0 flex-1">
                  <p className="truncate font-semibold">{c.clientName}</p>
                  <p className="truncate text-[13px] text-muted">
                    {c.clientHost ? `${c.clientHost} · ` : ""}last used {formatDate(c.lastUsed)}
                  </p>
                  <p className="mt-1 flex flex-wrap gap-1">
                    {c.granted.length ? c.granted.map((g) => <Chip key={g}>{scopeLabel(g)}</Chip>) : <Chip>Sign-in only</Chip>}
                  </p>
                </div>
                <form method="post" action="/account/revoke">
                  <input type="hidden" name="client_id" value={c.clientId} />
                  <button type="submit" className="revoke rounded-control bg-danger/12 px-3 py-1.5 text-sm font-semibold text-danger">
                    Revoke
                  </button>
                </form>
              </li>
            ))}
          </ul>
        ) : (
          <Empty icon={<AppWindow className="size-7" aria-hidden="true" />} text="No apps yet. Sign in to one with your Poweur ID and it appears here." />
        )}
      </Card>
      {d.consents.length > 0 && (
        <p className="mt-2 px-1 text-[13px] text-muted">Revoking ends the app's current access and makes it ask again. It does not delete the account the app keeps.</p>
      )}

      <h2 id="sign-ins" className="mt-10 mb-3 text-[19px] font-bold">
        Recent sign-ins
      </h2>
      <Card className="p-0 sm:p-0">
        {d.signIns.length ? (
          <ul className="divide-y divide-sep">
            {d.signIns.map((s, i) => (
              <li key={i} className="flex items-baseline justify-between gap-4 px-5 py-3.5">
                <div className="min-w-0">
                  <p className="truncate font-semibold">{s.clientName || page.service.name}</p>
                  <p className="truncate text-[13px] text-muted">
                    {[s.userAgent, s.crossDevice && "approved on another device", s.sessionKey && "session key"].filter(Boolean).join(" · ")}
                  </p>
                </div>
                <time className="shrink-0 text-[13px] text-muted">{formatDate(s.at, true)}</time>
              </li>
            ))}
          </ul>
        ) : (
          <Empty icon={<History className="size-7" aria-hidden="true" />} text="No sign-ins recorded." />
        )}
      </Card>
    </Shell>
  );
}

export function Empty({ icon, text }: { icon: ReactNode; text: string }) {
  return (
    <div className="flex flex-col items-center gap-3 px-6 py-10 text-center text-sm text-muted">
      <span className="flex size-14 items-center justify-center rounded-full bg-accent-soft text-accent">{icon}</span>
      {text}
    </div>
  );
}
