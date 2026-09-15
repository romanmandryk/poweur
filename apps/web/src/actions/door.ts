/**
 * Is this identity host's own name claimed — and if not, may it be? (E15-T9)
 *
 * "Absent" and "claimable" are different answers: `admin.poweur.net` has no
 * document and can never be claimed. `GET /hosted/availability` answers both
 * halves; outside `hosted_domains` it is not this relay's to decide, so that
 * case asks for the document instead and offers no claim either way.
 */
import { identityApiFor } from "../lib/client.js";
import { resolveMode } from "../lib/mode.js";
import { defaultRelayUrl } from "../lib/storage.js";
import { useData, type DataFields } from "../state/data";
import { useSession, type ModeInfo } from "../state/session";

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

/** Test seam. */
export function resetDoorProbeForTests() {
  inFlight = null;
}
