/**
 * The joining half of pairing (EPIC-011 E11-T8): this device has no key yet.
 * It commits to an ephemeral key, shows a QR of the pairing link and the
 * short code, and polls. When the other device answers, it reveals its key;
 * on the typed path both screens then show six digits to compare. Closing the
 * panel frees the pairing, so the relay's per-identity cap does not fill.
 */
import { useEffect, useRef, useState } from "react";
import { formatShortCode, pairingAppLink, pairingLink, type EnrollStep } from "@poweur/client";
import { Copy } from "lucide-react";
import { adoptIdentity, enrollApiForJoin } from "../actions/identity";
import { joinIdentityFor } from "../lib/claim";
import { cn } from "../lib/cn";
import { describeJoinError, startJoinPoll } from "../lib/enroll-wait.js";
import { deviceLabel } from "../lib/keystore.js";
import { getConfig, isShellRuntime } from "../lib/storage.js";
import { jwksFromSeed, toBase64url } from "../lib/vault.js";
import { useSession } from "../state/session";
import { closePanel, openPanel, setLoading, toast } from "../state/ui";
import { Button } from "../ui/Button";
import { FormGroup, Input, Label } from "../ui/Field";
import { QRCode } from "../ui/QRCode";

export function openJoinDevicePanel(knownIdentity = "") {
  openPanel("Add this device", () => <JoinDevice knownIdentity={knownIdentity} />);
}

type Phase =
  | { kind: "form" }
  | { kind: "opening" }
  | { kind: "waiting"; code: string; link: string; appLink: string }
  | { kind: "retry"; message: string };

/** What will approve this device decides what the QR must open. */
type Approver = "app" | "browser" | "terminal";
const APPROVER_KEY = "poweur:pair-approver";

function readApprover(): Approver {
  try {
    const v = localStorage.getItem(APPROVER_KEY);
    return v === "browser" || v === "terminal" ? v : "app";
  } catch {
    return "app";
  }
}

const APPROVERS: { id: Approver; label: string }[] = [
  { id: "app", label: "Poweur app" },
  { id: "browser", label: "Browser" },
  { id: "terminal", label: "Terminal" },
];

function CopyCode({ id, value, shown, what }: { id?: string; value: string; shown?: string; what: string }) {
  return (
    <div className="flex items-center justify-center gap-2">
      <span id={id} className="rendezvous-code font-mono text-[28px] font-bold tracking-[.1em] select-all">
        {shown ?? value}
      </span>
      <Button
        variant="ghost"
        size="sm"
        aria-label={`Copy the ${what}`}
        className="w-auto"
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(value);
            toast(`${what[0]!.toUpperCase()}${what.slice(1)} copied`, "success", 2000);
          } catch {
            toast("Copy failed — select it and copy it manually", "warning");
          }
        }}
      >
        <Copy className="size-4" aria-hidden="true" />
      </Button>
    </div>
  );
}

/**
 * The code, carried the way the approving side can open it: the app from a
 * phone camera needs poweur://, a browser the https link, a terminal a
 * command. The typed code works everywhere.
 */
function OfferedCode({ phase, identity }: { phase: { code: string; link: string; appLink: string }; identity: string }) {
  const [approver, setApprover] = useState<Approver>(readApprover);
  const choose = (next: Approver) => {
    setApprover(next);
    try {
      localStorage.setItem(APPROVER_KEY, next);
    } catch {
      /* remembered for this visit only */
    }
  };
  const command = `poweur key approve '${phase.link}' --seed …`;
  return (
    <>
      <p className="mb-2 text-[13px] text-muted">Approve with</p>
      <div role="tablist" aria-label="Approve with" className="mx-auto mb-4 grid max-w-[340px] grid-cols-3 gap-1 rounded-control bg-surface-2 p-1">
        {APPROVERS.map((a) => (
          <button
            key={a.id}
            id={`pair-approver-${a.id}`}
            type="button"
            role="tab"
            aria-selected={approver === a.id}
            onClick={() => choose(a.id)}
            className={cn(
              "rounded-[8px] px-2 py-2 text-[13px] font-semibold text-muted",
              approver === a.id && "bg-surface text-fg shadow-card",
            )}
          >
            {a.label}
          </button>
        ))}
      </div>
      {approver === "app" && (
        <>
          <QRCode value={phase.appLink} label="Pairing code for the Poweur app" className="mx-auto w-[min(240px,70vw)]" />
          <p className="mt-3 text-[15px] font-semibold">Scan with your phone's camera</p>
          <p className="text-[13px] text-muted">It opens the Poweur app, which asks you to approve.</p>
        </>
      )}
      {approver === "browser" && (
        <>
          <QRCode value={phase.link} label="Pairing link for a browser" className="mx-auto w-[min(240px,70vw)]" />
          <p className="mt-3 text-[15px] font-semibold">Scan with the device where you use Poweur in a browser</p>
          <p className="text-[13px] text-muted">It opens Poweur there, which asks you to approve.</p>
        </>
      )}
      {approver === "terminal" && (
        <div className="text-left">
          <p className="mb-2 text-[13px] text-muted">On a machine that has {identity || "this identity"}, run:</p>
          <div className="flex items-start gap-2 rounded-control bg-surface-2 p-3">
            <code id="pair-command" className="min-w-0 flex-1 font-mono text-[12px] break-all">
              {command}
            </code>
            <Button
              variant="ghost"
              size="sm"
              aria-label="Copy the command"
              className="w-auto shrink-0"
              onClick={async () => {
                try {
                  await navigator.clipboard.writeText(command);
                  toast("Command copied", "success", 2000);
                } catch {
                  toast("Copy failed — select it and copy it manually", "warning");
                }
              }}
            >
              <Copy className="size-4" aria-hidden="true" />
            </Button>
          </div>
        </div>
      )}
      <p className="mt-5 mb-1.5 text-[13px] text-muted">{approver === "terminal" ? "or with the code" : "or enter this code there"}</p>
      <CopyCode id="join-code" value={formatShortCode(phase.code)} what="code" />
      {approver !== "terminal" && (
        <p className="mt-2 text-[13px] text-muted">
          Settings → Keys &amp; devices → <strong>Add a device</strong>
        </p>
      )}
      {/* For tests and screen readers: every form of the link, whichever tab is open. */}
      <span hidden data-pair-link={phase.link} data-pair-app-link={phase.appLink} />
    </>
  );
}

export function JoinDevice({ knownIdentity = "" }: { knownIdentity?: string }) {
  const mode = useSession((state) => state.mode);
  // Prefer the identity the door already named over re-reading the mode.
  const [known] = useState(
    () => String(knownIdentity || "").trim().toLowerCase() || (mode.mode === "identity" ? mode.subject ?? "" : ""),
  );
  const here = isShellRuntime() ? "device" : "browser";
  const [phase, setPhase] = useState<Phase>(known ? { kind: "opening" } : { kind: "form" });
  const [waitText, setWaitText] = useState("Waiting for your other device…");
  const [progress, setProgress] = useState<Exclude<EnrollStep, { state: "delivered" }>>({ state: "offered" });
  const [expiry, setExpiry] = useState("");
  const identityField = useRef<HTMLInputElement>(null);
  const live = useRef<{ session: any; joining: string; poller: any; enroll: any }>({
    session: null,
    joining: "",
    poller: null,
    enroll: null,
  });

  const abandon = () => {
    const current = live.current;
    current.poller?.stop();
    current.poller = null;
    if (current.session && current.joining) {
      current.enroll?.cancel(current.joining, current.session).catch(() => {});
      current.session = null;
    }
  };

  const start = async () => {
    const identity = known || joinIdentityFor(identityField.current?.value ?? "", mode, getConfig().parentDomain);
    if (!identity) {
      toast("Enter your identity", "warning");
      return;
    }
    // A second tap must not leave the previous poller firing.
    abandon();

    setLoading(true, "Opening a secure channel…");
    try {
      // Unauthenticated by necessity — this device has no key yet.
      const { enroll, relayUrl } = await enrollApiForJoin(identity);
      const session = await enroll.offer(identity, deviceLabel());
      Object.assign(live.current, { enroll, session, joining: identity });
      setLoading(false);
      setWaitText("Waiting for your other device…");
      setProgress({ state: "offered" });
      // The link opens the app on the relay this pairing runs through — for a
      // hosted identity, its own host, where the other device keeps its keys.
      // Never this page's origin: in the shell that is capacitor://localhost.
      const link = pairingLink(`${String(relayUrl).replace(/\/+$/, "")}/app/`, identity, session.rendezvousId, session.commitment);
      setPhase({
        kind: "waiting",
        code: session.rendezvousId,
        link,
        appLink: pairingAppLink(identity, session.rendezvousId, session.commitment),
      });

      const deadlineMs = Date.parse(session.expiresAt) ? Math.max(0, Date.parse(session.expiresAt) - Date.now()) : undefined;
      live.current.poller = startJoinPoll({
        claim: async () => {
          const step: EnrollStep = await enroll.step(identity, session);
          if (step.state === "delivered") return step.seed;
          setProgress(step);
          setWaitText(step.state === "offered" ? "Waiting for your other device…" : "Waiting for you to approve on your other device…");
          return null;
        },
        deadlineMs,
        onTick: (text: string) => setExpiry(text),
        onSeed: async (seedBytes: Uint8Array) => {
          live.current.session = null;
          closePanel();
          try {
            // Adopt only keys that are this identity's published keys.
            if (!(await enroll.publishedKeyMatches(identity, seedBytes))) {
              throw new Error(`The keys received are not ${identity}'s — nothing was saved. Start again.`);
            }
            const derived: any = jwksFromSeed(seedBytes);
            await adoptIdentity({
              identity,
              relayUrl,
              signingJWK: derived.signingJWK,
              encJWK: derived.encJWK,
              seed: toBase64url(seedBytes),
              label: deviceLabel(),
            });
            toast(`${identity} is set up on this device`, "success", 5000);
          } catch (error) {
            setLoading(false);
            toast((error as Error).message, "error", 9000);
          }
        },
        onError: (error: unknown, { terminal }: { terminal: boolean }) => {
          const detail = describeJoinError(error);
          if (!terminal) {
            setWaitText(detail);
            return;
          }
          toast(detail, "error", 9000);
          setPhase({ kind: "retry", message: detail });
        },
      });
    } catch (error) {
      setLoading(false);
      const detail = (error as Error).message || "Could not open a secure channel";
      toast(detail, "error", 8000);
      if (known) setPhase({ kind: "retry", message: detail });
    }
  };

  useEffect(() => {
    // The door already named the subject: skip the form and show the code.
    if (known) void start();
    return abandon;
  }, []);

  return (
    <div>
      <p className="mb-3 text-[13px] text-muted">
        {known ? (
          <>
            Pair this {here} with a device that already has <strong>{known}</strong>.
          </>
        ) : (
          "Enter your identity, then pair this device with one you already use."
        )}
      </p>

      {!known && (
        <>
          <FormGroup>
            <Label htmlFor="join-identity">Your Poweur ID</Label>
            <Input
              ref={identityField}
              id="join-identity"
              type="text"
              placeholder="alice.poweur.net"
              autoCapitalize="none"
              autoCorrect="off"
              autoComplete="off"
              spellCheck={false}
              inputMode="url"
            />
          </FormGroup>
          {phase.kind !== "waiting" && (
            <Button id="btn-join-start" onClick={() => void start()}>
              Show my code
            </Button>
          )}
        </>
      )}

      <div id="join-state">
        {phase.kind === "opening" && (
          <p id="join-wait" className="text-[13px] text-muted">
            Opening a secure channel…
          </p>
        )}
        {phase.kind === "retry" && (
          <>
            <p className="val-warn mt-3 text-[13px] text-warning">{phase.message}</p>
            {known && (
              <Button id="btn-join-start" className="mt-2" onClick={() => void start()}>
                Try again
              </Button>
            )}
          </>
        )}
        {phase.kind === "waiting" && (
          <div className="mt-2 text-center">
            {progress.state === "offered" && <OfferedCode phase={phase} identity={live.current.joining} />}
            {progress.state === "compare" && (
              <div id="join-compare" className="rounded-card bg-surface-2 px-4 py-5">
                <p className="text-[15px]">Your other device shows a number. Check it's the same:</p>
                <p id="join-sas" className="sas-code my-3 font-mono text-[40px] leading-none font-extrabold tracking-[.1em]">
                  {progress.sas.slice(0, 3)} {progress.sas.slice(3)}
                </p>
                <p className="text-[13px] text-muted">Only compare — don't type it anywhere. Then approve there.</p>
              </div>
            )}
            {progress.state === "scan" && (
              <p id="join-scan" className="rounded-card bg-surface-2 px-4 py-5 text-[15px] font-semibold">Approve on your other device</p>
            )}
            <p id="join-wait" className="mt-4 text-[13px] text-muted">
              {waitText}
            </p>
            <p id="join-expiry" className="text-[13px] text-muted">
              {expiry}
            </p>
            <Button id="btn-join-check" variant="ghost" size="sm" className="mt-2 w-auto" onClick={() => live.current.poller?.checkNow()}>
              Check now
            </Button>
          </div>
        )}
      </div>
    </div>
  );
}
