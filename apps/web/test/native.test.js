/**
 * @vitest-environment happy-dom
 *
 * The native custody seam (EPIC-019 E19-T2). The platform half is Swift and
 * Kotlin and cannot run here; what *can* be tested is everything this side of
 * the contract — that a browser is unaffected, that the wrapped record is the
 * same shape the other two kdfs produce, and that the failure a device
 * actually hits (the OS invalidated the key) is reported as something the user
 * can act on rather than as "unlock failed".
 */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

import {
  hasNativeKeystore, isNativeShell, wrapKeysNative, unwrapKeysNative,
  forgetNativeSecret, biometricAvailability, GATE_BIOMETRIC, GATE_DEVICE,
} from "../js/native.js";
import { generateIdentityJwks } from "../js/vault.js";

/** A plugin that behaves like the real one, without Keychain or Keystore. */
function fakePlugin({ invalidated = false } = {}) {
  const secrets = new Map();
  const prompts = [];
  return {
    prompts,
    secrets,
    async setSecret({ key, value, gate, reason }) {
      prompts.push(reason);
      secrets.set(key, { value, gate });
    },
    async getSecret({ key, reason }) {
      prompts.push(reason);
      if (invalidated) return { value: null };
      return { value: secrets.get(key)?.value ?? null };
    },
    async deleteSecret({ key }) { secrets.delete(key); },
    async canUseBiometrics() { return { available: true }; },
  };
}

function install(plugin) {
  globalThis.Capacitor = { isNativePlatform: () => true, Plugins: { PoweurKeystore: plugin } };
}

afterEach(() => { delete globalThis.Capacitor; });

describe("in a plain browser", () => {
  it("reports no native custody and refuses to pretend", async () => {
    expect(hasNativeKeystore()).toBe(false);
    expect(isNativeShell()).toBe(false);
    await expect(wrapKeysNative("alice.poweur.net", {}, {})).rejects.toThrow(/not available/);
    expect(await biometricAvailability()).toEqual({
      available: false, reason: "no_native_keystore",
    });
  });
});

describe("in a shell with a keystore", () => {
  let keys;

  beforeEach(async () => {
    keys = await generateIdentityJwks();
  });

  it("round-trips keys through a hardware-held secret", async () => {
    const plugin = fakePlugin();
    install(plugin);

    const wrapped = await wrapKeysNative(
      "alice.poweur.net", keys.signingJWK, keys.encJWK, "seedbytes",
    );
    // The record is the same shape as prf/pbkdf2 — only `kdf` differs, which
    // is what lets the rest of the app stay ignorant of custody.
    expect(wrapped.kdf).toBe("native");
    expect(wrapped.iv).toBeTruthy();
    expect(wrapped.ciphertext).toBeTruthy();
    // The keys themselves are never handed to the plugin: it holds 32 random
    // bytes and knows nothing about identities.
    expect(JSON.stringify([...plugin.secrets.values()])).not.toContain(keys.signingJWK.d);

    const opened = await unwrapKeysNative("alice.poweur.net", wrapped);
    expect(opened.signingJWK).toEqual(keys.signingJWK);
    expect(opened.encJWK).toEqual(keys.encJWK);
    expect(opened.seed).toBe("seedbytes");
  });

  it("gates on biometrics by default and says what is being authorised", async () => {
    const plugin = fakePlugin();
    install(plugin);

    const wrapped = await wrapKeysNative("alice.poweur.net", keys.signingJWK, keys.encJWK);
    expect(wrapped.gate).toBe(GATE_BIOMETRIC);
    await unwrapKeysNative("alice.poweur.net", wrapped, { reason: "Send a message as alice" });
    expect(plugin.prompts).toContain("Send a message as alice");

    // Device-gated storage exists only for the background path that cannot prompt.
    const background = await wrapKeysNative(
      "alice.poweur.net", keys.signingJWK, keys.encJWK, null, { gate: GATE_DEVICE },
    );
    expect(background.gate).toBe(GATE_DEVICE);
  });

  it("keeps one secret per identity", async () => {
    const plugin = fakePlugin();
    install(plugin);
    await wrapKeysNative("alice.poweur.net", keys.signingJWK, keys.encJWK);
    await wrapKeysNative("bob.poweur.net", keys.signingJWK, keys.encJWK);
    expect(plugin.secrets.size).toBe(2);

    // Removing one identity must not lock the others out.
    await forgetNativeSecret("alice.poweur.net");
    expect(plugin.secrets.has("poweur.identity.bob.poweur.net")).toBe(true);
    expect(plugin.secrets.has("poweur.identity.alice.poweur.net")).toBe(false);
  });

  it("explains an OS-invalidated key instead of reporting a failed unlock", async () => {
    install(fakePlugin());
    const wrapped = await wrapKeysNative("alice.poweur.net", keys.signingJWK, keys.encJWK);

    // What a changed fingerprint enrolment, or a restore onto a new device,
    // looks like from here: the entry is simply gone.
    install(fakePlugin({ invalidated: true }));
    await expect(unwrapKeysNative("alice.poweur.net", wrapped))
      .rejects.toThrow(/Add the device again|recovery kit/);
  });

  it("says what storing is authorising, because Android prompts on write too", async () => {
    const plugin = fakePlugin();
    install(plugin);
    // Android's Keystore gates the key rather than the direction, so writing
    // under a biometric gate raises a prompt. Without a reason the user is
    // asked to authenticate for no stated purpose.
    await wrapKeysNative("alice.poweur.net", keys.signingJWK, keys.encJWK, null, {
      reason: "Protect alice",
    });
    expect(plugin.prompts).toContain("Protect alice");

    await wrapKeysNative("bob.poweur.net", keys.signingJWK, keys.encJWK);
    expect(plugin.prompts.filter(Boolean).every((reason) => reason.length > 0)).toBe(true);
  });

  it("survives a plugin that throws when asked about biometrics", async () => {
    install({ ...fakePlugin(), canUseBiometrics: () => { throw new Error("no sensor"); } });
    expect((await biometricAvailability()).available).toBe(false);
  });
});
