/**
 * @vitest-environment happy-dom
 *
 * Host → front door (EPIC-015 E15-T7).
 *
 * `classifyHost` is pure so the table below needs neither a page nor a relay.
 * Host parsing is exactly where this kind of thing goes wrong quietly: a
 * trailing root dot, a port, or a two-label subdomain that is not an identity.
 */
import { describe, it, expect, beforeEach } from "vitest";
import { classifyHost, currentHost, modeNow, resolveMode, resetMode, addIdOptions } from "../../src/lib/mode.js";

const ROOT = {
  service: "poweur-relay",
  launcher_host: "id.poweur.net",
  launcher_hosts: ["id.poweur.net", "poweur.net"],
  hosted_domains: ["poweur.net"],
};

describe("classifyHost", () => {
  const cases = [
    ["id.poweur.net", "launcher", ""],
    ["poweur.net", "launcher", ""],
    ["bob.poweur.net", "identity", "bob.poweur.net"],
    ["seedcheck.poweur.net", "identity", "seedcheck.poweur.net"],
    // Two labels under the parent is not a hosted identity — registration
    // would refuse it, so the door must not offer it.
    ["a.b.poweur.net", "unknown", ""],
    ["relay.example.org", "unknown", ""],
    ["localhost", "unknown", ""],
    ["", "unknown", ""],
  ];

  for (const [host, mode, subject] of cases) {
    it(`${host || "(empty)"} → ${mode}`, () => {
      const info = classifyHost(host, ROOT);
      expect(info.mode).toBe(mode);
      expect(info.subject).toBe(subject);
    });
  }

  it("splits an identity host into handle and domain", () => {
    const info = classifyHost("bob.poweur.net", ROOT);
    expect(info.handle).toBe("bob");
    expect(info.domain).toBe("poweur.net");
  });

  it("gives the launcher the domain it claims under", () => {
    const multi = {
      ...ROOT,
      hosted_domains: ["poweur.net", "example.org"],
      launcher_hosts: ["id.poweur.net", "poweur.net", "id.example.org", "example.org"],
    };
    expect(classifyHost("id.example.org", multi).domain).toBe("example.org");
    expect(classifyHost("example.org", multi).domain).toBe("example.org");
    expect(classifyHost("id.poweur.net", multi).domain).toBe("poweur.net");
  });

  it("treats a shell as a shell whatever its hostname says", () => {
    // capacitor://localhost — an ordinary hostname on a protocol that is not
    // the web, so the protocol is what decides.
    const info = classifyHost("localhost", ROOT, { shell: true });
    expect(info.mode).toBe("shell");
    expect(info.domain).toBe("poweur.net");
  });

  it("falls back to launcher_host on a relay older than E15-T7", () => {
    const legacy = { launcher_host: "id.poweur.net", hosted_domains: ["poweur.net"] };
    expect(classifyHost("id.poweur.net", legacy).mode).toBe("launcher");
    // The apex is not implied there — that relay does not redirect it either.
    expect(classifyHost("poweur.net", legacy).mode).toBe("unknown");
  });

  it("is unknown, not a guess, when the relay could not be asked", () => {
    const info = classifyHost("bob.poweur.net", null);
    expect(info.mode).toBe("unknown");
    expect(info.resolved).toBe(false);
    expect(info.reachable).toBe(false);
  });

  it("normalizes case, the root dot and duplicate advertised hosts", () => {
    const messy = {
      launcher_host: "ID.Poweur.NET.",
      launcher_hosts: ["ID.POWEUR.NET", "id.poweur.net.", " poweur.net "],
      hosted_domains: ["Poweur.NET."],
    };
    const info = classifyHost("id.poweur.net", messy);
    expect(info.mode).toBe("launcher");
    expect(info.launcherHosts).toEqual(["id.poweur.net", "poweur.net"]);
    expect(info.hostedDomains).toEqual(["poweur.net"]);
    expect(info.launcherHost).toBe("id.poweur.net");
  });
});

describe("currentHost", () => {
  it("lowercases and drops the DNS root dot", () => {
    expect(currentHost({ hostname: "Bob.Poweur.NET." })).toBe("bob.poweur.net");
  });

  it("is empty when there is no location at all", () => {
    // A shell before the webview has a document; `null` rather than
    // `undefined` because undefined would take the default parameter.
    expect(currentHost(null)).toBe("");
  });
});

describe("modeNow / resolveMode", () => {
  beforeEach(() => {
    resetMode();
    sessionStorage.clear();
    localStorage.clear();
  });

  it("paints from the cached root document rather than flashing a guess", () => {
    sessionStorage.setItem("poweur:root", JSON.stringify(ROOT));
    // happy-dom serves localhost, so with no cache this would be `unknown`;
    // the cache is what makes a reload paint the right door immediately.
    const info = classifyHost("bob.poweur.net", JSON.parse(sessionStorage.getItem("poweur:root")));
    expect(info.mode).toBe("identity");
    expect(modeNow().resolved).toBe(true);
  });

  it("memoizes, so every render path does not re-classify", () => {
    expect(modeNow()).toBe(modeNow());
  });

  it("resolves to unknown-but-unreachable when there is no relay to ask", async () => {
    const info = await resolveMode();
    expect(info.mode).toBe("unknown");
    expect(info.reachable).toBe(false);
  });

  it("a cached root does not make a probe look finished", async () => {
    // The shape that starved the page: `modeNow()` is `resolved` the moment a
    // cached document exists, so a `resolveMode()` that short-circuited on
    // `resolved` handed back a promise whose `probed` was still false. Callers
    // written as `if (!probed) resolveMode().then(render)` then re-armed
    // themselves in a microtask on every render — forever, before
    // DOMContentLoaded, with no network request to show for it.
    sessionStorage.setItem("poweur:root", JSON.stringify(ROOT));
    expect(modeNow().resolved).toBe(true);
    expect(modeNow().probed).toBe(false);

    const info = await resolveMode();
    expect(info.probed).toBe(true);
    // And a second call is now genuinely a no-op rather than a fresh promise
    // that leaves the caller's condition true.
    expect(await resolveMode()).toBe(info);
  });
});

describe("addIdOptions", () => {
  const cases = [
    ["shell",    { passkey: false, join: true,  create: true,  joinSubject: "" }],
    ["unknown",  { passkey: true,  join: true,  create: true,  joinSubject: "" }],
    ["launcher", { passkey: true,  join: false, create: false, joinSubject: "" }],
    ["identity", { passkey: true,  join: true,  create: false, joinSubject: "bob.poweur.net" }],
  ];

  for (const [mode, expected] of cases) {
    it(`${mode} offers ${Object.entries(expected).filter(([, v]) => v === true).map(([k]) => k).join(" + ") || "nothing"}`, () => {
      const info = mode === "identity"
        ? classifyHost("bob.poweur.net", ROOT)
        : mode === "launcher"
          ? classifyHost("id.poweur.net", ROOT)
          : mode === "shell"
            ? classifyHost("localhost", ROOT, { shell: true })
            : classifyHost("localhost", ROOT);
      expect(info.mode).toBe(mode);
      expect(addIdOptions(info)).toEqual(expected);
    });
  }
});
