/**
 * Sign-in prompts (EPIC-022 E22-T7): `sys.auth.request` messages from an OAuth
 * bridge the user trusts. They are notifications, not conversation: shown in
 * their own list, never archived, gone when they expire or are dismissed.
 */
import { MSG_TYPE_AUTH_REQUEST, normalizeMessageType, parseAuthRequestPayload } from "@poweur/client";

export interface AuthPrompt {
  id: string;
  from: string;
  client: string;
  clientHost: string;
  audience: string;
  request: string;
  expiresAt: string;
}

/** The prompts still worth showing, newest first. */
export function pendingAuthPrompts(messages: any[], dismissed: string[] = [], nowMs = Date.now()): AuthPrompt[] {
  const out: AuthPrompt[] = [];
  const skip = new Set(dismissed);
  for (const message of messages ?? []) {
    if (normalizeMessageType(message?.type) !== MSG_TYPE_AUTH_REQUEST) continue;
    if (!message.id || skip.has(message.id) || !message.sender || message.plaintext == null) continue;
    try {
      const { payload, request } = parseAuthRequestPayload(String(message.plaintext), nowMs);
      out.push({
        id: message.id,
        from: message.sender,
        client: payload.client ?? "",
        clientHost: payload.client_host ?? "",
        audience: request.audience,
        request: payload.request,
        expiresAt: payload.expires_at,
      });
    } catch {
      // Expired or malformed: nothing to act on.
    }
  }
  return out.sort((a, b) => Date.parse(b.expiresAt) - Date.parse(a.expiresAt));
}
