import { describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import { render } from "@testing-library/react";
import { ErrorBoundary } from "../../src/shell/ErrorBoundary";
import { getObservability } from "../../src/lib/observability";

function Boom(): ReactNode {
  throw new Error("render exploded");
}

describe("ErrorBoundary", () => {
  it("shows a reload fallback and reports the render error", () => {
    const spy = vi.spyOn(getObservability(), "captureError");
    const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    const { container } = render(
      <ErrorBoundary>
        <Boom />
      </ErrorBoundary>,
    );
    expect(container.querySelector("#btn-error-reload")).toBeTruthy();
    expect(spy).toHaveBeenCalled();
    spy.mockRestore();
    errorSpy.mockRestore();
  });
});
