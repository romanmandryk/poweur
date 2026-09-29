/**
 * The drive link viewer (E20-T7): `https://<owner>/s/<link>#<secret>`.
 * It runs with no app state and no identity under a strict CSP. It reads the
 * secret from the fragment, removes it from the address bar and history, and
 * decrypts everything here; the relay only ever sees the link ID (and, for a
 * password, the derived verifier).
 */
import { LinkPasswordRequired, openLink, parseLinkUrl, type OpenedLink, type OpenFile } from "@poweur/client/drive";

const status = document.getElementById("status")!;
const items = document.getElementById("items")!;
const form = document.getElementById("password") as HTMLFormElement;
const input = document.getElementById("password-input") as HTMLInputElement;

function say(text: string, error = false) {
  status.textContent = text;
  status.classList.toggle("text-danger", error);
  status.classList.toggle("text-muted", !error);
}

let parsed: ReturnType<typeof parseLinkUrl>;
try {
  parsed = parseLinkUrl(location.href);
} catch {
  say("This link is incomplete. Copy the whole link, including everything after #.", true);
  throw new Error("invalid link");
}
// The key must not linger in the address bar, history or a bookmark.
history.replaceState(null, "", location.pathname);

async function download(opened: OpenedLink, file: OpenFile) {
  const parts: BlobPart[] = [];
  for await (const chunk of opened.files.read(file)) parts.push(new Uint8Array(chunk));
  const url = URL.createObjectURL(new Blob(parts));
  const a = document.createElement("a");
  a.href = url;
  a.download = file.name || "download";
  a.rel = "noopener";
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 30_000);
}

function row(opened: OpenedLink, file: OpenFile) {
  const li = document.createElement("li");
  li.className = "flex items-center justify-between gap-4 px-4 py-3";
  const name = document.createElement("span");
  name.className = "break-all";
  name.textContent = (file.name || "Shared item") + (file.manifest.kind === "folder" ? "/" : "");
  li.append(name);
  if (file.manifest.kind === "file" && file.manifest.mode === "replace") {
    const button = document.createElement("button");
    button.type = "button";
    button.textContent = "Download";
    button.className = "rounded-lg bg-accent px-3 py-1.5 text-sm font-medium text-white disabled:opacity-50";
    button.addEventListener("click", () => {
      button.disabled = true;
      download(opened, file).catch(() => say("Could not decrypt this file.", true)).finally(() => { button.disabled = false; });
    });
    li.append(button);
  }
  items.append(li);
}

async function open(password?: string) {
  say("Opening…");
  try {
    // Authors are verified from public identity documents, fetched through
    // this relay (which serves hosted identities and resolves the rest).
    const local = location.hostname === "localhost" || location.hostname.endsWith(".localhost");
    const resolve = { scheme: location.protocol === "http:" ? "http" as const : "https" as const, relayUrl: location.origin, skipDns: true, allowPrivate: local };
    const opened = await openLink({ origin: location.origin, drive: parsed.drive, link: parsed.link, fragment: parsed.fragment, resolve, ...(password ? { password } : {}) });
    form.hidden = true;
    items.replaceChildren();
    if (opened.root.manifest.kind === "folder") {
      const children = await opened.files.list(opened.root);
      say(children.length ? `${children.length} item${children.length === 1 ? "" : "s"}` : "This folder is empty.");
      for (const child of children) row(opened, child);
    } else {
      say("One file");
      row(opened, opened.root);
    }
  } catch (error) {
    if (error instanceof LinkPasswordRequired) {
      form.hidden = false;
      say("");
      input.focus();
      return;
    }
    const code = (error as { status?: number }).status;
    say(code === 401 ? "Wrong password." : code === 404 ? "This link does not exist, has expired or has been used up." :
      code === 429 ? "Too many attempts. Try again later." : "This link could not be opened.", true);
    if (code === 401) form.hidden = false;
  }
}

form.addEventListener("submit", event => {
  event.preventDefault();
  void open(input.value);
});
void open();
