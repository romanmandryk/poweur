package guestbook

import (
	"html/template"
	"net/http"
)

// The whole front end. One file, no build step, no framework: a reference RP
// that needed a toolchain would be teaching the wrong lesson.
//
// Two transports are wired here, because they are the two a real site needs:
//
//   - same device — hand the request to the local signer via `poweur://auth`,
//     which comes back through GET /auth/callback?response=…
//   - other device — show the request as a link (and, in a real deployment, a
//     QR code of the same string), then poll /auth/poll until the phone has
//     approved.
var indexTemplate = template.Must(template.New("index").Parse(`<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Name}}</title>
<style>
  :root { color-scheme: light dark; }
  body { font: 16px/1.5 system-ui, sans-serif; max-width: 44rem; margin: 3rem auto; padding: 0 1rem; }
  header p { color: #666; }
  button { font: inherit; padding: .5rem 1rem; border-radius: .4rem; border: 1px solid #888; cursor: pointer; }
  #approve { word-break: break-all; font-size: .8rem; }
  .entry { border-top: 1px solid #ddd; padding: .75rem 0; }
  .who { font-weight: 600; }
  .at, .where { color: #777; font-size: .8rem; }
  .consent { background: #f6f6f6; border-radius: .4rem; padding: .75rem 1rem; }
  textarea { width: 100%; font: inherit; }
  [hidden] { display: none !important; }
</style>

<header>
  <h1>{{.Name}}</h1>
  <p>Sign in with a Poweur ID. This site holds no accounts, no passwords and no
     registration with anybody — it checks one signature against the name you
     published.</p>
</header>

<section id="auth">
  <button id="signin">Sign in with Poweur ID</button>
  <div id="pending" hidden>
    <p>Approve on your device, or open this on your phone:</p>
    <p id="approve"></p>
    <p id="status">Waiting…</p>
  </div>
</section>

<section id="me" hidden>
  <p>Signed in as <span class="who" id="who"></span>.
     <button id="logout">Sign out</button></p>
  <p class="where" id="storage"></p>
  <form id="post">
    <textarea id="message" rows="3" placeholder="Say something"></textarea>
    <button type="submit">Sign the guestbook</button>
  </form>
</section>

<section id="entries"></section>

<script type="module">
const $ = (id) => document.getElementById(id);
const j = async (url, opts) => (await fetch(url, { credentials: "same-origin", ...opts })).json();

async function refresh() {
  const me = await j("/api/session");
  $("me").hidden = !me.signed_in;
  $("auth").hidden = !!me.signed_in;
  if (me.signed_in) {
    $("who").textContent = me.identity;
    $("storage").textContent = me.home_storage
      ? "Entries are stored in your own home at " + me.home_storage
      : (me.home_storage_error ? "Storage not connected: " + me.home_storage_error : "");
  }
  const { entries } = await j("/api/entries");
  $("entries").innerHTML = "";
  for (const e of entries) {
    const el = document.createElement("div");
    el.className = "entry";
    el.innerHTML = '<div class="who"></div><div class="body"></div><div class="at"></div>';
    el.querySelector(".who").textContent = e.identity;
    el.querySelector(".body").textContent = e.message;
    el.querySelector(".at").textContent = e.at + (e.stored_at ? " · stored at " + e.stored_at : "");
    $("entries").append(el);
  }
}

$("signin").addEventListener("click", async () => {
  const start = await j("/auth/start", { method: "POST" });
  $("pending").hidden = false;
  const a = document.createElement("a");
  a.href = start.web_link;
  a.textContent = start.web_link;
  $("approve").replaceChildren(a);
  if (start.consent?.length) {
    const box = document.createElement("div");
    box.className = "consent";
    box.textContent = start.consent.join(" ");
    $("pending").prepend(box);
  }
  // Same device: let the OS hand it to a registered signer. If nothing is
  // registered the deep link is a no-op and the link above still works.
  location.href = start.deep_link;
  const deadline = Date.parse(start.expires_at);
  while (Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, 1000));
    const poll = await j("/auth/poll?request_id=" + encodeURIComponent(start.request_id));
    if (poll.status === "complete") { $("pending").hidden = true; return refresh(); }
    if (poll.status === "failed") { $("status").textContent = "Rejected: " + poll.error; return; }
    if (poll.status === "expired") { $("status").textContent = "Request expired."; return; }
  }
  $("status").textContent = "Request expired.";
});

$("logout").addEventListener("click", async () => {
  await fetch("/auth/logout", { method: "POST", credentials: "same-origin" });
  refresh();
});

$("post").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const message = $("message").value.trim();
  if (!message) return;
  const res = await fetch("/api/entries", {
    method: "POST", credentials: "same-origin",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ message }),
  });
  if (!res.ok) { alert((await res.json()).error); return; }
  $("message").value = "";
  refresh();
});

refresh();
</script>
`))

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = indexTemplate.Execute(w, map[string]any{"Name": s.cfg.Name})
}
