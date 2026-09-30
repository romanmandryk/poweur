/**
 * The device registry's presentation half (EPIC-004 E04-T6).
 *
 * `poweur-sys/relay/devices.json` is written by the relay from its own
 * observations; the owner reads it. These helpers turn a row into the two
 * things the UI actually says about a device — what it is, and how stale it
 * is — and live here rather than in `app.js` so they can be tested without
 * booting the shell.
 *
 * **Presence is not user-visible in v1.** That decision (EPIC-009 E09-T2) is
 * about *peers*: no one but the owner can learn when a device was last seen.
 * Showing the owner their own devices is the whole point of the registry.
 */

/** Icons for the kinds devices.json accepts. */
export const DEVICE_KIND_ICON = {
  laptop: "💻",
  desktop: "🖥️",
  phone: "📱",
  tablet: "📲",
  browser: "🌐",
  agent: "🤖",
  unknown: "💠",
};

export function deviceIcon(kind) {
  return DEVICE_KIND_ICON[kind] ?? DEVICE_KIND_ICON.unknown;
}

/**
 * "phone last synced 3 days ago" — from the relay's own record of the sync
 * cursor, so it is answerable while the phone is off.
 *
 * The cursor moving is the only honest evidence of a sync: a device can hold
 * a stream open for a week and copy nothing, so `last_seen` is not this.
 */
export function describeDeviceSync(device, now = Date.now()) {
  if (!device?.synced_at) return "never synced";
  const at = Date.parse(device.synced_at);
  if (Number.isNaN(at)) return "never synced";
  const mins = Math.max(0, Math.round((now - at) / 60_000));
  if (mins < 1) return "synced just now";
  if (mins < 60) return `synced ${mins} min ago`;
  const hours = Math.round(mins / 60);
  if (hours < 24) return `synced ${hours} h ago`;
  const days = Math.round(hours / 24);
  return `synced ${days} day${days === 1 ? "" : "s"} ago`;
}

/** The one-line summary under a device's name. */
export function describeDevice(device, now = Date.now()) {
  const parts = [
    device?.kind || "unknown",
    device?.last_seen ? `last seen ${formatWhen(device.last_seen)}` : "never seen",
    describeDeviceSync(device, now),
  ];
  return parts.join(" · ");
}

function formatWhen(ts) {
  try {
    return new Date(ts).toLocaleString();
  } catch {
    return ts;
  }
}

/**
 * What this browser or app calls itself in the owner's device list: a name a
 * person recognises, plus the client type, platform and browser the relay
 * records as separate fields.
 *
 * The shell's WebView still reports Safari or Chrome. That is the wrapper,
 * not the product — the key lives in the OS keystore, so the app is "iPhone"
 * (client `app`), not "Safari on iOS".
 */
export function describeThisDevice(userAgent = "", { native = false } = {}) {
  const ua = userAgent ?? "";
  const platform =
    /iPhone|iPad/i.test(ua) ? "iOS" :
    /Android/i.test(ua) ? "Android" :
    /Mac OS X/i.test(ua) ? "macOS" :
    /Windows/i.test(ua) ? "Windows" :
    /Linux/i.test(ua) ? "Linux" : "";
  if (native) {
    const name = /iPad/i.test(ua) ? "iPad" : /iPhone/i.test(ua) ? "iPhone" : /Android/i.test(ua) ? "Android" : "This device";
    return { name, client: "app", platform, browser: "", kind: /iPad/i.test(ua) ? "tablet" : platform === "iOS" || platform === "Android" ? "phone" : "unknown" };
  }
  const browser =
    /Edg\//.test(ua) ? "Edge" :
    /Firefox\//.test(ua) ? "Firefox" :
    /Chrome\//.test(ua) ? "Chrome" :
    /Safari\//.test(ua) ? "Safari" : "browser";
  const where = { macOS: "Mac" }[platform] ?? (platform || "Device");
  return { name: `${browser} on ${where}`, client: "web", platform, browser: browser === "browser" ? "" : browser, kind: "browser" };
}

/**
 * The optional device headers for every relay request. The fingerprint is
 * random and local to this browser; the relay keeps only its hash.
 */
export function deviceHeadersFor(fingerprint, info, enrollmentId = "") {
  const headers = { "X-Poweur-Device": fingerprint };
  const set = (name, value) => { if (value) headers[`X-Poweur-Device-${name}`] = value; };
  set("Name", info.name);
  set("Kind", info.kind);
  set("Client", info.client);
  set("Platform", info.platform);
  set("Browser", info.browser);
  set("Enrollment", enrollmentId);
  return headers;
}

/**
 * How one row reads: what it is and where, then when. Name and browser come
 * first in the row, so this is only the second line.
 */
export function describeDeviceRow(device) {
  const clientLabel = { app: "App", web: "Web", cli: "CLI" }[device?.client] ?? "";
  const parts = [
    clientLabel || device?.kind || "unknown",
    device?.platform,
    device?.browser,
    device?.added_at ? `added ${formatWhen(device.added_at)}` : null,
    device?.last_seen ? `last used ${formatWhen(device.last_seen)}` : "never used",
  ].filter(Boolean);
  return parts.join(" · ");
}

/**
 * One list out of the two things the relay knows: the device registry (who
 * is using the identity) and the keystore (whose passkey or OS keystore holds
 * a backup copy). A device's `enrollment_id` joins them; a keystore entry
 * nobody has claimed — a client from before devices reported themselves —
 * stays as its own row rather than vanishing.
 *
 * Current device first, revoked last, the rest by most recent activity.
 */
export function mergeKeysAndDevices(enrollments = [], registry = []) {
  const byEnrollment = new Map(enrollments.map((entry) => [entry.enrollment_id, entry]));
  const claimed = new Set();
  const rows = [];
  for (const device of registry ?? []) {
    const enrollment = device.enrollment_id ? byEnrollment.get(device.enrollment_id) : undefined;
    if (enrollment) claimed.add(enrollment.enrollment_id);
    rows.push({ id: `device:${device.id}`, device, enrollment, current: Boolean(enrollment?.current) });
  }
  for (const enrollment of enrollments) {
    if (!claimed.has(enrollment.enrollment_id)) {
      rows.push({ id: `enrollment:${enrollment.enrollment_id}`, enrollment, current: Boolean(enrollment.current) });
    }
  }
  const activity = (row) =>
    Date.parse(row.device?.last_seen ?? row.enrollment?.last_used_at ?? row.enrollment?.created_at ?? "") || 0;
  return rows.sort((a, b) =>
    Number(Boolean(a.device?.revoked)) - Number(Boolean(b.device?.revoked)) ||
    Number(b.current) - Number(a.current) ||
    activity(b) - activity(a));
}

/**
 * The one line about restorability. It is a note, not a class of device:
 * every row can enrol and approve others; they differ only in whether the
 * relay holds a passkey-wrapped copy that survives a reinstall.
 */
export function restoreNote(row) {
  const e = row.enrollment;
  if (e?.has_passkey && e.wrap === "prf") return "Passkey backup: signing in with the passkey restores your keys after a reinstall.";
  if (e) return "Keeps its keys in this device's secure storage. After a reinstall, pair it again from another device.";
  return "Keeps its keys on this machine only. If they are lost, pair it again from another device.";
}
