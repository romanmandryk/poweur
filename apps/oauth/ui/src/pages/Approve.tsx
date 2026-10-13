import { useEffect, useState } from "react";
import { Laptop, Send, Smartphone } from "lucide-react";
import { isHandheld } from "../lib/device";
import type { AwaitData, Page } from "../lib/page";
import { Button, LinkButton } from "../ui/Button";
import { Card, Chip, CopyValue, Disclosure, Spinner } from "../ui/Display";
import { Shell } from "../ui/Shell";

type Status = { status: string; next?: string; error?: string; match?: string };

/**
 * Approve the sign-in. Where the keys are decides what leads: on a desktop,
 * the phone (scan, approve); on a phone, this device. A desktop is never
 * offered "open the app on this device". A number appears only once the
 * request was pushed to the app (EPIC-028): the app asks to pick it.
 */
export function Approve({ page }: { page: Page<AwaitData> }) {
  const d = page.data;
  const [handheld] = useState(() => isHandheld());
  const status = useApprovalStatus(d.txn);
  const [pushedMatch, setPushedMatch] = useState(d.match);
  const match = status.match || pushedMatch;

  return (
    <Shell page={page} narrow>
      <Card className="animate-fade-in-up">
        {d.client && <p className="mb-1 text-sm font-medium text-muted">Signing in to {d.client.name}</p>}
        <h1 className="text-[24px] leading-tight font-extrabold tracking-[-.3px]">
          {handheld ? "Approve this sign-in" : "Approve on your phone"}
        </h1>
        <p className="mt-2 flex flex-wrap items-center gap-2 text-sm">
          <Chip tone="accent" id="approve-identity">
            {d.identity}
          </Chip>
          <a href={`/t/${d.txn}?change=1`} className="font-medium">
            Not you?
          </a>
        </p>

        {handheld ? <ThisDeviceFirst d={d} onMatch={setPushedMatch} /> : <PhoneFirst d={d} onMatch={setPushedMatch} />}

        {match && status.status === "pending" && <PickNumber match={match} />}
        <StatusLine status={status} />
      </Card>
      <form method="post" action={`/t/${d.txn}/cancel`} className="mt-4 text-center">
        <button type="submit" id="approve-cancel" className="text-sm font-medium text-muted hover:text-fg">
          Cancel
        </button>
      </form>
    </Shell>
  );
}

type OnMatch = (match: string) => void;

function PhoneFirst({ d, onMatch }: { d: AwaitData; onMatch: OnMatch }) {
  return (
    <>
      <OtherDevice d={d} />
      <PushButton d={d} onMatch={onMatch} />
      {d.signers.length > 0 && (
        <div id="this-browser" className="mt-6 border-t border-sep pt-5">
          <p className="mb-3 flex items-center gap-2 text-sm font-semibold text-muted">
            <Laptop className="size-4" aria-hidden="true" /> Or approve in this browser
          </p>
          <BrowserOptions d={d} lead={false} />
        </div>
      )}
    </>
  );
}

function ThisDeviceFirst({ d, onMatch }: { d: AwaitData; onMatch: OnMatch }) {
  return (
    <>
      <div className="mt-6 flex flex-col gap-2.5">
        <BrowserOptions d={d} lead />
        {d.deepLink && (
          <LinkButton id="open-app" href={d.deepLink} variant={d.signers.length ? "outline" : "primary"}>
            <Smartphone className="size-5" aria-hidden="true" /> Open the Poweur app
          </LinkButton>
        )}
      </div>
      <Disclosure id="other-device" summary="Use another device" className="mt-6 border-t border-sep pt-5">
        <OtherDevice d={d} compact />
        <PushButton d={d} onMatch={onMatch} />
      </Disclosure>
    </>
  );
}

/** Scan with the phone's camera; it opens the place that holds the keys. */
function OtherDevice({ d, compact = false }: { d: AwaitData; compact?: boolean }) {
  return (
    <div id="phone-approval" className={compact ? "" : "mt-6"}>
      <div className="grid grid-cols-[132px_1fr] items-center gap-5 sm:grid-cols-[160px_1fr]">
        <div className="qr rounded-control bg-white p-2" aria-label="QR code to approve on your phone" dangerouslySetInnerHTML={{ __html: d.qr }} />
        <p className="text-[15px]">Scan with your phone's camera, then approve there.</p>
      </div>
      <Disclosure summary="Can't scan? Copy the link" className="mt-4">
        <CopyValue id="request-code" value={d.qrLink || d.request} label="Copy" />
        <p className="mt-2 text-[13px] text-muted">
          Open it on the other device. In a terminal: <code>poweur auth approve {d.request}</code>
        </p>
      </Disclosure>
      <p className="mt-4 text-[13px] text-muted">Only approve a sign-in you just started yourself.</p>
    </div>
  );
}

/** After a push: the number the app asks the user to pick. */
function PickNumber({ match }: { match: string }) {
  return (
    <div id="pick-number" className="mt-6 rounded-control bg-surface-2 p-4 text-center">
      <p className="text-sm font-medium text-muted">On your phone, tap</p>
      <p id="match-code" aria-label="Number to tap on your phone" className="mt-1 font-mono text-[40px] leading-none font-extrabold tracking-[.12em] text-accent">
        {match}
      </p>
    </div>
  );
}

function hostOf(href: string): string {
  try {
    return new URL(href).host;
  } catch {
    return href;
  }
}

/**
 * Web signers, named by where they go. The identity's own signer is a
 * button; the operator's fallback is one only when there is nothing else —
 * otherwise a small link, since it works only if the keys are there.
 */
function BrowserOptions({ d, lead }: { d: AwaitData; lead: boolean }) {
  const own = d.signers.filter((s) => s.own);
  const fallback = d.signers.filter((s) => !s.own);
  const buttons = own.length ? own : fallback;
  const links = own.length ? fallback : [];
  return (
    <div className="flex flex-col gap-2.5">
      {buttons.map((s, i) => (
        <div key={s.href}>
          <LinkButton href={s.href} className="signer-link" variant={lead && i === 0 ? "primary" : "outline"}>
            Continue at {hostOf(s.href)}
          </LinkButton>
          {!s.own && <p className="mt-1.5 text-center text-[13px] text-muted">Only if this browser already has your keys there.</p>}
        </div>
      ))}
      {links.map((s) => (
        <p key={s.href} className="text-center text-[13px] text-muted">
          Or{" "}
          <a href={s.href} className="signer-link font-semibold">
            continue at {hostOf(s.href)}
          </a>
        </p>
      ))}
    </div>
  );
}

const PUSH_NOTICES: Record<string, string> = {
  sent: "Sent. Open it in your Poweur app and tap the number shown here.",
  "too-soon": "Already sent — give it a few seconds.",
  "too-many": "Sent as many times as this sign-in allows.",
  paused: "Sending is paused for a few minutes after a wrong number was picked. Scan the code instead.",
  failed: "It could not be sent. Scan the code instead.",
  closed: "This sign-in is no longer waiting.",
};

/**
 * "Send to my Poweur app": the request arrives as a sign-in prompt, and the
 * app asks to pick the number this page then shows. When this browser last
 * signed in as this ID, it is sent as the page opens.
 */
function PushButton({ d, onMatch }: { d: AwaitData; onMatch: OnMatch }) {
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const [left, setLeft] = useState(d.push?.left ?? 0);
  const send = async () => {
    setBusy(true);
    try {
      const res = await fetch(`/t/${d.txn}/push`, {
        method: "POST",
        credentials: "same-origin",
        headers: { Accept: "application/json" },
      });
      const out = (await res.json()) as { notice: string; left?: number; match?: string };
      setNotice(PUSH_NOTICES[out.notice] ?? PUSH_NOTICES.failed);
      if (typeof out.left === "number") setLeft(out.left);
      if (out.match) onMatch(out.match);
    } catch {
      setNotice(PUSH_NOTICES.failed);
    } finally {
      setBusy(false);
    }
  };
  const auto = Boolean(d.autoPush && d.push);
  useEffect(() => {
    if (auto) void send();
    // Once, on open.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [auto]);
  if (!d.push) return null;
  return (
    <div className="mt-4">
      <Button id="push-send" variant="outline" disabled={busy || left <= 0} onClick={() => void send()}>
        <Send className="size-4" aria-hidden="true" /> {auto ? "Send to my Poweur app again" : "Send to my Poweur app"}
      </Button>
      <p id="push-notice" role="status" className="mt-2 text-center text-[13px] text-muted">
        {notice || `Arrives if ${d.push.from} is under Sign-in services in your app.`}
      </p>
    </div>
  );
}

function useApprovalStatus(txn: string): Status {
  const [st, setSt] = useState<Status>({ status: "pending" });
  useEffect(() => {
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    const tick = async () => {
      if (stopped) return;
      try {
        const res = await fetch(`/t/${txn}/status`, { credentials: "same-origin", cache: "no-store" });
        const next = (await res.json()) as Status;
        setSt(next);
        if (next.status === "complete" && next.next) {
          stopped = true;
          location.assign(next.next);
          return;
        }
        if (next.status === "failed" || next.status === "expired") {
          stopped = true;
          return;
        }
      } catch {
        setSt({ status: "offline" });
      }
      timer = setTimeout(tick, 1500);
    };
    timer = setTimeout(tick, 1000);
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  }, [txn]);
  return st;
}

function StatusLine({ status }: { status: Status }) {
  let text = "Waiting for your approval…";
  let tone = "text-muted";
  let spin = true;
  switch (status.status) {
    case "complete":
      text = "Approved. Continuing…";
      tone = "text-success";
      break;
    case "approved":
      text = "Approved — finishing in the browser you approved from…";
      tone = "text-success";
      break;
    case "failed":
    case "expired":
      text = status.error || "This sign-in did not complete.";
      tone = "text-danger";
      spin = false;
      break;
    case "offline":
      text = "Connection lost — retrying…";
      break;
  }
  return (
    <p id="status" role="status" className={`mt-6 flex items-center justify-center gap-2 border-t border-sep pt-5 text-sm font-medium ${tone}`}>
      {spin && <Spinner className="size-3.5" />}
      {text}
    </p>
  );
}
