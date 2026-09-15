import { createRef } from "react";
import { describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, waitFor } from "@testing-library/react";
import { IdentityInput, type IdentityInputHandle } from "../../src/components/IdentityInput";
import { avatarColor, handleOf, isValidIdentity } from "../../src/lib/identity";
import { entry, resolveFail, resolveOk, settle } from "./fixtures";

function setup(props: Partial<Parameters<typeof IdentityInput>[0]> = {}) {
  const ref = createRef<IdentityInputHandle>();
  const utils = render(<IdentityInput ref={ref} resolve={resolveOk} {...props} />);
  const input = utils.container.querySelector<HTMLInputElement>(".idin input")!;
  const type = (value: string) => fireEvent.input(input, { target: { value } });
  return { ...utils, ref, input, type, handle: () => ref.current! };
}

describe("isValidIdentity", () => {
  it("accepts FQDNs and rejects everything a user typos", () => {
    for (const good of ["alice.poweur.net", "a-b.example.com", "x.co"]) {
      expect(isValidIdentity(good), good).toBe(true);
    }
    for (const bad of ["", "alice", "alice.", ".net", "alice space.net", "-alice.net", "alice..net"]) {
      expect(isValidIdentity(bad), bad).toBe(false);
    }
  });

  it("is case-insensitive, matching the relay", () => {
    expect(isValidIdentity("Alice.Poweur.NET")).toBe(true);
  });

  it("gives an identity a stable avatar colour", () => {
    expect(avatarColor("alice.poweur.net")).toBe(avatarColor("alice.poweur.net"));
    expect(handleOf("alice.poweur.net")).toBe("alice");
  });
});

describe("IdentityInput", () => {
  it("rejects a malformed identity without calling the resolver", async () => {
    const resolve = vi.fn(resolveOk);
    const onChange = vi.fn();
    const { input, container, handle } = setup({ resolve, onChange });

    input.value = "not an id";
    await act(() => handle().lookup());

    expect(resolve).not.toHaveBeenCalled();
    expect(onChange).toHaveBeenCalledWith(null);
    expect(container.textContent).toContain("does not look like a Poweur ID");
    expect(handle().value()).toBeNull();
  });

  it("resolves a valid identity and reports it", async () => {
    const onChange = vi.fn();
    const { input, container, handle } = setup({ onChange, preview: false });

    input.value = "alice.poweur.net";
    await act(() => handle().lookup());

    expect(handle().value()).toMatchObject({ identity: "alice.poweur.net" });
    expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ identity: "alice.poweur.net" }));
    expect(container.querySelector(".idin-status")!.textContent).toBe("Found");
  });

  it("completes a bare handle with the default domain, and says so", async () => {
    const { input, container, handle } = setup({ preview: false, defaultDomain: "poweur.net" });
    input.value = "alice";
    await act(() => handle().lookup());
    expect(handle().value()).toMatchObject({ identity: "alice.poweur.net" });
    expect(container.querySelector(".idin-status")!.textContent).toBe("Found alice.poweur.net");
  });

  it("surfaces an unresolvable identity instead of accepting it", async () => {
    const { input, container, handle } = setup({ resolve: resolveFail, preview: false });
    input.value = "ghost.poweur.net";
    await act(() => handle().lookup());
    expect(handle().value()).toBeNull();
    expect(container.textContent).toContain("Not found");
  });

  it("shows a profile preview for what it resolved", async () => {
    const { container, handle } = setup();
    await act(() => handle().setValue("alice.poweur.net"));
    expect(container.querySelector(".profile-card")).toBeTruthy();
  });

  it("autocompletes from contacts and picks by pointer", async () => {
    const { container, input, type, handle } = setup({
      preview: false,
      contacts: [
        { identity: "alice.poweur.net", petname: "Alice" },
        { identity: "bob.poweur.net", petname: "Bob" },
      ],
    });

    type("ali");
    const options = container.querySelectorAll(".idin-suggestion");
    expect(options).toHaveLength(1);
    expect(options[0].textContent).toContain("Alice");

    fireEvent.mouseDown(options[0]);
    await waitFor(() => expect(handle().value()).toMatchObject({ identity: "alice.poweur.net" }));
    expect(input.value).toBe("alice.poweur.net");
  });

  it("a forced lookup is not discarded by the input debounce", async () => {
    // Typing arms a 400ms debounce; Send calls lookup() immediately. Left armed,
    // the debounce starts a second resolve and the submit resolves to null.
    const resolve = (identity: string) => new Promise<ReturnType<typeof entry>>((r) => setTimeout(() => r(entry(identity)), 450));
    const { type, handle } = setup({ resolve, preview: false });
    type("alice.poweur.net");
    const result = await act(() => handle().lookup());
    expect(result?.identity).toBe("alice.poweur.net");
    expect(handle().value()).toMatchObject({ identity: "alice.poweur.net" });
  });

  it("keeps the last lookup's answer when an earlier one lands late", async () => {
    const slow = (identity: string) =>
      identity.startsWith("slow")
        ? new Promise<ReturnType<typeof entry>>((r) => setTimeout(() => r(entry(identity)), 40))
        : Promise.resolve(entry(identity));

    const { input, handle } = setup({ resolve: slow, preview: false });
    input.value = "slow.poweur.net";
    const first = handle().lookup();
    input.value = "fast.poweur.net";
    await act(() => handle().lookup());
    await act(async () => {
      await first;
      await settle(60);
    });

    expect(handle().value()).toMatchObject({ identity: "fast.poweur.net" });
  });

  it("keyboard-navigates the suggestion list", async () => {
    const { container, input, type } = setup({
      preview: false,
      contacts: [
        { identity: "alice.poweur.net", petname: "Alice" },
        { identity: "alina.poweur.net", petname: "Alina" },
      ],
    });
    type("ali");

    fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(container.querySelectorAll(".idin-suggestion")[0].classList.contains("active")).toBe(true);

    fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(container.querySelectorAll(".idin-suggestion")[1].classList.contains("active")).toBe(true);

    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(input.value).toBe("alina.poweur.net"));
  });

  it("submits a resolved identity on Enter", async () => {
    const onSubmit = vi.fn();
    const { type, input } = setup({ preview: false, onSubmit });
    type("bob.poweur.net");
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith("bob.poweur.net"));
  });
});
