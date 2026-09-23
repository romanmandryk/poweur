import { describe, expect, it } from "vitest";
import { encodeSignInRequest, signInDeepLink } from "@poweur/client";
import {
  appendBrowserConsent, decodeAuthInput, deliverBrowserApproval, loadSignInConsent, resolveAuthInput,
  readConnectedApps, readConsentLog, revokeConnectedApp, signBrowserApproval,
} from "../../src/lib/signin.js";

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

  it("fetches a request passed by reference, only from its own audience", async () => {
    const serve = (audience) => async () => new Response(JSON.stringify({ request: encodeSignInRequest({ ...request(), audience }) }));
    const link = "https://tasks.example/r/K7QM4XP2";
    for (const input of [link, `poweur://auth?request_uri=${encodeURIComponent(link)}`, `https://poweur.net/app/?auth=${encodeURIComponent(link)}`]) {
      expect((await resolveAuthInput(input, { fetch: serve("https://tasks.example") })).request_id).toBe("req_web");
    }
    await expect(resolveAuthInput(link, { fetch: serve("https://bank.example") })).rejects.toThrow(/outside its audience/);
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
    const fakeFetch = async (url, init) => { calls.push({ url, init }); return { ok: true, json: async () => ({ status: "ok" }) }; };
    expect(await deliverBrowserApproval(request(), "encoded", fakeFetch)).toEqual({ delivered: true, resumeUri: "" });
    expect(calls[0].url).toBe("https://tasks.example/callback");
    // Without a code the wire is unchanged: the bare code, CORS-simple.
    expect(calls[0].init.body).toBe("encoded");
    expect(calls[0].init.headers["content-type"]).toMatch(/^text\/plain/);
    expect(await deliverBrowserApproval({ ...request(), response_uri: "" }, "encoded", fakeFetch))
      .toEqual({ delivered: false, resumeUri: "" });
  });

  it("sends a cross-device code in an envelope", async () => {
    let body = "";
    const fakeFetch = async (_url, init) => { body = init.body; return { ok: true, json: async () => ({}) }; };
    await deliverBrowserApproval(request(), "encoded", fakeFetch, " 4-2 ");
    expect(JSON.parse(body)).toEqual({ response: "encoded", match: "42" });
  });

  it("returns a same-origin resume link and refuses any other", async () => {
    const answer = (resume_uri) => async () => ({ ok: true, json: async () => ({ status: "ok", resume_uri }) });
    const ok = await deliverBrowserApproval(request(), "encoded", answer("https://tasks.example/auth/resume?code=abc"));
    expect(ok).toEqual({ delivered: true, resumeUri: "https://tasks.example/auth/resume?code=abc" });
    for (const bad of ["https://evil.example/r", "http://tasks.example/r", "javascript:alert(1)"]) {
      await expect(deliverBrowserApproval(request(), "encoded", answer(bad))).rejects.toThrow(/resume_uri/);
    }
  });

  it("never puts the approval in a URL", async () => {
    const urls = [];
    const fakeFetch = async (url) => { urls.push(url); return { ok: true, json: async () => ({ resume_uri: "https://tasks.example/r?code=c" }) }; };
    const out = await deliverBrowserApproval(request(), "SECRET-APPROVAL", fakeFetch);
    expect(urls.join(" ")).not.toContain("SECRET-APPROVAL");
    expect(out.resumeUri).not.toContain("SECRET-APPROVAL");
  });

  it("tolerates an RP that answers without JSON, and surfaces refusals", async () => {
    const legacy = async () => ({ ok: true, json: async () => { throw new Error("not json"); } });
    expect(await deliverBrowserApproval(request(), "encoded", legacy)).toEqual({ delivered: true, resumeUri: "" });
    const refused = async () => ({ ok: false, status: 403, json: async () => ({ error: "the code does not match" }) });
    await expect(deliverBrowserApproval(request(), "encoded", refused, "11")).rejects.toThrow("the code does not match");
  });

  it("adds where the sign-in started when the app publishes it, and never fails on it", async () => {
    const { encodeSignInRequest: enc } = await import("@poweur/client");
    const req = request();
    const meta = { poweur_auth: "1", origin: "https://tasks.example", name: "Tasks", context_uri: "https://tasks.example/ctx",
      response_uris: ["https://tasks.example/callback"] };
    const fetchWith = (ctx) => async (url) => {
      if (url.endsWith("/.well-known/poweur.json")) return new Response(JSON.stringify(meta));
      if (ctx === "boom") throw new Error("offline");
      return new Response(JSON.stringify(ctx));
    };
    const withCtx = await loadSignInConsent(enc(req), {
      fetch: fetchWith({ request_id: "req_web", started_at: new Date().toISOString(), browser: "Firefox on Linux" }),
    });
    expect(withCtx.context).toMatch(/^Started just now in Firefox on Linux\.$/);
    const without = await loadSignInConsent(enc(req), { fetch: fetchWith("boom") });
    expect(without.context).toBe("");
    expect(without.metadata.name).toBe("Tasks");
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
