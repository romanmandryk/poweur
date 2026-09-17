/**
 * Pure helpers behind the front doors and claim screens (from app.js). No DOM,
 * no stores: what a typed handle means, which relay a preset names, and where
 * this app lives so links stay inside the app they were opened from.
 */
import type { ModeInfo } from "../state/session";

/** Custody and passkey probes as `chooseCustody()` / `checkPasskeySupport()` answer them. */
export interface CustodyChoice {
  kind: "native" | "passkey";
  gate?: string;
  biometryKind?: string | null;
  reason?: string;
}

export interface PasskeySupport {
  available?: boolean;
  prf?: boolean | null;
  reason?: string;
}

export interface NamePolicy {
  min_len?: number;
  max_len?: number;
  charset?: string;
}

/**
 * What someone types into a field that already shows its suffix. A visible
 * `.poweur.net` invites pasting the whole address, and an ID copied from
 * elsewhere arrives as `@alice` or `Alice ` (E15-T12) — all the same handle.
 */
export function normalizeHandleInput(raw: string, info: Pick<ModeInfo, "domain" | "hostedDomains"> = {}): string {
  let value = String(raw ?? "").trim().toLowerCase().replace(/^@+/, "");
  const domains = [info.domain, ...(info.hostedDomains ?? [])].filter(Boolean) as string[];
  for (const domain of domains) {
    if (value.endsWith(`.${domain}`)) {
      value = value.slice(0, value.length - domain.length - 1);
      break;
    }
  }
  return value;
}

/**
 * What a link into the claim page asks for: `?handle=alice` pre-fills the name
 * (a sign-in page that already checked it sends this), `from=signin` means a
 * sign-in is waiting in another tab. Only a plausible handle is taken — the
 * link comes from anywhere, so nothing else from it reaches the page.
 */
export function claimInvite(search: string): { handle: string; fromSignIn: boolean } {
  const params = new URLSearchParams(search);
  const handle = (params.get("handle") ?? "").trim().toLowerCase();
  return {
    handle: /^[a-z0-9][a-z0-9-]{0,62}$/.test(handle) ? handle : "",
    fromSignIn: params.get("from") === "signin",
  };
}

/** The relay a preset names; a typed URL gains https:// and loses trailing slashes. */
export function relayUrlForPreset(
  preset: string,
  typed: string | undefined,
  { production, local }: { production: string; local: string },
): string {
  if (preset === "local") return local;
  if (preset === "custom") {
    const raw = (typed ?? "").trim();
    if (!raw) return "";
    return (/^https?:\/\//i.test(raw) ? raw : `https://${raw}`).replace(/\/+$/, "");
  }
  return production;
}

/**
 * The name rule before the first keystroke (E15-T12). The policy is deployment
 * configuration, so the only honest source is a verdict the relay returned —
 * until one arrives, say nothing rather than guess.
 */
export function policyHint(info: Pick<ModeInfo, "reachable" | "resolved">, policy: NamePolicy | null): string {
  if (!info.reachable && info.resolved === false) return "";
  if (!policy) return "";
  const bits: string[] = [];
  if (policy.min_len && policy.max_len) bits.push(`${policy.min_len}–${policy.max_len} characters`);
  if (policy.charset) bits.push(policy.charset);
  return bits.join(", ");
}

/**
 * Whether this browser can hold a new identity's keys. Native keystore wins;
 * otherwise a passkey with PRF is required — a PIN is not a wrapping secret.
 * Unknown (still probing) is not blocked.
 */
export function webCustodyBlocked(custody: CustodyChoice | null, passkey: PasskeySupport | null): boolean {
  if (custody?.kind === "native") return false;
  if (!passkey) return false;
  return passkey.available === false || passkey.prf === false;
}

/**
 * The directory this app is served from: `/app/` on a relay, `/` in the
 * shell. Links to another host keep it, so a hand-off lands in the same app.
 */
export function appBasePath(pathname: string = globalThis.location?.pathname ?? "/"): string {
  const index = pathname.lastIndexOf("/");
  return index >= 0 ? pathname.slice(0, index + 1) : "/";
}

type LocationLike = Pick<Location, "protocol" | "port" | "pathname">;

/**
 * This app on an identity's own origin. The port travels: empty in production,
 * and the difference between a working hand-off and a dead host in dev and e2e.
 */
export function identityAppUrl(identity: string, location: LocationLike | undefined = globalThis.location): string {
  const port = location?.port ? `:${location.port}` : "";
  return `${location?.protocol ?? "https:"}//${identity}${port}${appBasePath(location?.pathname)}`;
}

/** This app on the launcher host, where a different name is chosen. */
export function launcherAppUrl(launcherHost: string, location: LocationLike | undefined = globalThis.location): string {
  return `${location?.protocol ?? "https:"}//${launcherHost}${appBasePath(location?.pathname)}`;
}

/**
 * The identity a joining device asks for. On an identity host the URL named
 * it; elsewhere a bare handle is completed with the host's domain so it
 * matches what the approving device holds.
 */
export function joinIdentityFor(raw: string, info: Pick<ModeInfo, "mode" | "subject" | "domain">, parentDomain = ""): string {
  if (info.mode === "identity" && info.subject) return info.subject;
  const value = String(raw ?? "").trim().toLowerCase();
  if (!value) return "";
  if (value.includes(".")) return value;
  const domain = (info.domain || parentDomain || "").replace(/^\./, "");
  return domain ? `${value}.${domain}` : value;
}
