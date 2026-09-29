// The only script on a generated identity page. It copies the ID and, when
// this browser already uses a Poweur ID under the same parent domain, turns
// "Message" into a one-click link from that ID. The web app on each identity
// host lists itself in the `poweur_ids` cookie (apps/web/src/lib/visit.ts);
// nothing here sends it anywhere.

const ID = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$/;

function idHints() {
  for (const part of document.cookie.split(";")) {
    const [name, ...rest] = part.trim().split("=");
    if (name !== "poweur_ids") continue;
    try {
      return [...new Set(decodeURIComponent(rest.join("=")).split("|"))].filter((id) => ID.test(id)).slice(0, 5);
    } catch {
      return [];
    }
  }
  return [];
}

function appOn(host, query) {
  const port = location.port ? `:${location.port}` : "";
  return `${location.protocol}//${host}${port}/app/${query}`;
}

function link(className, href, text) {
  const a = document.createElement("a");
  a.className = className;
  a.href = href;
  a.textContent = text;
  return a;
}

function personalize() {
  const card = document.querySelector("[data-identity]");
  const actions = document.getElementById("message-actions");
  if (!card || !actions) return;
  const identity = card.getAttribute("data-identity") || "";
  const hints = idHints();
  if (!hints.length) return;
  const primary = document.getElementById("btn-message");
  const also = document.getElementById("message-also");

  if (hints.includes(identity)) {
    // The owner, on their own page.
    const inbox = link("primary", "/app/", "Open your inbox");
    inbox.id = "btn-inbox";
    if (primary) primary.replaceWith(inbox);
    else actions.prepend(inbox);
    document.getElementById("btn-anonymous")?.remove();
    document.querySelector(".about-actions")?.remove();
    if (also) {
      also.textContent = "This is your public page. Change it in Settings → Profile.";
      also.hidden = false;
    }
    return;
  }

  const query = `?to=${encodeURIComponent(identity)}`;
  const [first, ...others] = hints;
  const message = link("primary", appOn(first, query), "");
  message.id = "btn-message";
  if (primary) {
    message.append(...[...primary.childNodes].filter((node) => node.nodeName === "svg"));
    primary.replaceWith(message);
  } else {
    actions.prepend(message);
  }
  message.append(document.createTextNode(` Message as ${first}`));
  document.getElementById("btn-message-2")?.setAttribute("href", appOn(first, query));
  const claim = document.getElementById("btn-claim");
  if (claim) claim.textContent = "Get another ID";
  if (also && others.length) {
    message.after(also);
    also.append("Or as ");
    others.forEach((other, index) => {
      if (index) also.append(", ");
      also.append(link("", appOn(other, query), other));
    });
    also.hidden = false;
  }
}

document.addEventListener("click", async (event) => {
  const button = event.target instanceof Element ? event.target.closest("[data-copy-id]") : null;
  if (!(button instanceof HTMLButtonElement)) return;
  const identity = button.dataset.copyId || "";
  const status = document.querySelector(".copy-status");
  try {
    await navigator.clipboard.writeText(identity);
    button.classList.add("copied");
    if (status) status.textContent = `${identity} copied`;
    setTimeout(() => button.classList.remove("copied"), 1500);
  } catch {
    if (status) status.textContent = `Copy this ID: ${identity}`;
  }
});

personalize();
