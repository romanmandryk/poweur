import { Smartphone } from "lucide-react";
import type { HandoffData, Page } from "../lib/page";
import { LinkButton } from "../ui/Button";
import { Card, Chip } from "../ui/Display";
import { Shell } from "../ui/Shell";

/**
 * A phone's camera opened the QR on the screen that started a sign-in. Offer
 * the ways this phone can approve it; the code on that screen is asked for
 * next, which is what proves the person here is looking at it.
 */
export function Handoff({ page }: { page: Page<HandoffData> }) {
  const d = page.data;
  const own = d.signers.filter((s) => s.own);
  const buttons = own.length ? own : d.signers;
  const links = own.length ? d.signers.filter((s) => !s.own) : [];
  return (
    <Shell page={page} narrow>
      <Card className="animate-fade-in-up">
        {d.client && <p className="mb-1 text-sm font-medium text-muted">Signing in to {d.client.name}</p>}
        <h1 className="text-[24px] leading-tight font-extrabold tracking-[-.3px]">Approve on this phone</h1>
        <p className="mt-2">
          <Chip tone="accent" id="handoff-identity">
            {d.identity}
          </Chip>
        </p>
        <div className="mt-6 flex flex-col gap-2.5">
          <LinkButton id="handoff-app" href={d.deepLink}>
            <Smartphone className="size-5" aria-hidden="true" /> Open the Poweur app
          </LinkButton>
          {buttons.map((s) => (
            <LinkButton key={s.href} href={s.href} variant="outline" className="handoff-signer">
              Continue at {new URL(s.href).host}
            </LinkButton>
          ))}
          {links.map((s) => (
            <p key={s.href} className="text-center text-[13px] text-muted">
              Or{" "}
              <a href={s.href} className="handoff-signer font-semibold">
                continue at {new URL(s.href).host}
              </a>
            </p>
          ))}
        </div>
        <p className="mt-5 text-[13px] text-muted">
          You'll be asked for the 2-digit code on the screen you started from. Only approve a sign-in you started yourself.
        </p>
      </Card>
    </Shell>
  );
}
