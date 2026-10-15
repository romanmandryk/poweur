import { beforeEach, describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { Header } from "../../src/shell/Header";
import { useSession } from "../../src/state/session";
import { resetStores } from "../helpers/stores";

describe("header identity pill", () => {
  beforeEach(() => resetStores());

  it("on an identity host shows its one ID without switching or adding", () => {
    useSession.setState({ identity: "johnjohn.poweur.net", mode: { mode: "identity", subject: "johnjohn.poweur.net", domain: "poweur.net", hostedDomains: ["poweur.net"] } as any });
    const { container } = render(<Header />);
    const pill = container.querySelector("#id-pill")!;
    expect(pill.textContent).toContain("johnjohn");
    expect(pill.tagName).toBe("DIV");
    expect(container.querySelector(".id-pill-chevron")).toBeNull();
  });

  it("on the launcher keeps the switcher", () => {
    useSession.setState({ identity: "johnjohn.poweur.net", mode: { mode: "launcher", domain: "poweur.net", hostedDomains: ["poweur.net"] } as any });
    const { container } = render(<Header />);
    expect(container.querySelector("#id-pill")!.tagName).toBe("BUTTON");
    expect(container.querySelector(".id-pill-chevron")).toBeTruthy();
  });
});
