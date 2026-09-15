import { createRef } from "react";
import { describe, expect, it } from "vitest";
import { fireEvent, render, waitFor } from "@testing-library/react";
import { PolicyControls, type PolicyControlsHandle } from "../../src/components/PolicyControls";
import { describePowBits, INBOX_MODES, POW_UNCOMFORTABLE_BITS, powCost, type InboxPolicy } from "../../src/lib/policy";

const save = () => Promise.resolve();

function setup(policy: InboxPolicy, onSave: (policy: InboxPolicy) => Promise<void> = save) {
  const ref = createRef<PolicyControlsHandle>();
  const utils = render(<PolicyControls ref={ref} policy={policy} onSave={onSave} />);
  const $ = <T extends Element = HTMLElement>(selector: string) => utils.container.querySelector<T>(selector)!;
  return { ...utils, $, controls: () => ref.current! };
}

describe("PolicyControls", () => {
  it("offers every mode the relay implements, and marks the current one", () => {
    const { container, $ } = setup({ mode: "contacts_only" });
    expect(container.querySelectorAll(".policy-mode")).toHaveLength(INBOX_MODES.length);
    const selected = $(".policy-mode.selected");
    expect(selected.dataset.mode).toBe("contacts_only");
    expect(selected.getAttribute("aria-checked")).toBe("true");
  });

  it("writes no anonymous block at all when anonymous is off", () => {
    const { controls } = setup({ mode: "open" });
    // An absent block *is* deny.
    expect(controls().value()).toEqual({
      version: 1, mode: "open", read_receipts: { enabled: true, disabled_for: [] },
    });
  });

  it("keeps the difficulty dial out of the document unless the challenge is pow", () => {
    const { $, controls } = setup({ mode: "open", anonymous: { allow: true, challenge: "none", pow_bits: 22 } });
    expect(controls().value().anonymous).toEqual({ allow: true, challenge: "none", max_bytes: 4096, max_per_day: 20 });

    fireEvent.click($('[data-challenge="pow"]'));
    expect(controls().value().anonymous?.pow_bits).toBe(22);
  });

  it("shows the two designed-but-unenforced challenges as disabled", () => {
    const { $ } = setup({ mode: "open", anonymous: { allow: true } });
    expect($<HTMLButtonElement>('[data-challenge="verified"]').disabled).toBe(true);
    expect($<HTMLButtonElement>('[data-challenge="payment"]').disabled).toBe(true);
    expect($<HTMLButtonElement>('[data-challenge="pow"]').disabled).toBe(false);
  });

  it("describes the difficulty in time, and warns once a phone would suffer", () => {
    const { $, controls } = setup({ mode: "open", anonymous: { allow: true, challenge: "pow", pow_bits: 16 } });
    expect($("#policy-bits-readout").textContent).toContain("16 bits");
    expect($("#policy-bits-warning").hidden).toBe(true);

    fireEvent.change($("#policy-pow-bits"), { target: { value: "24" } });
    expect($("#policy-bits-readout").textContent).toContain("24 bits");
    expect($("#policy-bits-warning").hidden).toBe(false);
    expect(controls().value().anonymous?.pow_bits).toBe(24);
  });

  it("toggling anonymous on reveals the cost controls", () => {
    const { $, container, controls } = setup({ mode: "open" });
    expect(container.querySelector(".policy-anon-body")).toBeNull();
    fireEvent.click($("#policy-anon-allow"));
    expect(container.querySelector(".policy-anon-body")).toBeTruthy();
    expect(controls().value().anonymous?.allow).toBe(true);
  });

  it("quotes browser cost, which is what the sender actually pays", () => {
    expect(powCost(16).phone).toBe("~1 s");
    expect(powCost(21).bits).toBe(20);
    expect(describePowBits(20)).toMatch(/laptop.*phone/);
    expect(POW_UNCOMFORTABLE_BITS).toBe(20);
  });

  it("hands the caller one document and reports a failed save", async () => {
    const saves: InboxPolicy[] = [];
    const { $ } = setup({ mode: "open" }, (document) => {
      saves.push(document);
      return Promise.reject(new Error("relay said no"));
    });
    fireEvent.click($('[data-mode="contacts_and_requests"]'));
    fireEvent.click($("#policy-save"));

    await waitFor(() => expect($(".idin-status").textContent).toBe("relay said no"));
    expect(saves).toEqual([{ version: 1, mode: "contacts_and_requests", read_receipts: { enabled: true, disabled_for: [] } }]);
  });

  it("normalizes per-contact read-receipt opt-outs", () => {
    const { $, controls } = setup({ mode: "open" });
    fireEvent.change($("#policy-read-disabled-for"), { target: { value: " Alice.Example, bob.example " } });
    fireEvent.click($("#policy-read-receipts"));
    expect(controls().value().read_receipts).toEqual({ enabled: false, disabled_for: ["alice.example", "bob.example"] });
  });
});
