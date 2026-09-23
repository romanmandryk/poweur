/**
 * Is this a phone or tablet — somewhere the Poweur app could be installed?
 * Decides which way of approving leads: on a desktop the phone (QR, code, a
 * push) comes first and "open the app on this device" is never offered.
 */
export function isHandheld(nav: Navigator = navigator): boolean {
  const hint = (nav as Navigator & { userAgentData?: { mobile?: boolean } }).userAgentData?.mobile;
  if (typeof hint === "boolean") return hint;
  const ua = nav.userAgent || "";
  if (/Android|iPhone|iPad|iPod|Mobile/i.test(ua)) return true;
  // iPadOS reports a Mac user agent; it has touch and no fine pointer.
  return /Macintosh/.test(ua) && nav.maxTouchPoints > 1;
}
