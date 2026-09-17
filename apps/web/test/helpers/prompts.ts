import { encodeSignInRequest } from "@poweur/client";

/** A `sys.auth.request` inbox message as the web store holds it. */
export function promptMessage(id: string, expiresAt = "2026-09-17T12:03:00Z", extra: Record<string, unknown> = {}) {
  const request = encodeSignInRequest({
    poweur_auth: "1", request_id: `req_${id}`, audience: "https://oauth.poweur.org", nonce: "nonce-nonce-nonce",
    action: "signin", issued_at: "2026-09-17T12:00:00Z", expires_at: expiresAt,
    response_uri: "https://oauth.poweur.org/poweur/callback",
  } as never);
  return {
    id, sender: "bridge.poweur.org", type: "sys.auth.request", timestamp: "2026-09-17T12:00:00Z",
    plaintext: JSON.stringify({ version: 1, request, client: "Team dashboard", client_host: "grafana.example.org", expires_at: expiresAt }),
    ...extra,
  };
}
