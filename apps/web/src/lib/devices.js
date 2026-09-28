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
