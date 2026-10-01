import { characters, mountEditor } from "/assets/editor.js";

const $ = (id) => document.getElementById(id);
const getJSON = async (url, opts) =>
  (await fetch(url, { credentials: "same-origin", ...opts })).json();

let limits = { max: 1000, interval: 5 };
let editor = null;
let cooldownUntil = 0;
let cooldownTimer = null;

function formatTime(iso) {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return date.toLocaleString([], { dateStyle: "medium", timeStyle: "short" });
}

function renderEntries(entries) {
  const list = $("entries");
  list.replaceChildren();
  if (entries.length === 0) {
    const empty = document.createElement("p");
    empty.className = "empty";
    empty.textContent = "No entries yet. Be the first to sign.";
    list.append(empty);
    return;
  }
  for (const entry of entries) {
    const article = document.createElement("article");
    article.className = "entry";
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
    article.append(head, body);
    list.append(article);
  }
}

async function loadEntries() {
  const data = await getJSON("/api/entries");
  limits = { max: data.max_message || 1000, interval: data.min_interval || 0 };
  renderEntries(data.entries || []);
  if (data.archive) {
    $("archive").href = data.archive;
    $("archive").hidden = false;
  }
  updateComposer();
}

async function refresh() {
  const me = await getJSON("/api/session");
  $("compose").hidden = !me.signed_in;
  $("auth").hidden = !!me.signed_in;
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
      $("send").textContent = "Sign the guestbook";
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
    notice("Thank you, you are in the book.");
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

async function signIn() {
  const start = await getJSON("/auth/start", { method: "POST" });
  if (start.error) {
    $("status").textContent = start.error;
    $("pending").hidden = false;
    return;
  }
  $("pending").hidden = false;
  const link = document.createElement("a");
  // The short link, not the whole request: something a person can read out.
  link.href = start.request_link;
  link.textContent = start.request_link;
  $("approve").replaceChildren(link);
  $("match").textContent = start.match_code;
  $("consent").replaceChildren();
  if (start.consent?.length) $("consent").textContent = start.consent.join(" ");
  $("status").textContent = "Waiting for your approval…";
  // Same device: let the OS hand it to a registered signer. If nothing is
  // registered the deep link is a no-op and the link above still works.
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
      return refresh();
    }
    if (poll.status === "approved") {
      $("status").textContent = "Approved. Finishing in this browser…";
      continue;
    }
    if (poll.status === "failed") {
      $("status").textContent = "Rejected: " + poll.error;
      return;
    }
    if (poll.status === "expired") break;
  }
  $("status").textContent = "The request expired. Press the button to try again.";
}

$("signin").addEventListener("click", signIn);
$("logout").addEventListener("click", async () => {
  await fetch("/auth/logout", { method: "POST", credentials: "same-origin" });
  refresh();
});
$("send").addEventListener("click", submit);

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
