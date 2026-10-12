/**
 * Background push registration against a real relay (EPIC-009 E09-T11).
 * The relay is started with a VAPID key and allowed to push to a local fake
 * push service; what is asserted is the SDK's half: config, subscribe, test,
 * unsubscribe, and that the registration lands on this device's row.
 */

import { generateKeyPairSync } from "node:crypto";
import { createServer, type Server } from "node:http";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { MemorySessionStore } from "../src/session.js";
import { PoweurClient } from "../src/client.js";
import { createTestIdentity, localResolveOptions, type TestIdentity } from "./helpers/identities.js";
import { startRelay, type RunningRelay } from "./helpers/relay.js";

function vapidKey(): string {
  const { privateKey } = generateKeyPairSync("ec", { namedCurve: "P-256" });
  return privateKey.export({ format: "jwk" }).d!;
}

/** A browser-shaped subscription: a real P-256 point and a 16-byte secret. */
function subscriptionFor(endpoint: string) {
  const { publicKey } = generateKeyPairSync("ec", { namedCurve: "P-256" });
  const jwk = publicKey.export({ format: "jwk" });
  const point = Buffer.concat([
    Buffer.from([4]),
    Buffer.from(jwk.x!, "base64url"),
    Buffer.from(jwk.y!, "base64url"),
  ]);
  return {
    endpoint,
    keys: {
      p256dh: point.toString("base64url"),
      auth: Buffer.from(crypto.getRandomValues(new Uint8Array(16))).toString("base64url"),
    },
  };
}

describe("background push registration", () => {
  let relay: RunningRelay;
  let alice: TestIdentity;
  let phone: PoweurClient;
  let pushService: Server;
  let pushURL = "";
  const received: { headers: Record<string, unknown> }[] = [];

  beforeAll(async () => {
    pushService = createServer((req, res) => {
      req.resume();
      received.push({ headers: req.headers });
      res.writeHead(201).end();
    });
    await new Promise<void>((resolve) => pushService.listen(0, "127.0.0.1", resolve));
    const address = pushService.address();
    pushURL = `http://127.0.0.1:${typeof address === "object" && address ? address.port : 0}/push/phone`;

    relay = await startRelay({
      env: {
        PUSH_VAPID_PRIVATE_KEY: vapidKey(),
        PUSH_VAPID_SUBJECT: "mailto:ops@poweur.test",
        PUSH_ENDPOINT_HOSTS: "127.0.0.1",
        PUSH_COALESCE: "50ms",
      },
    });
    alice = await createTestIdentity(relay.baseUrl, "pushalice");
    phone = new PoweurClient({
      relayUrl: relay.baseUrl,
      signer: alice.client.signer,
      decryptor: alice.client.decryptor,
      sessionStore: new MemorySessionStore(),
      resolve: localResolveOptions(relay.baseUrl),
      headers: { "X-Poweur-Device": "alice-phone-fingerprint", "X-Poweur-Device-Kind": "phone" },
    });
  }, 180_000);

  afterAll(() => {
    relay?.stop();
    pushService?.close();
  });

  it("reads the relay's push config", async () => {
    const config = await phone.push().config();
    expect(config.enabled).toBe(true);
    expect(config.vapid_public_key).toMatch(/^[A-Za-z0-9_-]{87}$/);
  });

  it("subscribes this device, pushes a test, and unsubscribes", async () => {
    const registered = await phone.push().subscribe(subscriptionFor(pushURL));
    expect(registered.enabled).toBe(true);
    expect(registered.device_id).toMatch(/^dev_/);

    const { devices } = await phone.devices().list();
    const row = devices.find((d) => d.id === registered.device_id);
    expect(row?.push?.transport).toBe("webpush");
    expect(row?.push?.endpoint).toBe(pushURL);

    expect(await phone.push().test()).toEqual({ sent: true });
    expect(received).toHaveLength(1);
    expect(received[0]!.headers["content-encoding"]).toBe("aes128gcm");
    expect(String(received[0]!.headers.authorization)).toMatch(/^vapid t=/);

    const off = await phone.push().unsubscribe();
    expect(off.enabled).toBe(false);
    const after = await phone.devices().list();
    expect(after.devices.find((d) => d.id === registered.device_id)?.push).toBeUndefined();
  });

  it("refuses an endpoint that is not a push service", async () => {
    await expect(phone.push().subscribe(subscriptionFor("https://evil.example.com/x"))).rejects.toMatchObject({
      status: 403,
    });
  });
});
