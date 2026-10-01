import { characters, mountEditor } from "/assets/editor.js";

const $ = (id) => document.getElementById(id);
const getJSON = async (url, opts) =>
  (await fetch(url, { credentials: "same-origin", ...opts })).json();

let limits = { max: 1000, interval: 5 };
let nextCursor = "";
let listing = 0;
let editor = null;
let cooldownUntil = 0;
let cooldownTimer = null;

function formatTime(iso) {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return date.toLocaleString([], { dateStyle: "medium", timeStyle: "short" });
}

// A few warm gradients, picked by the ID, so one person is recognisable
// down the page without anyone uploading a picture.
const AVATARS = [
  ["#0f766e", "#65a30d"],
  ["#c2410c", "#d97706"],
  ["#be123c", "#c2410c"],
  ["#4d7c0f", "#a16207"],
  ["#9f1239", "#d97706"],
  ["#115e59", "#4d7c0f"],
];

function avatar(identity) {
  let h = 0;
  for (const ch of identity) h = (h * 31 + ch.codePointAt(0)) >>> 0;
  const [a, b] = AVATARS[h % AVATARS.length];
  const el = document.createElement("div");
  el.className = "avatar";
  el.setAttribute("aria-hidden", "true");
  el.style.background = `linear-gradient(135deg, ${a}, ${b})`;
  el.textContent = [...identity][0] || "?";
  return el;
}

function entryElement(entry) {
  const article = document.createElement("article");
  article.className = "entry";
  const main = document.createElement("div");
  const head = document.createElement("header");
  const who = document.createElement("strong");
  who.textContent = entry.identity;
  const when = document.createElement("time");
  when.dateTime = entry.at;
  when.textContent = formatTime(entry.at);
  head.append(who, when);
  const body = document.createElement("div");
  body.className = "body";
  // The server rendered this from the Markdown with raw HTML switched off,
  // and the page's CSP allows no script or remote content besides.
  body.innerHTML = entry.html;
  for (const a of body.querySelectorAll("a")) {
    a.rel = "nofollow ugc noopener noreferrer";
    a.target = "_blank";
  }
  main.append(head, body);
  article.append(avatar(entry.identity), main);
  return article;
}

function renderEntries(entries, { append = false } = {}) {
  const list = $("entries");
  if (!append) list.replaceChildren();
  if (entries.length === 0 && !append) {
    const empty = document.createElement("p");
    empty.className = "empty";
    empty.textContent = "No messages yet. Be the first to leave one.";
    list.append(empty);
    return;
  }
  list.append(...entries.map(entryElement));
}

function showMore() {
  $("more").hidden = !nextCursor;
}

// The newest messages. The page asks for them in pieces, so a book of a
// thousand messages is never in the browser at once.
async function loadEntries() {
  const generation = ++listing;
  const data = await getJSON("/api/entries");
  if (generation !== listing) return;
  limits = { max: data.max_message || 1000, interval: data.min_interval || 0 };
  nextCursor = data.next || "";
  renderEntries(data.entries || []);
  showMore();
  if (data.archive) {
    $("archive").href = data.archive;
    $("archive").hidden = false;
  }
  updateComposer();
}

async function loadMore() {
  if (!nextCursor) return;
  const generation = listing;
  const button = $("load-more");
  button.disabled = true;
  try {
    const data = await getJSON("/api/entries?before=" + encodeURIComponent(nextCursor));
    if (generation !== listing) return;
    if (data.error) throw new Error(data.error);
    nextCursor = data.next || "";
    renderEntries(data.entries || [], { append: true });
    showMore();
  } catch {
    button.textContent = "Could not load more. Try again";
    return;
  } finally {
    button.disabled = false;
  }
  button.textContent = "Show older messages";
}

async function refresh() {
  const me = await getJSON("/api/session");
  $("compose").hidden = !me.signed_in;
  $("auth").hidden = !!me.signed_in;
  $("who-chip").hidden = !me.signed_in;
  if (me.signed_in) $("who").textContent = me.identity;
  await loadEntries();
}

let current = "";

function updateComposer() {
  const count = characters(current);
  const counter = $("count");
  counter.textContent = `${count} / ${limits.max}`;
  counter.classList.toggle("over", count > limits.max);
  const waiting = Date.now() < cooldownUntil;
  $("send").disabled = waiting || count === 0 || count > limits.max;
}

function startCooldown(seconds) {
  cooldownUntil = Date.now() + seconds * 1000;
  clearInterval(cooldownTimer);
  const tick = () => {
    const left = Math.ceil((cooldownUntil - Date.now()) / 1000);
    if (left <= 0) {
      clearInterval(cooldownTimer);
      $("send").textContent = "Leave message";
    } else {
      $("send").textContent = `Wait ${left}s`;
    }
    updateComposer();
  };
  cooldownTimer = setInterval(tick, 250);
  tick();
}

function notice(text, isError) {
  const el = $("notice");
  el.textContent = text;
  el.classList.toggle("error", !!isError);
}

async function submit() {
  const message = editor.markdown();
  if (!message || characters(message) > limits.max) return;
  $("send").disabled = true;
  notice("");
  let res;
  try {
    res = await fetch("/api/entries", {
      method: "POST",
      credentials: "same-origin",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ message }),
    });
  } catch {
    notice("Could not reach the guestbook. Try again.", true);
    updateComposer();
    return;
  }
  const body = await res.json().catch(() => ({}));
  if (res.status === 201) {
    editor.clear();
    notice("Thank you, your message is in the book.");
    if (limits.interval > 0) startCooldown(limits.interval);
    await loadEntries();
    return;
  }
  if (res.status === 401) {
    notice("Your sign-in expired. Sign in again.", true);
    await refresh();
    return;
  }
  if (res.status === 429 && body.retry_after) startCooldown(body.retry_after);
  notice(body.error || "Something went wrong.", true);
  updateComposer();
}

function setStatus(text, state = "") {
  const el = $("status");
  el.textContent = text;
  el.className = "status" + (state ? " " + state : "");
}

function showMatch(code) {
  $("match").replaceChildren(
    ...[...code].map((digit) => {
      const el = document.createElement("span");
      el.className = "digit";
      el.textContent = digit;
      return el;
    }),
  );
}

async function signIn() {
  $("signin").disabled = true;
  let start;
  try {
    start = await getJSON("/auth/start", { method: "POST" });
  } catch {
    start = { error: "Could not reach the guestbook. Try again." };
  }
  $("signin").disabled = false;
  $("pending").hidden = false;
  if (start.error) {
    setStatus(start.error, "failed");
    return;
  }
  // A new tab, so this page, its code and its poll all stay alive while the
  // person approves. Following the link in this tab would throw them away.
  $("approve").href = start.request_link;
  $("approve-url").textContent = start.request_link;
  copyLink = start.request_link;
  showMatch(start.match_code);
  $("consent").replaceChildren();
  if (start.consent?.length) $("consent").textContent = start.consent.join(" ");
  setStatus("Waiting for your approval…");
  $("signin").hidden = true;
  // Same device: let the OS hand it to a registered signer. If nothing is
  // registered the deep link is a no-op and the button above still works.
  location.href = start.deep_link;
  const deadline = Date.parse(start.expires_at);
  while (Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, 1000));
    const poll = await getJSON(
      "/auth/poll?request_id=" + encodeURIComponent(start.request_id) +
        "&poll_secret=" + encodeURIComponent(start.poll_secret),
    );
    if (poll.status === "complete") {
      $("pending").hidden = true;
      $("signin").hidden = false;
      return refresh();
    }
    if (poll.status === "approved") {
      setStatus("Approved. Finishing in this browser…");
      continue;
    }
    if (poll.status === "failed") {
      setStatus("Rejected: " + poll.error, "failed");
      $("signin").hidden = false;
      return;
    }
    if (poll.status === "expired" || poll.status === "unknown") break;
  }
  setStatus("The request expired. Sign in again to get a new one.", "failed");
  $("signin").hidden = false;
}

let copyLink = "";

$("copy").addEventListener("click", async () => {
  try {
    await navigator.clipboard.writeText(copyLink);
    $("copy").textContent = "Copied";
  } catch {
    $("copy").textContent = "Select the link below";
  }
  setTimeout(() => ($("copy").textContent = "Copy the link"), 2000);
});

$("signin").addEventListener("click", signIn);
$("logout").addEventListener("click", async () => {
  await fetch("/auth/logout", { method: "POST", credentials: "same-origin" });
  refresh();
});
$("send").addEventListener("click", submit);
$("load-more").addEventListener("click", loadMore);

editor = mountEditor($("editor"), $("toolbar"), (markdown) => {
  current = markdown;
  if (markdown) notice("");
  updateComposer();
});
$("editor").addEventListener("keydown", (event) => {
  if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
    event.preventDefault();
    submit();
  }
});

refresh();
