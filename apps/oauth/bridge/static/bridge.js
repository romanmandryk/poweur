// The only script the bridge serves: poll a waiting sign-in, copy buttons.
(() => {
  "use strict";
  for (const button of document.querySelectorAll("[data-copy]")) {
    button.addEventListener("click", async () => {
      const target = document.getElementById(button.dataset.copy);
      if (!target) return;
      try {
        await navigator.clipboard.writeText(target.value ?? target.textContent);
        button.textContent = "Copied";
      } catch {
        target.select?.();
      }
    });
  }

  setupCreate();

  const card = document.querySelector("[data-status-url]");
  if (!card) return;
  const statusLine = document.getElementById("status");
  const url = card.dataset.statusUrl;
  let stopped = false;

  const say = (text) => { if (statusLine) statusLine.textContent = text; };

  async function tick() {
    if (stopped) return;
    try {
      const res = await fetch(url, { credentials: "same-origin", cache: "no-store" });
      const st = await res.json();
      switch (st.status) {
        case "complete":
          stopped = true;
          say("Approved. Continuing…");
          location.assign(st.next);
          return;
        case "approved":
          say("Approved — finishing in the browser you approved from…");
          break;
        case "failed":
        case "expired":
          stopped = true;
          say(st.error || "This sign-in did not complete.");
          return;
        default:
          break;
      }
    } catch {
      say("Connection lost — retrying…");
    }
    setTimeout(tick, 1500);
  }
  setTimeout(tick, 1000);

  // "Don't have a Poweur ID?": check the name at the launcher as it is typed,
  // open the launcher in a new tab, and notice when the name has been taken —
  // by this visitor, a moment ago — so this sign-in carries on with it.
  function setupCreate() {
    const section = document.querySelector("[data-create]");
    if (!section) return;
    const launcher = section.dataset.launcher;
    const domain = section.dataset.domain;
    const input = document.getElementById("new-handle");
    const status = document.getElementById("new-handle-status");
    const link = document.getElementById("create-id");
    const idField = document.getElementById("identity");
    const ready = document.getElementById("created-ready");
    const suffix = "." + domain;
    let handle = "";
    let available = false;
    let watching = "";
    let watchUntil = 0;
    let backoffUntil = 0;
    let typingTimer;

    const normalize = (value) => {
      let v = String(value || "").trim().toLowerCase().replace(/^@/, "");
      if (v.endsWith(suffix)) v = v.slice(0, -suffix.length);
      return v;
    };
    const say = (text, ok) => {
      status.textContent = text;
      status.classList.toggle("ok", Boolean(ok));
    };
    const ask = async (name) => {
      if (Date.now() < backoffUntil) return null;
      const url = `${launcher}/hosted/availability?handle=${encodeURIComponent(name)}&domain=${encodeURIComponent(domain)}`;
      const res = await fetch(url, { credentials: "omit", cache: "no-store" });
      if (res.status === 429) {
        backoffUntil = Date.now() + 60_000;
        return null;
      }
      return res.ok ? res.json() : null;
    };
    const setLink = () => {
      const q = new URLSearchParams({ from: "signin" });
      if (handle && available) q.set("handle", handle);
      link.href = `${launcher}/app/?${q}`;
    };

    input.addEventListener("input", () => {
      handle = normalize(input.value);
      available = false;
      setLink();
      clearTimeout(typingTimer);
      if (!handle) return say("");
      say("Checking…");
      typingTimer = setTimeout(async () => {
        const mine = handle;
        try {
          const verdict = await ask(mine);
          if (mine !== handle) return;
          if (!verdict) return say("Couldn't check right now — you can still create it.");
          available = Boolean(verdict.available);
          say(available ? `${verdict.identity} is available` : verdict.message, available);
          setLink();
        } catch {
          say("Couldn't check right now — you can still create it.");
        }
      }, 450);
    });

    link.addEventListener("click", () => {
      fetch(section.dataset.creatingUrl, { method: "POST", credentials: "same-origin" }).catch(() => {});
      if (handle && available) {
        watching = handle;
        watchUntil = Date.now() + 30 * 60_000;
        say(`Create ${handle}${suffix} in the new tab, then come back here.`);
      } else {
        say("When you have created your ID, come back and enter it above.");
      }
    });

    const look = async () => {
      if (!watching || Date.now() > watchUntil) return;
      try {
        const verdict = await ask(watching);
        if (verdict && !verdict.available && verdict.reason === "taken") {
          const id = verdict.identity || watching + suffix;
          idField.value = id;
          ready.textContent = `${id} is ready — continue to sign in with it.`;
          ready.hidden = false;
          watching = "";
          idField.focus();
          ready.scrollIntoView({ block: "center" });
        }
      } catch { /* try again later */ }
    };
    setInterval(look, 20_000);
    document.addEventListener("visibilitychange", () => {
      if (document.visibilityState === "visible") look();
    });
    window.addEventListener("focus", look);
  }
})();
