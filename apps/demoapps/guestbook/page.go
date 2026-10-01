package guestbook

import (
	"embed"
	"html/template"
	"net/http"
	"path"
	"strings"
)

// The whole front end: one page and a few files in assets/, no build step and
// no framework. A reference RP that needed a toolchain would be teaching the
// wrong lesson.
//
// Two ways a sign-in finishes, and the approval — not this page — picks one:
//
//   - same device — the signer (the `poweur://auth` handler, or the web signer
//     the link opens) POSTs the approval and sends this browser to
//     /auth/resume, which works only here because only this browser holds the
//     binding cookie /auth/start set.
//   - other device — the phone's user types the two-digit code shown on this
//     page into their signer; this page polls /auth/poll with its own poll
//     secret and is handed the session.
//
//go:embed assets
var assets embed.FS

var assetTypes = map[string]string{
	".js":  "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8",
	".svg": "image/svg+xml",
}

func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	contentType, ok := assetTypes[path.Ext(name)]
	if !ok || strings.ContainsAny(name, "/\\") {
		http.NotFound(w, r)
		return
	}
	data, err := assets.ReadFile("assets/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(data)
}

var indexTemplate = template.Must(template.New("index").Parse(`<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Name}}</title>
<meta name="description" content="A guestbook you sign with a Poweur ID. The messages are a public Markdown file.">
<link rel="icon" href="/assets/favicon.svg" type="image/svg+xml">
<link rel="stylesheet" href="/assets/guestbook.css">
<main>
  <nav class="topbar">
    <a class="brand" href="/"><span class="mark"><img src="/assets/poweur-mark.svg" alt="" width="19" height="24"></span>
      <span>{{.Name}}</span></a>
    <span id="who-chip" class="who-chip" hidden>Signed in as <strong id="who"></strong>
      <button id="logout" class="link" type="button">Sign out</button></span>
  </nav>

  <header class="hero">
    <p class="eyebrow">An open book on a Poweur drive</p>
    <h1>Sign in with a Poweur ID</h1>
    <p class="lede">Leave a message for others.</p>
  </header>

  <section id="auth" class="card">
    <h2>Write a message</h2>
    <p class="muted">Sign in first, so every message comes from a real ID.</p>
    <button id="signin" class="primary big" type="button"><img src="/assets/poweur-mark.svg" alt="" width="19" height="24">Sign in with Poweur ID</button>
    <ol id="pending" class="steps" hidden>
      <li class="step"><span class="n">1</span>
        <div>
          <strong>Approve it with your ID</strong>
          <p id="consent"></p>
          <a id="approve" class="cta big" href="#" target="_blank" rel="noopener">Open the approval page in a new tab</a>
          <p class="hint">It opens in a new tab so this page, and the code below, stay here. On your phone?
            <button id="copy" class="link" type="button">Copy the link</button> and open it there.</p>
          <span id="approve-url" class="url"></span>
        </div>
      </li>
      <li class="step"><span class="n">2</span>
        <div>
          <strong>If it asks for a code, type this one</strong>
          <div id="match" class="match" aria-label="Your code"></div>
          <p class="hint">Only enter it on a screen you opened yourself.</p>
        </div>
      </li>
      <li class="step"><span class="n">3</span>
        <div><p id="status" class="status" role="status">Waiting…</p>
          <p class="hint">Keep this page open. It finishes by itself once you approve.</p></div>
      </li>
    </ol>
  </section>

  <section id="compose" class="card" hidden>
    <h2>Write a message</h2>
    <p class="muted">Short and kind is best. It is added to the book for everyone to read.</p>
    <div id="toolbar" class="toolbar" role="toolbar" aria-label="Formatting">
      <button type="button" data-cmd="bold" title="Bold (Ctrl/Cmd+B)" aria-label="Bold" aria-pressed="false"><b>B</b></button>
      <button type="button" data-cmd="italic" title="Italic (Ctrl/Cmd+I)" aria-label="Italic" aria-pressed="false"><i>I</i></button>
      <button type="button" data-cmd="code" title="Code" aria-label="Code">&lt;/&gt;</button>
      <button type="button" data-cmd="link" title="Link (select text first)" aria-label="Link">Link</button>
      <button type="button" data-cmd="ul" title="Bulleted list" aria-label="Bulleted list" aria-pressed="false">&bull; List</button>
      <button type="button" data-cmd="ol" title="Numbered list" aria-label="Numbered list" aria-pressed="false">1. List</button>
      <button type="button" data-cmd="quote" title="Quote" aria-label="Quote">&ldquo; Quote</button>
    </div>
    <div id="editor" class="editor" contenteditable="true" role="textbox" aria-multiline="true"
         aria-label="Your message" data-placeholder="Say something kind"></div>
    <div class="composer-foot">
      <span id="count">0 / 1000</span>
      <span id="notice" role="status"></span>
      <button id="send" class="primary" type="button" disabled>Leave message</button>
    </div>
  </section>

  <section aria-label="Messages">
    <div class="entries-head"><h2>Messages</h2></div>
    <div id="entries"></div>
    <div id="more" class="more" hidden><button id="load-more" type="button">Show older messages</button></div>
  </section>

  <footer>
    <span class="mark"><img src="/assets/poweur-mark.svg" alt="" width="19" height="24"></span>
    <span>Every message is stored as Markdown in this guestbook's own Poweur drive.
    <a id="archive" href="#" hidden>Read the whole book as plain files</a></span>
  </footer>
</main>
<script type="module" src="/assets/page.js"></script>
`))

var resumeFailedTemplate = template.Must(template.New("resume-failed").Parse(`<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign-in not completed</title>
<link rel="stylesheet" href="/assets/guestbook.css">
<main>
  <h1>Sign-in not completed</h1>
  <p>{{.}}</p>
  <p><a href="/">Back to the guestbook</a></p>
</main>
`))

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = indexTemplate.Execute(w, map[string]any{"Name": s.cfg.Name})
}
