import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useFilesSync } from "../../src/shell/useFilesSync";
import { useData } from "../../src/state/data";
import { useSession } from "../../src/state/session";
import { resetStores } from "../helpers/stores";

const mocks = vi.hoisted(() => ({ refreshBrowserFiles: vi.fn(async () => {}) }));
vi.mock("../../src/actions/files", () => ({ refreshBrowserFiles: mocks.refreshBrowserFiles }));

describe("Files change stream", () => {
  beforeEach(() => { resetStores(); vi.clearAllMocks(); });

  it("refreshes cached listings after a drive.changed event", async () => {
    let emit: ((event: any) => Promise<void> | void) | undefined;
    const subscribe = vi.fn(async (handler: typeof emit, signal?: AbortSignal) => {
      emit = handler;
      await new Promise<void>((resolve) => signal?.addEventListener("abort", () => resolve(), { once: true }));
    });
    useSession.setState({ identity: "alice.poweur.net", unlocked: true });
    useData.setState((state) => ({ files: {
      ...state.files,
      identity: "alice.poweur.net",
      own: { files: { client: { drive: "alice.poweur.net", subscribe } } as any, root: {} as any },
      loaded: true,
    } }));

    const hook = renderHook(() => useFilesSync());
    await waitFor(() => expect(subscribe).toHaveBeenCalled());
    await act(async () => { await emit?.({ type: "drive.changed", drive: { drive: "alice.poweur.net" } }); });
    expect(mocks.refreshBrowserFiles).toHaveBeenCalledWith("alice.poweur.net");
    hook.unmount();
  });
});
