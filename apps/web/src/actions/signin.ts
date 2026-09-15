/** Approving a third-party sign-in request (EPIC-008), ported from app.js. */
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

const patchAuth = (patch: Partial<ReturnType<typeof useData.getState>["auth"]>) =>
  useData.setState((state) => ({ auth: { ...state.auth, ...patch } }));

export function resetSignInRequest() {
  useData.setState({
    auth: { input: "", request: null, metadata: null, headline: "", scopes: [], loading: false, error: "", result: null },
  });
}

/** Verify a pasted request or `?auth=` link against the app's origin. */
export async function beginSignInApproval(input: string) {
  patchAuth({ input: String(input ?? "").trim(), loading: true, error: "", result: null });
  useRoute.setState({ sub: "auth" });
  try {
    const consent: any = await loadSignInConsent(useData.getState().auth.input);
    patchAuth({ ...consent, loading: false, error: "" });
  } catch (error) {
    patchAuth({ request: null, metadata: null, loading: false, error: (error as Error).message });
  }
}

export async function approveSignIn() {
  const identity = useSession.getState().identity;
  const client: any = identity ? clientFor(identity) : null;
  const { request, metadata } = useData.getState().auth;
  if (!client || !request || !metadata) {
    toast("Unlock your identity before approving", "warning");
    return;
  }
  patchAuth({ loading: true, error: "" });
  try {
    const signed: any = await signBrowserApproval(request, identity, client.signer);
    const dav = await client.dav();
    await appendBrowserConsent(dav, signed.response, metadata);
    const delivered = await deliverBrowserApproval(request, signed.encoded);
    patchAuth({ loading: false, result: { ...signed, delivered } });
  } catch (error) {
    patchAuth({ loading: false, error: (error as Error).message });
  }
}
