import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, waitFor } from "@testing-library/react";

const mocks = vi.hoisted(() => ({ sendAnonymous: vi.fn() }));
vi.mock("@poweur/client", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  sendAnonymous: mocks.sendAnonymous,
}));
vi.mock("../../src/lib/client.js", () => ({
  resolveOptionsForRelay: (relayUrl: string) => ({ relayUrl, skipDns: true }),
}));
vi.mock("../../src/lib/storage.js", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  defaultRelayUrl: vi.fn(() => "https://alice.poweur.net"),
  identityOriginUrl: vi.fn(() => "https://alice.poweur.net"),
}));

import { isPublicAnonymousRoute, publicAnonymousFailure, PublicAnonymousComposer } from "../../src/screens/PublicAnonymous";
import { useSession } from "../../src/state/session";

const $ = <T extends Element = HTMLElement>(selector: string) => document.querySelector<T>(selector);

beforeEach(() => {
  mocks.sendAnonymous.mockReset();
  mocks.sendAnonymous.mockResolvedValue({ id: "anon_1", status: 202, targetRelay: "https://alice.poweur.net" });
  useSession.setState({
    identity: "someone-else.poweur.net",
    unlocked: true,
    mode: { mode: "identity", subject: "alice.poweur.net", probed: true },
  });
});

describe("public anonymous composer", () => {
  it("recognises only the explicit public route", () => {
    expect(isPublicAnonymousRoute("?anonymous=1")).toBe(true);
    expect(isPublicAnonymousRoute("?anonymous=0")).toBe(false);
    expect(isPublicAnonymousRoute("?auth=abc")).toBe(false);
  });

  it("sends unsigned to the host identity without offering stored credentials", async () => {
    render(<PublicAnonymousComposer />);
    expect(document.body.textContent).toContain("Write to alice.poweur.net");
    expect(document.body.textContent).not.toContain("someone-else.poweur.net");
    expect(document.body.textContent).not.toContain("Unlock");

    fireEvent.change($("#public-anon-body")!, { target: { value: "Hello privately" } });
    fireEvent.click($("#public-anon-send")!);
    await waitFor(() => expect(mocks.sendAnonymous).toHaveBeenCalled());
    expect(mocks.sendAnonymous.mock.calls[0][0]).toBe("alice.poweur.net");
    expect(mocks.sendAnonymous.mock.calls[0][1]).toBe("Hello privately");
    expect(mocks.sendAnonymous.mock.calls[0][2]).toEqual(expect.objectContaining({ targetRelayUrl: "https://alice.poweur.net" }));
    const { defaultRelayUrl, identityOriginUrl } = await import("../../src/lib/storage.js");
    expect(defaultRelayUrl).toHaveBeenCalledWith();
    expect(identityOriginUrl).toHaveBeenCalledWith("alice.poweur.net", "https://alice.poweur.net");
    await waitFor(() => expect($("#public-anon-status")!.textContent).toContain("sent anonymously"));
  });

  it("treats a policy rejection as a stale advertisement", async () => {
    mocks.sendAnonymous.mockRejectedValue(Object.assign(
      new Error(`relay rejected anonymous message (403): {"error":"policy_rejected","detail":"recipient does not accept anonymous messages"}`),
      { detail: `{"error":"policy_rejected","detail":"recipient does not accept anonymous messages","mode":"contacts_only","pow_bits":18}` },
    ));
    render(<PublicAnonymousComposer />);
    fireEvent.change($("#public-anon-body")!, { target: { value: "Hello" } });
    fireEvent.click($("#public-anon-send")!);
    await waitFor(() => expect($("#public-anon-status")!.textContent).toContain("not accepting anonymous messages"));
    const status = $("#public-anon-status")!.textContent ?? "";
    expect(status).not.toContain("policy_rejected");
    expect(status).not.toContain("contacts_only");
    expect(status).not.toContain("pow_bits");
    expect(publicAnonymousFailure(new Error("rate_limit_exceeded"))).toContain("Try again later");
  });

  it("refuses the route when the host is not an identity", () => {
    useSession.setState({ mode: { mode: "launcher", subject: "", probed: true } });
    render(<PublicAnonymousComposer />);
    expect(document.body.textContent).toContain("Anonymous messaging unavailable");
    expect($("#public-anon-send")).toBeNull();
  });
});
