/*
 * Poweur service worker (EPIC-009 E09-T12): background push and the icon
 * badge for an installed identity app.
 *
 * Plain JavaScript, copied to the build root untouched, so it is served at
 * /app/sw.js and its scope is /app/ — the same origin-and-path the manifest
 * installs. It holds no keys and reads no messages: a push says which
 * identity, what kind of thing arrived and how much is waiting, and the app
 * fetches the rest when it opens.
 */

self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (event) => event.waitUntil(self.clients.claim()));

const COPY = {
  message: { title: "New message", one: "You have a new message.", many: (n) => `${n} new messages and requests are waiting.` },
  request: { title: "New contact request", one: "Someone asked to be your contact.", many: (n) => `${n} new messages and requests are waiting.` },
  anon: { title: "New anonymous message", one: "You have a new anonymous message.", many: (n) => `${n} new messages and requests are waiting.` },
  test: { title: "Notifications are on", one: "This device will be told when something arrives.", many: () => "This device will be told when something arrives." },
};

/** Where a tap lands: requests live under Contacts, everything else in Messages. */
const pageFor = (kind) => (kind === "request" ? "contacts" : "messages");

async function setBadge(count) {
  const nav = self.navigator;
  try {
    if (count > 0 && nav.setAppBadge) await nav.setAppBadge(count);
    else if (count <= 0 && nav.clearAppBadge) await nav.clearAppBadge();
  } catch {
    // Badging is a nicety; a browser that refuses it still shows the notification.
  }
}

async function onPush(event) {
  let payload = {};
  try {
    payload = event.data ? event.data.json() : {};
  } catch {
    payload = {};
  }
  const kind = COPY[payload.kind] ? payload.kind : "message";
  const count = Math.max(0, Number(payload.count) || 0);
  const identity = typeof payload.identity === "string" ? payload.identity : "";

  if (kind !== "test") await setBadge(count);

  // Open pages refresh at once instead of waiting for their stream.
  const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
  for (const client of windows) client.postMessage({ type: "poweur:push", payload });

  // Every push shows a notification: browsers revoke subscriptions that push
  // silently. The relay already skips a device whose app is open.
  const copy = COPY[kind];
  await self.registration.showNotification(identity ? `${copy.title} · ${identity}` : copy.title, {
    body: count > 1 && kind !== "test" ? copy.many(count) : copy.one,
    tag: `poweur:${identity}`,
    renotify: kind !== "test",
    icon: "icon-192.png",
    badge: "icon-192.png",
    data: { kind, identity, page: pageFor(kind) },
  });
}

self.addEventListener("push", (event) => event.waitUntil(onPush(event)));

async function onClick(event) {
  event.notification.close();
  const page = event.notification.data?.page ?? "messages";
  const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
  const open = windows.find((client) => client.url.startsWith(self.registration.scope));
  if (open) {
    open.postMessage({ type: "poweur:open", page });
    return open.focus();
  }
  return self.clients.openWindow(new URL(`./?open=${page}`, self.registration.scope).href);
}

self.addEventListener("notificationclick", (event) => event.waitUntil(onClick(event)));
