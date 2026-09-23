/**
 * The joining half of pairing (EPIC-011 E11-T8): this device has no key yet.
 * It commits to an ephemeral key, shows a QR of the pairing link and the
 * short code, and polls. When the other device answers, it reveals its key;
 * on the typed path both screens then show six digits to compare. Closing the
 * panel frees the pairing, so the relay's per-identity cap does not fill.
 */
import { useEffect, useRef, useState } from "react";
import { formatShortCode, pairingLink, type EnrollStep } from "@poweur/client";
import { Copy } from "lucide-react";
import { adoptIdentity, enrollApiForJoin } from "../actions/identity";
import { joinIdentityFor } from "../lib/claim";
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
  | { kind: "waiting"; code: string; link: string }
  | { kind: "retry"; message: string };

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
      setPhase({ kind: "waiting", code: session.rendezvousId, link });

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
            {progress.state === "offered" && (
              <>
                <QRCode value={phase.link} label="Pairing code for your other device" className="mx-auto w-[min(240px,70vw)]" />
                <p className="mt-3 text-[15px] font-semibold">Scan with your other device's camera</p>
                <p className="mt-5 mb-1.5 text-[13px] text-muted">or enter this code on it</p>
                <div className="flex items-center justify-center gap-2">
                  <span id="join-code" className="rendezvous-code font-mono text-[28px] font-bold tracking-[.1em] select-all">
                    {formatShortCode(phase.code)}
                  </span>
                  <Button
                    id="btn-copy-rendezvous"
                    variant="ghost"
                    size="sm"
                    aria-label="Copy the code"
                    className="w-auto"
                    onClick={async () => {
                      try {
                        await navigator.clipboard.writeText(formatShortCode(phase.code));
                        toast("Code copied", "success", 2000);
                      } catch {
                        toast("Copy failed — select the code and copy it manually", "warning");
                      }
                    }}
                  >
                    <Copy className="size-4" aria-hidden="true" />
                  </Button>
                </div>
                <p className="mt-2 text-[13px] text-muted">
                  There: Settings → Keys &amp; devices → <strong>Add a device</strong>
                </p>
              </>
            )}
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
