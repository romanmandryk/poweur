import { describe, expect, it } from "vitest";
import { encodeSignInRequest, signInDeepLink } from "@poweur/client";
import {
  appendBrowserConsent, decodeAuthInput, deliverBrowserApproval,
  readConnectedApps, readConsentLog, revokeConnectedApp, signBrowserApproval,
} from "../js/signin.js";

function request() {
  const now = Date.now();
  return {
    poweur_auth: "1", request_id: "req_web", domain: "tasks.example",
    audience: "https://tasks.example", nonce: "nonce_web",
    issued_at: new Date(now - 30_000).toISOString(),
    expires_at: new Date(now + 30_000).toISOString(), action: "signin",
    response_uri: "https://tasks.example/callback",
    scopes: ["dav:rw:apps/example.tasks"],
  };
}

class FakeDav {
  constructor(files = {}) { this.files = { ...files }; }
  async readOptional(path) { return this.files[path] ?? null; }
  async mkdir() {}
  async write(path, value) { this.files[path] = value; }
  async writeJson(path, value) { this.files[path] = JSON.stringify(value); }
}

describe("web Sign in with Poweur ID", () => {
  it("accepts portable codes, deep links and web handoff links", () => {
    const encoded = encodeSignInRequest(request());
    for (const input of [encoded, signInDeepLink(request()), `https://poweur.net/app/?auth=${encoded}`]) {
      expect(decodeAuthInput(input).request_id).toBe("req_web");
    }
  });

  it("signs the canonical response through the WebCrypto signer seam", async () => {
    let canonical = "";
    const signer = { sign: async value => { canonical = value; return "signature"; } };
    const out = await signBrowserApproval(request(), "Alice.Poweur.NET", signer);
    expect(out.response.identity).toBe("alice.poweur.net");
    expect(out.response.signature).toBe("signature");
    expect(canonical).toContain("poweur-signin\n1\nreq_web\nalice.poweur.net");
    expect(out.encoded).toBeTruthy();
  });

  it("appends a user-owned consent record", async () => {
    const dav = new FakeDav();
    const { response } = await signBrowserApproval(request(), "alice.poweur.net", { sign: async () => "sig" });
    await appendBrowserConsent(dav, response, { name: "Tasks", app_id: "example.tasks" });
    const line = JSON.parse(dav.files["poweur-sys/private/logs/auth.log"].trim());
    expect(line.app_id).toBe("example.tasks");
    expect(line.verified).toBe(true);
    expect(line.signer).toBe("web");
  });

  it("delivers to the request callback and reports copy-only flows", async () => {
    const calls = [];
    const fakeFetch = async (url, init) => { calls.push({ url, init }); return { ok: true }; };
    expect(await deliverBrowserApproval(request(), "encoded", fakeFetch)).toBe(true);
    expect(calls[0].url).toBe("https://tasks.example/callback");
    expect(calls[0].init.body).toBe("encoded");
    expect(await deliverBrowserApproval({ ...request(), response_uri: "" }, "encoded", fakeFetch)).toBe(false);
  });

  it("lists and revokes connected apps by editing the relay policy file", async () => {
    const dav = new FakeDav({
      "poweur-sys/relay/connected-apps.json": JSON.stringify({ version: 1, apps: [{ app_id: "example.tasks" }] }),
    });
    expect((await readConnectedApps(dav)).apps).toHaveLength(1);
    expect(await revokeConnectedApp(dav, "example.tasks", new Date("2026-09-10T12:00:00Z"))).toBe(true);
    const doc = JSON.parse(dav.files["poweur-sys/relay/connected-apps.json"]);
    expect(doc.apps[0].revoked_at).toBe("2026-09-10T12:00:00Z");
  });

  it("reads the JSON Lines consent audit trail", async () => {
    const dav = new FakeDav({
      "poweur-sys/private/logs/auth.log": '{"app_id":"example.tasks"}\n',
    });
    await expect(readConsentLog(dav)).resolves.toEqual([{ app_id: "example.tasks" }]);
  });
});
