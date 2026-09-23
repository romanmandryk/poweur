/**
 * Approve a new device (EPIC-011 E11-T8, the approving half).
 *
 * Reached from Settings → Add a device, or by opening a pairing link — the
 * new device's QR, scanned with this phone's camera. Scanned, the link
 * carries the new device's commitment, so its key is checked with nothing to
 * compare. Typed, both screens show six digits to compare before the keys go.
 */
import { useEffect, useRef, useState } from "react";
import { formatShortCode, pairingAppLink, parsePairingLink, type ApproverSession, type ApproverStep } from "@poweur/client";
import { CircleCheck, Laptop, ScanLine, ShieldAlert } from "lucide-react";
import { enrollApiForJoin } from "../actions/identity";
import { activeClient } from "../actions/relay";
import { getUnlockedKeys, loadIdentityRecord } from "../lib/storage.js";
import { fromBase64url } from "../lib/vault.js";
import { useData } from "../state/data";
import { useRoute } from "../state/route";
import { switchIdentity, useSession } from "../state/session";
import { toast } from "../state/ui";
import { Button } from "../ui/Button";
import { Input, Label, Note } from "../ui/Field";
import { SubPage } from "../ui/Layout";

type Phase =
  | { kind: "input" }
  | { kind: "waiting"; session: ApproverSession }
  | { kind: "confirm"; session: ApproverSession; step: Extract<ApproverStep, { state: "revealed" }> }
  | { kind: "done" }
  | { kind: "error"; message: string };

const WAIT_MS = 2000;

export function PairDevice() {
  const identity = useSession((state) => state.identity);
  const unlocked = useSession((state) => state.unlocked);
  const pendingLink = useData((state) => state.pairInput);
  const push = useRoute((state) => state.push);
  const pop = useRoute((state) => state.pop);
  const field = useRef<HTMLInputElement>(null);
  const [phase, setPhase] = useState<Phase>({ kind: "input" });
  const live = useRef<{ enroll: any; timer: ReturnType<typeof setTimeout> | null; stopped: boolean }>({
    enroll: null,
    timer: null,
    stopped: false,
  });

  const stop = () => {
    live.current.stopped = true;
    if (live.current.timer) clearTimeout(live.current.timer);
  };
  useEffect(() => stop, []);

  const begin = async (input: string) => {
    const client = activeClient();
    if (!client || !identity) return;
    const keys: any = getUnlockedKeys();
    if (!keys?.seed) {
      setPhase({ kind: "error", message: "Only identities created with a seed can hand their keys to a new device — see Recovery kit." });
      return;
    }
    live.current.stopped = false;
    try {
      const { enroll } = await enrollApiForJoin(identity);
      live.current.enroll = enroll;
      const session: ApproverSession = await enroll.begin(client.signer, identity, input);
      useData.setState({ pairInput: "" });
      setPhase({ kind: "waiting", session });
      const poll = async () => {
        if (live.current.stopped) return;
        try {
          const step: ApproverStep = await enroll.wait(client.signer, identity, session);
          if (step.state === "revealed") {
            setPhase({ kind: "confirm", session, step });
            return;
          }
        } catch (error) {
          setPhase({ kind: "error", message: (error as Error).message });
          return;
        }
        live.current.timer = setTimeout(poll, WAIT_MS);
      };
      void poll();
    } catch (error) {
      setPhase({ kind: "error", message: (error as Error).message || "No new device is waiting with that code." });
    }
  };

  // A pairing link names the identity it joins: switch to it if this device
  // holds it, refuse plainly if not.
  const linkIdentity = pendingLink ? parsePairingLink(pendingLink)?.identity ?? "" : "";
  const holdsLinkIdentity = !linkIdentity || Boolean(loadIdentityRecord(linkIdentity));
  useEffect(() => {
    if (linkIdentity && holdsLinkIdentity && linkIdentity !== identity) switchIdentity(linkIdentity);
  }, [linkIdentity, holdsLinkIdentity, identity]);

  // A pairing link opened on this device goes straight to approval.
  useEffect(() => {
    if (pendingLink && unlocked && phase.kind === "input" && (!linkIdentity || linkIdentity === identity)) void begin(pendingLink);
  }, [pendingLink, unlocked, identity]);

  if (!holdsLinkIdentity) {
    // Most often the keys are in the Poweur app on this same phone, and the
    // camera opened the browser link here: hand the pairing to the app.
    const parts = parsePairingLink(pendingLink);
    const appLink = parts ? pairingAppLink(parts.identity, parts.code, parts.commitment) : "";
    return (
      <SubPage title="Add a device">
        <p id="pair-error" className="text-[15px]">
          This browser doesn't hold <strong>{linkIdentity}</strong>.
        </p>
        {appLink && (
          <a
            id="pair-open-app"
            href={appLink}
            className="mt-4 block rounded-button bg-accent p-4 text-center text-[17px] font-semibold text-white no-underline"
          >
            Open in the Poweur app
          </a>
        )}
        <p className="mt-3 text-[13px] text-muted">Or open the link on another device that has it, or type the code shown on the new device there.</p>
        <Button className="mt-4" variant="ghost" onClick={() => { useData.setState({ pairInput: "" }); pop(); }}>
          Close
        </Button>
      </SubPage>
    );
  }

  const approve = async () => {
    if (phase.kind !== "confirm") return;
    const client = activeClient();
    const keys: any = getUnlockedKeys();
    if (!client || !identity || !keys?.seed) return;
    try {
      await live.current.enroll.approve(client.signer, identity, phase.session, phase.step, fromBase64url(keys.seed));
      setPhase({ kind: "done" });
    } catch (error) {
      toast((error as Error).message, "error", 8000);
    }
  };

  if (!unlocked) {
    return (
      <SubPage title="Add a device">
        <p className="mb-4 text-muted">Unlock {identity || "your identity"} to add a device to it.</p>
        <Button id="btn-pair-unlock" variant="passkey" onClick={() => push("unlock", { returnTo: "pair" })}>
          Unlock
        </Button>
      </SubPage>
    );
  }

  return (
    <SubPage title="Add a device">
      {phase.kind === "input" && (
        <>
          <p className="mb-4 text-[15px] text-muted">
            On the new device, choose <strong className="text-fg">Add this device</strong>. Scan its code with this phone's camera, or type the
            code it shows.
          </p>
          <Label htmlFor="pair-code">Code from the new device</Label>
          <Input
            ref={field}
            id="pair-code"
            type="text"
            placeholder="K7QM-4XP2"
            autoCapitalize="characters"
            autoCorrect="off"
            autoComplete="off"
            spellCheck={false}
            className="text-center font-mono text-2xl tracking-[.12em] uppercase"
            onKeyDown={(event) => {
              if (event.key === "Enter") void begin(field.current?.value ?? "");
            }}
          />
          <Button id="btn-pair-continue" className="mt-4" onClick={() => void begin(field.current?.value ?? "")}>
            Continue
          </Button>
        </>
      )}

      {phase.kind === "waiting" && (
        <div className="py-6 text-center">
          <p className="font-semibold">Connecting to {phase.session.label || "the new device"}…</p>
          <p className="mt-1 text-sm text-muted">Keep the new device's screen open.</p>
        </div>
      )}

      {phase.kind === "confirm" && phase.session.mode === "compare" && (
        <div id="pair-compare" className="text-center">
          <p className="text-[15px] text-muted">Does {phase.session.label || "the new device"} show this number?</p>
          <p id="pair-sas" aria-label="Number to compare" className="my-5 font-mono text-[40px] leading-none font-extrabold tracking-[.1em]">
            {phase.step.sas.slice(0, 3)} {phase.step.sas.slice(3)}
          </p>
          <Button id="btn-pair-approve" onClick={() => void approve()}>
            Yes, they match — add it
          </Button>
          <Button id="btn-pair-reject" variant="ghost" className="mt-2.5" onClick={() => { stop(); pop(); }}>
            No — cancel
          </Button>
          <Note className="flex items-start gap-2 text-left">
            <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
            Different numbers mean a device you don't control is trying to join. Cancel and start again.
          </Note>
        </div>
      )}

      {phase.kind === "confirm" && phase.session.mode === "scan" && (
        <div id="pair-scan" className="text-center">
          <span className="mx-auto mb-4 flex size-14 items-center justify-center rounded-full bg-accent-soft text-accent">
            <ScanLine className="size-7" aria-hidden="true" />
          </span>
          <p className="text-[17px] font-semibold">Add {phase.session.label || "this device"} to {identity}?</p>
          <p className="mt-1 mb-5 text-sm text-muted">It gets your keys, and can read your messages and files.</p>
          <Button id="btn-pair-approve" onClick={() => void approve()}>
            Add device
          </Button>
          <Button id="btn-pair-reject" variant="ghost" className="mt-2.5" onClick={() => { stop(); pop(); }}>
            Cancel
          </Button>
        </div>
      )}

      {phase.kind === "done" && (
        <div id="pair-done" className="py-6 text-center">
          <CircleCheck className="mx-auto mb-3 size-12 text-success" strokeWidth={1.6} aria-hidden="true" />
          <p className="text-[17px] font-semibold">Keys sent</p>
          <p className="mt-1 text-sm text-muted">The new device finishes setting up on its own.</p>
          <Button className="mt-5" variant="ghost" onClick={pop}>
            Done
          </Button>
        </div>
      )}

      {phase.kind === "error" && (
        <div className="py-4">
          <p id="pair-error" className="mb-4 text-sm text-danger">{phase.message}</p>
          <Button variant="ghost" onClick={() => setPhase({ kind: "input" })}>
            Try again
          </Button>
        </div>
      )}

      {phase.kind !== "done" && phase.kind !== "input" && phase.kind !== "error" && (
        <p className="mt-6 flex items-center justify-center gap-1.5 text-[13px] text-muted">
          <Laptop className="size-3.5" aria-hidden="true" /> Code {formatShortCode(phase.session.code)}
        </p>
      )}
    </SubPage>
  );
}
