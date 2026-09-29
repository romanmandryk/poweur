/**
 * Is this identity host's own name claimed — and if not, may it be? (E15-T9)
 *
 * "Absent" and "claimable" are different answers: `admin.poweur.net` has no
 * document and can never be claimed. `GET /hosted/availability` answers both
 * halves; outside `hosted_domains` it is not this relay's to decide, so that
 * case asks for the document instead and offers no claim either way.
 */
import { existingIdFrom, identityAppUrl } from "../lib/claim";
import { chatTargetQuery } from "../lib/visit";
import { identityApiFor } from "../lib/client.js";
import { resolveMode } from "../lib/mode.js";
import { defaultRelayUrl } from "../lib/storage.js";
import { useData, type DataFields } from "../state/data";
import { useSession, type ModeInfo } from "../state/session";
import { setLoading } from "../state/ui";

type Door = DataFields["door"];

let inFlight: Promise<void> | null = null;

const setDoor = (door: Door) => useData.setState({ door });

export function probeDoor(info: ModeInfo, identity: string | null) {
  if (info.mode !== "identity" || identity || !info.subject) return;
  const current = useData.getState().door;
  if (current.subject === info.subject && current.state !== "idle") return;
  if (inFlight) return;

  const subject = info.subject;
  setDoor({ subject, state: "checking", message: "", policy: null });
  const relayUrl = defaultRelayUrl();
  if (!relayUrl) {
    setDoor({ subject, state: "offline", message: "", policy: null });
    return;
  }

  const api: any = identityApiFor(relayUrl);
  const hosted = (info.hostedDomains ?? []).includes(info.domain ?? "");

  const verdict: Promise<Omit<Door, "subject">> = hosted
    ? api.availability(info.handle, info.domain).then((answer: any) => {
        if (answer.reason === "taken") return { state: "claimed", message: "", policy: answer.policy };
        if (answer.available) return { state: "claimable", message: "", policy: answer.policy };
        // reserved / blocked / too_short / charset — the relay's own words.
        return { state: "unavailable", message: answer.message, policy: answer.policy };
      })
    : api.get(subject).then(
        () => ({ state: "claimed", message: "", policy: null }),
        (error: any) => {
          if (error?.status === 404) {
            return { state: "unavailable", message: "This relay does not host names under this domain.", policy: null };
          }
          throw error;
        },
      );

  inFlight = verdict
    .then((next) => setDoor({ subject, ...next }))
    .catch(() => setDoor({ subject, state: "offline", message: "", policy: null }))
    .finally(() => {
      inFlight = null;
    });
}

/** "Try again" on an offline door: ask the relay again, then re-probe. */
export function retryDoor() {
  useData.setState((state) => ({ door: { ...state.door, state: "idle" } }));
  void resolveMode({ force: true }).then((info: ModeInfo) => useSession.setState({ mode: info }));
}

export type ExistingIdVerdict =
  | { state: "found"; identity: string; url: string }
  | { state: "missing"; identity: string; claimable: boolean; message: string }
  | { state: "invalid" | "elsewhere" | "offline"; message: string };

/**
 * "I already have an ID" on the launcher. The keys for a hosted name live on
 * its own origin (the claim hand-off put them there), so there is nothing to
 * sign in to here: find out whether the name exists and, if it does, go to
 * its door, which offers the passkey and adding this device.
 */
export async function lookUpExistingId(raw: string, info: ModeInfo): Promise<ExistingIdVerdict> {
  const example = info.domain ? `alice.${info.domain}` : "alice.example.com";
  const target = existingIdFrom(raw, info);
  if (!target) return { state: "invalid", message: `Enter your ID, like ${example}.` };
  const { identity } = target;
  if (!target.hosted) {
    return { state: "elsewhere", message: `${identity} isn't hosted here. Open the app on the relay that hosts it.` };
  }

  let answer: any;
  try {
    answer = await (identityApiFor(defaultRelayUrl()) as any).availability(target.handle, target.domain);
  } catch {
    return { state: "offline", message: "Can't reach the relay right now. Try again in a moment." };
  }
  if (answer?.reason === "taken") return { state: "found", identity, url: identityAppUrl(identity) };
  const claimable = Boolean(answer?.available);
  return {
    state: "missing",
    identity,
    claimable,
    message: claimable ? `${identity} doesn't exist yet. You can claim it above.` : `${identity} doesn't exist.`,
  };
}

/** Leave the launcher for an existing identity's own door. */
export function goToIdentityDoor(identity: string, url: string) {
  setLoading(true, `Taking you to ${identity}…`);
  globalThis.location.assign(url + chatTargetQuery());
}

/** Test seam. */
export function resetDoorProbeForTests() {
  inFlight = null;
}
