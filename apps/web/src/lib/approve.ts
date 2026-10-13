/**
 * The approve page, `/id/approve` (EPIC-028): a QR on another screen opens it
 * with the request link in the fragment (`#auth=`) or a pairing (`#pair=`).
 * When this browser holds no keys it decides where approval can happen
 * instead, by device rather than by asking.
 */
import { signInReferenceDeepLink, signInRequestUri } from "@poweur/client";
import { isIOS } from "./push";

/** The store app's Android package: an intent names it so Chrome can fall back. */
export const ANDROID_PACKAGE = "net.poweur.app";

/** `#noapp` on the fallback URL: the app is not installed, do not try again. */
const NO_APP = "noapp";

export type ApproveDevice = "ios" | "android" | "desktop";

export function approveDevice(userAgent = globalThis.navigator?.userAgent ?? "", touchPoints = globalThis.navigator?.maxTouchPoints ?? 0): ApproveDevice {
  if (isIOS(userAgent, touchPoints)) return "ios";
  if (/Android/i.test(userAgent)) return "android";
  return "desktop";
}

/**
 * The sign-in request link carried by an approve page's fragment, or "".
 * Strips the fragment from the address bar: a reload must not approve twice.
 */
export function takeApproveLink(): { link: string; noApp: boolean } {
  const hash = globalThis.location?.hash ?? "";
  const params = new URLSearchParams(hash.replace(/^#/, ""));
  const link = params.get("auth") ? signInRequestUri(globalThis.location.href) ?? "" : "";
  if (!link) return { link: "", noApp: false };
  try {
    history.replaceState(null, "", globalThis.location.pathname + globalThis.location.search);
  } catch {
    /* keep going: the request is in hand */
  }
  return { link, noApp: params.has(NO_APP) };
}

/** This page's approve link for a request link (what a phone should scan). */
export function approvePageLink(requestLink: string, location: Pick<Location, "origin" | "pathname"> = globalThis.location): string {
  const path = location.pathname.endsWith("/approve") ? location.pathname : location.pathname.replace(/\/?$/, "/approve");
  return `${location.origin}${path}#auth=${encodeURIComponent(requestLink)}`;
}

/** `poweur://auth?request_uri=…` for a request link, or "" for anything else. */
export function appLinkFor(input: string): string {
  const ref = signInRequestUri(input);
  return ref ? signInReferenceDeepLink(ref) : "";
}

/**
 * Chrome on Android opens an intent in the app when it is installed, and
 * otherwise loads `fallback` — here the same approve page marked `noapp`, so
 * it does not try again.
 */
export function androidIntentFor(requestLink: string, fallback: string): string {
  const back = fallback.includes("#") ? `${fallback}&${NO_APP}` : `${fallback}#${NO_APP}`;
  return (
    `intent://auth?request_uri=${encodeURIComponent(requestLink)}` +
    `#Intent;scheme=poweur;package=${ANDROID_PACKAGE};S.browser_fallback_url=${encodeURIComponent(back)};end`
  );
}
