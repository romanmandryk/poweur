import {
  checkRequestAgainstMetadata, checkResumeUri, decodeSignInRequest, describeSignInContext,
  fetchSignInContext, fetchSignInRequest, normalizeMatchCode, signInRequestUri,
  describeScopes, encodeSignInResponse, fetchRelyingPartyMetadata,
  normalizeSignInRequest, signInResponseCanonical, summarizeSignInRequest,
  validateSignInRequest,
} from "@poweur/client";

export const AUTH_LOG_PATH = "poweur-sys/private/logs/auth.log";
export const CONNECTED_APPS_PATH = "poweur-sys/relay/connected-apps.json";
const AUTH_LOG_MAX_BYTES = 256 * 1024;

export function decodeAuthInput(input) {
  const value = String(input ?? "").trim();
  if (!value) throw new Error("Paste a sign-in request or open its QR link.");
  try {
    const u = new URL(value);
    const encoded = u.searchParams.get("request") || u.searchParams.get("auth");
    if (encoded) return validateSignInRequest(decodeSignInRequest(encoded));
  } catch {
    // The portable base64url code and raw JSON are intentionally not URLs.
  }
  return validateSignInRequest(decodeSignInRequest(value));
}

/**
 * The request, however it came: carried inline (decodeAuthInput), or by
 * reference — the RP's short link from its QR, fetched here and required to
 * live at the request's own audience (E08-T6).
 */
export async function resolveAuthInput(input, options = {}) {
  const ref = signInRequestUri(String(input ?? ""));
  if (ref) {
    const { request } = await fetchSignInRequest(ref, options);
    return validateSignInRequest(request);
  }
  return decodeAuthInput(input);
}

export async function loadSignInConsent(input, options = {}) {
  const request = await resolveAuthInput(input, options);
  const metadata = await fetchRelyingPartyMetadata(request.audience, options);
  checkRequestAgainstMetadata(request, metadata);
  // Where the sign-in was started, for someone approving from another device.
  // Best effort: a missing or failing context never blocks an approval.
  let context = "";
  try {
    context = describeSignInContext(await fetchSignInContext(metadata, request.request_id, options));
  } catch { /* the RP may not publish one */ }
  return {
    request,
    metadata,
    context,
    headline: summarizeSignInRequest(request, metadata.name),
    scopes: describeScopes(request.scopes ?? [], metadata.name),
  };
}

export async function signBrowserApproval(request, identity, signer) {
  const n = normalizeSignInRequest(request);
  validateSignInRequest(n);
  const response = {
    poweur_auth: "1",
    request_id: n.request_id,
    identity: String(identity).trim().toLowerCase(),
    audience: n.audience,
    nonce: n.nonce,
    issued_at: n.issued_at,
    expires_at: n.expires_at,
    action: n.action,
    key_id: "identity",
    signature: "",
    ...(n.statement ? { statement: n.statement } : {}),
    ...(n.scopes?.length ? { scopes: n.scopes } : {}),
  };
  // Referencing the exported canonical helper here is deliberate: it keeps
  // WebCrypto custody while using exactly the SDK/Go byte sequence.
  response.signature = await signer.sign(signInResponseCanonical(response), "base64url");
  return { response, encoded: encodeSignInResponse(response) };
}

export async function appendBrowserConsent(dav, response, metadata) {
  const appId = metadata.app_id || new URL(response.audience).hostname.split(".").reverse().join(".");
  const record = {
    at: new Date().toISOString().replace(/\.\d{3}Z$/, "Z"),
    action: response.action,
    audience: response.audience,
    app_id: appId,
    app_name: metadata.name,
    request_id: response.request_id,
    verified: true,
    key_id: response.key_id,
    scopes: response.scopes ?? [],
    signer: "web",
    ...(response.statement ? { statement: response.statement } : {}),
  };
  const existing = await dav.readOptional(AUTH_LOG_PATH) ?? "";
  let lines = existing.trimEnd() ? existing.trimEnd().split("\n") : [];
  lines.push(JSON.stringify(record));
  while (new TextEncoder().encode(lines.join("\n") + "\n").length > AUTH_LOG_MAX_BYTES && lines.length > 1) {
    lines.shift();
  }
  try { await dav.mkdir("poweur-sys/private/logs"); } catch { /* already exists */ }
  await dav.write(AUTH_LOG_PATH, lines.join("\n") + "\n");
  return record;
}

/**
 * POST the approval to the request's callback and read the relying party's
 * receipt. Returns `{ delivered, resumeUri }`.
 *
 * `match` is the number the user copied from the screen that started the
 * sign-in; present only when approving from another device. Without it the
 * wire stays what it always was — the bare code as text/plain, a CORS-simple
 * request every existing RP accepts.
 *
 * The approval never goes into a URL. A same-device RP answers with a
 * `resume_uri` instead, which finishes only in the browser that started.
 */
export async function deliverBrowserApproval(request, encoded, fetchImpl = globalThis.fetch, match = "") {
  if (!request.response_uri) return { delivered: false, resumeUri: "" };
  const code = normalizeMatchCode(match);
  const response = await fetchImpl(request.response_uri, {
    method: "POST",
    // text/plain is a CORS-simple request; the RP accepts the portable code
    // directly, so QR/cross-device approval needs no preflight ceremony.
    headers: { "content-type": "text/plain;charset=UTF-8" },
    body: code ? JSON.stringify({ response: encoded, match: code }) : encoded,
    redirect: "error",
  });
  if (!response.ok) {
    let reason = "";
    try { reason = (await response.json())?.error ?? ""; } catch { /* not JSON */ }
    throw new Error(reason
      ? `The app refused the approval: ${reason}`
      : `The relying party refused the approval (HTTP ${response.status}).`);
  }
  let receipt = {};
  try { receipt = (await response.json()) ?? {}; } catch { /* an RP that predates receipts */ }
  const resumeUri = typeof receipt.resume_uri === "string" ? receipt.resume_uri.trim() : "";
  // Never follow a resume link off the origin the user just approved.
  checkResumeUri(request.audience, resumeUri);
  return { delivered: true, resumeUri };
}

export async function readConnectedApps(dav) {
  const raw = await dav.readOptional(CONNECTED_APPS_PATH);
  if (!raw) return { version: 1, apps: [] };
  const doc = JSON.parse(raw);
  if (doc.version !== 1 || !Array.isArray(doc.apps)) throw new Error("connected-apps.json has an unsupported shape");
  return doc;
}

export async function readConsentLog(dav) {
  const raw = await dav.readOptional(AUTH_LOG_PATH);
  if (!raw) return [];
  const records = [];
  for (const line of raw.split("\n")) {
    if (!line.trim()) continue;
    const record = JSON.parse(line);
    if (record && typeof record === "object") records.push(record);
  }
  return records;
}

export async function revokeConnectedApp(dav, appId, now = new Date()) {
  const doc = await readConnectedApps(dav);
  const app = doc.apps.find(entry => entry.app_id === appId);
  if (!app) return false;
  app.revoked_at = now.toISOString().replace(/\.\d{3}Z$/, "Z");
  await dav.writeJson(CONNECTED_APPS_PATH, doc);
  return true;
}
