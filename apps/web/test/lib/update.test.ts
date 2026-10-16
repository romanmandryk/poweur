import { describe, expect, it } from "vitest";
import { deployedBundle, runningBundle, updateAvailable } from "../../src/lib/update";

function page(src?: string): Document {
  const doc = document.implementation.createHTMLDocument("x");
  if (src) {
    const script = doc.createElement("script");
    script.type = "text/x-inert"; // found by src, never loaded by the test DOM
    script.setAttribute("src", src);
    doc.head.append(script);
  }
  return doc;
}

const index = (bundle: string) => `<!doctype html><script type="module" crossorigin src="./assets/${bundle}"></script>`;
const fetchIndex = (html: string, ok = true) => (async () => new Response(html, { status: ok ? 200 : 503 })) as unknown as typeof fetch;

describe("update check", () => {
  it("reads the entry bundle from the page and from a fetched index", () => {
    expect(runningBundle(page("./assets/main-CLRrel2s.js"))).toBe("assets/main-CLRrel2s.js");
    expect(runningBundle(page("/src/main.tsx"))).toBe("");
    expect(deployedBundle(index("main-Ab_9-x.js"))).toBe("assets/main-Ab_9-x.js");
  });

  it("reports a different deployed bundle, and nothing otherwise", async () => {
    const doc = page("./assets/main-OLD.js");
    expect(await updateAvailable(fetchIndex(index("main-NEW.js")), doc)).toBe(true);
    expect(await updateAvailable(fetchIndex(index("main-OLD.js")), doc)).toBe(false);
    expect(await updateAvailable(fetchIndex("no bundle here"), doc)).toBe(false);
    expect(await updateAvailable(fetchIndex(index("main-NEW.js"), false), doc)).toBe(false);
    expect(await updateAvailable((async () => { throw new Error("offline"); }) as unknown as typeof fetch, doc)).toBe(false);
    // A dev server (no hashed bundle) never asks.
    expect(await updateAvailable(fetchIndex(index("main-NEW.js")), page("/src/main.tsx"))).toBe(false);
  });
});
