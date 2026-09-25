import { beforeEach, describe, expect, it, vi } from "vitest";

const holder = vi.hoisted(() => ({ client: null as any, setIdentity: vi.fn() }));
vi.mock("../../src/actions/relay", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  activeClient: () => holder.client,
}));
vi.mock("@poweur/faro", () => ({
  startFaro: vi.fn(),
  telemetry: () => ({ pageView() {}, event() {}, error() {}, setIdentity: holder.setIdentity, identified: false }),
  resetTelemetryForTests: vi.fn(),
}));

import { refreshAnalyticsConsent } from "../../src/actions/analytics";
import { resetObservabilityForTests } from "../../src/lib/observability";
import { useSession } from "../../src/state/session";
import { resetStores } from "../helpers/stores";

beforeEach(() => {
  resetStores();
  resetObservabilityForTests();
  holder.setIdentity.mockClear();
});

describe("the diagnostics opt-in on unlock", () => {
  it("an identity that opted in is identified", async () => {
    holder.client = { analyticsPreference: vi.fn(async () => ({ granted: true })) };
    useSession.setState({ identity: "alice.poweur.net", unlocked: true });
    await refreshAnalyticsConsent();
    expect(holder.setIdentity).toHaveBeenLastCalledWith("alice.poweur.net");
  });

  it("no opt-in, a failed read, or locked keys stay anonymous", async () => {
    useSession.setState({ identity: "alice.poweur.net", unlocked: true });
    holder.client = { analyticsPreference: vi.fn(async () => ({ granted: false })) };
    await refreshAnalyticsConsent();
    expect(holder.setIdentity).toHaveBeenLastCalledWith(null);

    holder.client = { analyticsPreference: vi.fn(async () => Promise.reject(new Error("offline"))) };
    await refreshAnalyticsConsent();
    expect(holder.setIdentity).toHaveBeenLastCalledWith(null);

    holder.client = { analyticsPreference: vi.fn(async () => ({ granted: true })) };
    useSession.setState({ unlocked: false });
    await refreshAnalyticsConsent();
    expect(holder.setIdentity).not.toHaveBeenCalledWith("alice.poweur.net");
  });
});
