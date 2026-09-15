import { createRef } from "react";
import { describe, expect, it, vi } from "vitest";
import { act, fireEvent, render } from "@testing-library/react";
import { AudiencePicker, type AudiencePickerHandle } from "../../src/components/AudiencePicker";
import { resolveOk } from "./fixtures";

const contacts = [
  { identity: "alice.poweur.net", petname: "Alice" },
  { identity: "bob.poweur.net", petname: "Bob" },
];

function setup(props: Partial<Parameters<typeof AudiencePicker>[0]> = {}) {
  const ref = createRef<AudiencePickerHandle>();
  const utils = render(<AudiencePicker ref={ref} resolve={resolveOk} contacts={contacts} {...props} />);
  return { ...utils, picker: () => ref.current! };
}

describe("AudiencePicker", () => {
  it("toggles contacts and reports the selection", () => {
    const onChange = vi.fn();
    const { container, picker } = setup({ onChange });

    expect(picker().selection()).toEqual([]);
    expect(container.textContent).toContain("Nobody selected yet");

    fireEvent.click(container.querySelectorAll(".audience-list .audience-row")[0]);
    expect(picker().selection()).toEqual(["alice.poweur.net"]);
    expect(onChange).toHaveBeenLastCalledWith(["alice.poweur.net"]);
    expect(container.querySelectorAll(".audience-list .audience-row")[0].getAttribute("aria-checked")).toBe("true");

    fireEvent.click(container.querySelectorAll(".audience-list .audience-row")[0]);
    expect(picker().selection()).toEqual([]);
  });

  it("expands a group into its members, and collapses it again", () => {
    const { container, picker } = setup({
      groups: [{ id: "team", name: "Team", members: ["alice.poweur.net", "bob.poweur.net"] }],
    });

    fireEvent.click(container.querySelector(".audience-groups .audience-row")!);
    // A group is a shortcut for its members, not a member of the selection.
    expect(picker().selection().sort()).toEqual(["alice.poweur.net", "bob.poweur.net"]);

    fireEvent.click(container.querySelector(".audience-groups .audience-row")!);
    expect(picker().selection()).toEqual([]);
  });

  it("removes a selection from its chip", () => {
    const { container, picker } = setup({ selected: ["alice.poweur.net"] });
    expect(picker().selection()).toEqual(["alice.poweur.net"]);
    fireEvent.click(container.querySelector(".audience-chip-x")!);
    expect(picker().selection()).toEqual([]);
  });

  it("de-duplicates case-insensitively so one person is picked once", () => {
    const { picker } = setup();
    act(() => {
      picker().add("Alice.Poweur.NET");
      picker().add("alice.poweur.net");
    });
    expect(picker().selection()).toEqual(["alice.poweur.net"]);
  });

  it("renders no stray text when there are no contacts and no groups", () => {
    const { container } = setup({ contacts: [], groups: [] });
    // `contacts.length && node` renders a literal 0; the picker once showed "000".
    expect(container.textContent).not.toMatch(/0/);
    expect(container.textContent).toContain("No contacts yet");
  });
});
