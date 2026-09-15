/**
 * The joining half of the enrollment ceremony (E11-T3): this device has no key
 * yet. It opens a rendezvous and polls until a device that holds the seed
 * approves. Closing the panel frees the rendezvous, so the relay's
 * per-identity cap does not fill with abandoned ceremonies.
 */
import { useEffect, useRef, useState } from "react";
import { adoptIdentity, enrollApiForJoin } from "../actions/identity";
import { joinIdentityFor } from "../lib/claim";
import { describeJoinError, startJoinPoll } from "../lib/enroll-wait.js";
import { deviceLabel } from "../lib/keystore.js";
import { getConfig, isShellRuntime } from "../lib/storage.js";
import { jwksFromSeed, toBase64url } from "../lib/vault.js";
import { useSession } from "../state/session";
import { closePanel, openPanel, setLoading, toast } from "../state/ui";
import { Button } from "../ui/Button";
import { Notice } from "../ui/Display";
import { FormGroup, Input, Label } from "../ui/Field";

export function openJoinDevicePanel(knownIdentity = "") {
  openPanel("Add this device", () => <JoinDevice knownIdentity={knownIdentity} />);
}

type Phase =
  | { kind: "form" }
  | { kind: "opening" }
  | { kind: "waiting"; rendezvousId: string; sas: string }
  | { kind: "retry"; message: string };

export function JoinDevice({ knownIdentity = "" }: { knownIdentity?: string }) {
  const mode = useSession((state) => state.mode);
  // Prefer the identity the door already named over re-reading the mode.
  const [known] = useState(
    () => String(knownIdentity || "").trim().toLowerCase() || (mode.mode === "identity" ? mode.subject ?? "" : ""),
  );
  const here = isShellRuntime() ? "device" : "browser";
  const [phase, setPhase] = useState<Phase>(known ? { kind: "opening" } : { kind: "form" });
  const [waitText, setWaitText] = useState("Waiting for approval…");
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
      setWaitText("Waiting for approval…");
      setPhase({ kind: "waiting", rendezvousId: session.rendezvousId, sas: session.sas });

      const deadlineMs = Date.parse(session.expiresAt) ? Math.max(0, Date.parse(session.expiresAt) - Date.now()) : undefined;
      live.current.poller = startJoinPoll({
        claim: () => enroll.claim(identity, session),
        deadlineMs,
        onTick: (text: string) => setExpiry(text),
        onSeed: async (seedBytes: Uint8Array) => {
          live.current.session = null;
          closePanel();
          try {
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
            This {here} will show a code to type on a device that already has <strong>{known}</strong>.
          </>
        ) : (
          "Enter your identity. This device will show a code to type on a device you already use."
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
          <>
            <Notice className="mt-3.5">
              <p>
                On a device you already use, open <strong>Settings → Keys &amp; devices → Add a device</strong> and enter:
              </p>
              <p className="rendezvous-code my-2 rounded-control bg-surface-2 px-3 py-2.5 font-mono text-[15px] break-all select-all">
                {phase.rendezvousId}
              </p>
              <Button
                id="btn-copy-rendezvous"
                variant="ghost"
                size="sm"
                onClick={async () => {
                  try {
                    await navigator.clipboard.writeText(phase.rendezvousId);
                    toast("Request code copied", "success", 2000);
                  } catch {
                    toast("Copy failed — select the code and copy it manually", "warning");
                  }
                }}
              >
                Copy request code
              </Button>
              <p className="mt-3.5">Then check it shows these six digits — they confirm it is really this device:</p>
              <p className="sas-code mt-2.5 mb-1 text-center font-mono text-[34px] font-bold tracking-[.18em]">{phase.sas}</p>
            </Notice>
            <p id="join-wait" className="text-[13px] text-muted">
              {waitText}
            </p>
            <p id="join-expiry" className="text-[13px] text-muted">
              {expiry}
            </p>
            <Button id="btn-join-check" variant="ghost" size="sm" className="mt-2" onClick={() => live.current.poller?.checkNow()}>
              Check now
            </Button>
          </>
        )}
      </div>
    </div>
  );
}
