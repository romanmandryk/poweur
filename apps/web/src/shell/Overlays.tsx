import { CircleCheck, CircleX, Info, TriangleAlert, type LucideIcon } from "lucide-react";
import { closePanel, useUi, type ToastType } from "../state/ui";
import { cn } from "../lib/cn";
import { Spinner } from "../ui/Display";
import { Sheet } from "../ui/Sheet";

const TOAST_ICON: Record<ToastType, { Icon: LucideIcon; className: string }> = {
  success: { Icon: CircleCheck, className: "text-success" },
  error: { Icon: CircleX, className: "text-danger" },
  warning: { Icon: TriangleAlert, className: "text-warning" },
  info: { Icon: Info, className: "text-accent" },
};

export function Toaster() {
  const toasts = useUi((state) => state.toasts);
  return (
    <div
      id="toast-root"
      aria-live="polite"
      className="bottom-above-nav pointer-events-none fixed left-1/2 z-1100 flex w-[90%] max-w-[380px] -translate-x-1/2 flex-col gap-2"
    >
      {toasts.map((item) => {
        const { Icon, className } = TOAST_ICON[item.type];
        return (
          <div
            key={item.id}
            data-toast-key={item.key}
            className={cn(
              "toast flex animate-toast-in items-center gap-2.5 rounded-xl bg-[rgb(50_50_50/.95)] px-4 py-[13px] text-sm font-medium text-white shadow-[0_4px_24px_rgb(0_0_0/.3)] backdrop-blur-xl dark:bg-[rgb(70_70_70/.95)]",
              item.type,
            )}
          >
            <Icon aria-hidden="true" className={cn("toast-icon size-[18px] shrink-0", className)} />
            <span>{item.message}</span>
          </div>
        );
      })}
    </div>
  );
}

export function LoadingOverlay() {
  const { active, text } = useUi((state) => state.loading);
  return (
    <div
      id="loading-root"
      role="status"
      hidden={!active}
      className="loading-root fixed inset-0 z-1000 flex animate-fade-in items-center justify-center bg-black/50 backdrop-blur-sm"
    >
      <div className="loading-inner flex flex-col items-center gap-3.5 text-center">
        <Spinner />
        <p id="loading-text" className="text-[15px] font-medium text-white">
          {text}
        </p>
      </div>
    </div>
  );
}

export function PanelHost() {
  const panel = useUi((state) => state.panel);
  return (
    <Sheet
      open={panel !== null}
      onOpenChange={(open) => {
        if (!open && panel) closePanel(panel.id);
      }}
      title={panel?.title ?? ""}
      returnFocus={panel?.returnFocus}
    >
      {panel?.render(() => closePanel(panel.id))}
    </Sheet>
  );
}
