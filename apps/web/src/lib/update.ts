/**
 * Notice a newer deployed build while this page keeps running. An installed
 * app (and the page a notification focuses) can stay alive for hours, so a
 * deploy is otherwise invisible until the phone kills it: the user sees an
 * old UI next to a new one. The built index names its entry bundle by hash
 * (`assets/main-<hash>.js`); a different one there means a new version.
 */

const ENTRY = /assets\/main-[A-Za-z0-9_-]+\.js/;

/** The entry bundle this page is running, or "" when it cannot tell (dev server). */
export function runningBundle(doc: Document = document): string {
  for (const script of Array.from(doc.querySelectorAll<HTMLScriptElement>("script[src]"))) {
    const match = script.getAttribute("src")?.match(ENTRY);
    if (match) return match[0];
  }
  return "";
}

/** The entry bundle a fetched index.html names, or "". */
export function deployedBundle(html: string): string {
  return html.match(ENTRY)?.[0] ?? "";
}

/** Whether the deployed index names another entry bundle than the running one. */
export async function updateAvailable(fetchImpl: typeof fetch = globalThis.fetch.bind(globalThis), doc: Document = document): Promise<boolean> {
  const running = runningBundle(doc);
  if (!running) return false;
  try {
    const response = await fetchImpl(new URL("./", doc.baseURI).href, { cache: "no-store", credentials: "same-origin" });
    if (!response.ok) return false;
    const deployed = deployedBundle(await response.text());
    return Boolean(deployed) && deployed !== running;
  } catch {
    return false;
  }
}
