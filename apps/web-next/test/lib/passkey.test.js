/**
 * @vitest-environment happy-dom
 *
 * Browser/authenticator support: the web app requires WebAuthn PRF. A PIN is
 * not a fallback.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

import { checkPasskeySupport, PRF_UNAVAILABLE_MESSAGE } from "../../src/lib/passkey.js";

describe("checkPasskeySupport", () => {
  beforeEach(() => {
    vi.stubGlobal("PublicKeyCredential", {
      isUserVerifyingPlatformAuthenticatorAvailable: async () => true,
      getClientCapabilities: async () => ({ "extension:prf": true }),
    });
  });
  afterEach(() => vi.unstubAllGlobals());

  it("reports PRF when the browser advertises the extension", async () => {
    expect(await checkPasskeySupport()).toEqual({ available: true, prf: true, reason: null });
  });

  it("fails closed when the browser has no PRF", async () => {
    PublicKeyCredential.getClientCapabilities = async () => ({ "extension:prf": false });
    expect(await checkPasskeySupport()).toEqual({
      available: true,
      prf: false,
      reason: PRF_UNAVAILABLE_MESSAGE,
    });
  });

  it("fails closed when there is no WebAuthn", async () => {
    vi.stubGlobal("PublicKeyCredential", undefined);
    expect(await checkPasskeySupport()).toEqual({
      available: false,
      prf: false,
      reason: PRF_UNAVAILABLE_MESSAGE,
    });
  });
});
