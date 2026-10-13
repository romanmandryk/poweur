/** Approving a third-party sign-in request (EPIC-008), ported from app.js. */
import { signInRequestUri } from "@poweur/client";
import { signInCodeFromInput } from "../lib/app-link";
import { clientFor } from "../lib/client.js";
import {
  appendBrowserConsent,
  deliverBrowserApproval,
  loadSignInConsent,
  signBrowserApproval,
} from "../lib/signin.js";
import { useData } from "../state/data";
import { useRoute } from "../state/route";
import { useSession } from "../state/session";
import { toast } from "../state/ui";
import { trackAction } from "../lib/observability";
import { isShellRuntime } from "../lib/storage.js";

const patchAuth = (patch: Partial<ReturnType<typeof useData.getState>["auth"]>) =>
  useData.setState((state) => ({ auth: { ...state.auth, ...patch } }));

/** Navigation seam, replaced in tests. */
export let continueTo = (url: string) => {
  window.location.assign(url);
};
export function setContinueTo(fn: (url: string) => void) {
  continueTo = fn;
}

export function resetSignInRequest() {
  useData.setState({
    auth: { input: "", request: null, metadata: null, headline: "", context: "", crossDevice: false, choices: null, pushUri: "", noApp: false, scopes: [], loading: false, error: "", result: null },
  });
}

/** Verify a request, or a link to one (poweur://, an approve page, a short link). */
export async function beginSignInApproval(input: string, { crossDevice = false, noApp = false } = {}) {
  const value = signInCodeFromInput(input);
  // A request by reference is what a QR or a copied link carries: the screen
  // that started the sign-in is elsewhere (E08-T6), and finishes on its poll.
  crossDevice = crossDevice || isShellRuntime() || Boolean(signInRequestUri(value));
  patchAuth({ input: value, loading: true, error: "", result: null, crossDevice, choices: null, pushUri: "", noApp });
  useRoute.setState({ sub: "auth" });
  try {
    const consent: any = await loadSignInConsent(useData.getState().auth.input);
    patchAuth({ ...consent, loading: false, error: "" });
  } catch (error) {
    patchAuth({ request: null, metadata: null, loading: false, error: (error as Error).message });
  }
}

/**
 * Sign and deliver. `match` is the number picked from the choices a pushed
 * sign-in offers (EPIC-028); empty otherwise.
 */
export async function approveSignIn(match = "") {
  const identity = useSession.getState().identity;
  const client: any = identity ? clientFor(identity) : null;
  const { request, metadata, crossDevice } = useData.getState().auth;
  if (!client || !request || !metadata) {
    toast("Unlock your identity before approving", "warning");
    return;
  }
  patchAuth({ loading: true, error: "" });
  try {
    const signed: any = await signBrowserApproval(request, identity, client.signer);
    await appendBrowserConsent(await client.consentLog(), signed.response, metadata);
    const { delivered, resumeUri } = await deliverBrowserApproval(request, signed.encoded, undefined, { crossDevice, match });
    patchAuth({ loading: false, result: { ...signed, delivered, resumeUri, crossDevice } });
    // Same device: finish in this browser straight away. The resume link works
    // only here, because only this browser holds the RP's sign-in cookie.
    if (resumeUri && !crossDevice) continueTo(resumeUri);
    trackAction("approve-signin", { kind: match ? "pick" : crossDevice ? "cross-device" : "same-device" });
  } catch (error) {
    const status = (error as { status?: number }).status;
    patchAuth({
      loading: false,
      // A refused pick ends the sign-in at the RP, and pauses its pushes.
      error: match && status === 403
        ? "That was not the number on the screen where the sign-in started, so it was stopped. If you did not start it, someone may be trying to sign in as you."
        : (error as Error).message,
    });
  }
}

/** Ask the RP to push this request to the user's app (a browser without the keys). */
export async function sendSignInToApp(): Promise<string> {
  const { pushUri } = useData.getState().auth;
  if (!pushUri) return "failed";
  try {
    const res = await fetch(pushUri, { method: "POST", headers: { accept: "application/json" } });
    const out = (await res.json()) as { notice?: string };
    return out.notice ?? "failed";
  } catch {
    return "failed";
  }
}

/** Open a sign-in prompt for review: always a cross-device approval. */
export function openAuthPrompt(prompt: { request: string }) {
  void beginSignInApproval(prompt.request, { crossDevice: true });
}

/** Hide a prompt for the rest of this session. */
export function dismissAuthPrompt(id: string) {
  useData.setState((state) => ({ dismissedPrompts: [...state.dismissedPrompts, id] }));
}
