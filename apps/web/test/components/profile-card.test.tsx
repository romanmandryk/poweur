import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ProfileCard } from "../../src/components/ProfileCard";
import { entry, resolveFail, resolveOk } from "./fixtures";

const card = (container: HTMLElement) => container.querySelector<HTMLElement>(".profile-card")!;

describe("ProfileCard", () => {
  it("paints a skeleton, then the resolved identity", async () => {
    const { container } = render(<ProfileCard identity="alice.poweur.net" resolve={resolveOk} />);
    expect(card(container).dataset.state).toBe("loading");
    expect(container.textContent).toContain("Resolving…");

    await waitFor(() => expect(card(container).dataset.state).toBe("ready"));
    expect(container.textContent).toContain("alice.poweur.net");
    expect(container.textContent).toContain("messaging"); // capability chip
  });

  it("prefers the profile's display name over the handle", async () => {
    const { container } = render(
      <ProfileCard
        identity="alice.poweur.net"
        resolve={(id) => Promise.resolve(entry(id, { displayName: "Alice Example", bio: "hi" }))}
      />,
    );
    await waitFor(() => expect(container.querySelector(".profile-card-name")!.textContent).toBe("Alice Example"));
    expect(container.textContent).toContain("hi");
  });

  it("says so when an identity cannot be resolved, instead of rendering nothing", async () => {
    const { container } = render(<ProfileCard identity="ghost.poweur.net" resolve={resolveFail} />);
    await waitFor(() => expect(card(container).dataset.state).toBe("error"));
    expect(container.textContent).toContain("Could not resolve");
  });

  it("renders a contextual action and passes the identity back", async () => {
    const onSelect = vi.fn();
    render(<ProfileCard identity="alice.poweur.net" resolve={resolveOk} action={{ label: "Message", onSelect }} />);
    const button = await screen.findByRole("button", { name: "Message" });
    await waitFor(() => expect(button).toBeEnabled());
    fireEvent.click(button);
    expect(onSelect).toHaveBeenCalledWith("alice.poweur.net", expect.objectContaining({ identity: "alice.poweur.net" }));
  });

  it("disables an action that needs a resolved identity when it did not resolve", async () => {
    const { container } = render(
      <ProfileCard identity="ghost.poweur.net" resolve={resolveFail} action={{ label: "Message", onSelect: () => {} }} />,
    );
    await waitFor(() => expect(card(container).dataset.state).toBe("error"));
    expect(screen.getByRole("button", { name: "Message" })).toBeDisabled();
  });

  it("never renders a javascript: link from a resolved profile", async () => {
    const { container } = render(
      <ProfileCard
        identity="evil.poweur.net"
        resolve={(id) =>
          Promise.resolve(entry(id, { links: [{ label: "click", url: "javascript:alert(1)" }, { label: "ok", url: "https://example.com" }] }))
        }
      />,
    );
    await waitFor(() => expect(card(container).dataset.state).toBe("ready"));
    const hrefs = [...container.querySelectorAll("a")].map((a) => a.getAttribute("href"));
    expect(hrefs).toEqual(["https://example.com"]);
  });

  it("escapes hostile text rather than parsing it as markup", async () => {
    const { container } = render(
      <ProfileCard
        identity="evil.poweur.net"
        resolve={(id) => Promise.resolve(entry(id, { displayName: "<img src=x onerror=alert(1)>" }))}
      />,
    );
    await waitFor(() => expect(card(container).dataset.state).toBe("ready"));
    expect(container.querySelector("img")).toBeNull();
    expect(container.textContent).toContain("<img src=x onerror=alert(1)>");
  });

  it("paints a cached entry without resolving again", () => {
    const resolve = vi.fn(resolveOk);
    const { container } = render(
      <ProfileCard identity="alice.poweur.net" resolve={resolve} cached={entry("alice.poweur.net")} />,
    );
    expect(card(container).dataset.state).toBe("ready");
    expect(resolve).not.toHaveBeenCalled();
  });
});
