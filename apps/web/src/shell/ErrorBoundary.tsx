import { Component, type ErrorInfo, type ReactNode } from "react";
import { getObservability } from "../lib/observability";
import { Button } from "../ui/Button";

/**
 * React render errors never become window.onerror. Report them through the
 * same observability facade as unhandled exceptions, then offer a reload.
 */
export class ErrorBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    getObservability().captureError(error, { source: "react", component: info.componentStack ? "yes" : "no" });
  }

  render() {
    if (!this.state.failed) return this.props.children;
    return (
      <div className="flex h-dvh flex-col items-center justify-center gap-4 p-8 text-center">
        <h1 className="text-[22px] font-extrabold">Something went wrong</h1>
        <p className="max-w-[260px] text-base text-muted">Reload the app. If this keeps happening, the relay logs may have more detail.</p>
        <Button id="btn-error-reload" className="w-auto px-10" onClick={() => window.location.reload()}>
          Reload
        </Button>
      </div>
    );
  }
}
