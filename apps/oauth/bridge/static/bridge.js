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
})();
