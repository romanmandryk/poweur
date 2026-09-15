import type { ComponentProps, ReactNode } from "react";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { cn } from "../lib/cn";

/** The in-page title of a destination, distinct from the app header. */
export function DestHeader({ title, children, className }: { title: ReactNode; children?: ReactNode; className?: string }) {
  return (
    <div className={cn("dest-header flex items-center gap-2.5 px-4 pt-[18px] pb-2.5", className)}>
      <h1 className="dest-title min-w-0 flex-1 text-[28px] font-extrabold tracking-[-.02em]">{title}</h1>
      {children}
    </div>
  );
}

/**
 * A full-screen page with a back button: gates (unlock, add-id) own the whole
 * screen, details (thread, new chat) sit in the detail pane on wide screens.
 */
export function SubPage({
  title,
  onBack,
  backLabel = "Back",
  actions,
  footer,
  className,
  bodyClassName,
  children,
}: {
  title: ReactNode;
  onBack?: () => void;
  backLabel?: string;
  actions?: ReactNode;
  footer?: ReactNode;
  className?: string;
  bodyClassName?: string;
  children?: ReactNode;
}) {
  return (
    <div className={cn("sub-page flex h-dvh animate-fade-in flex-col bg-bg lg:[#detail-pane_&]:h-full lg:[#detail-pane_&]:bg-transparent", className)}>
      <div className="sub-header flex h-header shrink-0 items-center gap-3 border-b border-sep bg-chrome px-4 pt-safe backdrop-blur-xl lg:[#detail-pane_&]:sticky lg:[#detail-pane_&]:top-0 lg:[#detail-pane_&]:z-2">
        {onBack && (
          <button
            id="btn-back"
            type="button"
            onClick={onBack}
            className="btn-back -ml-1 flex items-center gap-1 rounded-lg p-1 text-[17px] text-accent active:opacity-60"
          >
            <ChevronLeft className="size-5" strokeWidth={2.4} aria-hidden="true" />
            {backLabel}
          </button>
        )}
        <div className="sub-title flex-1 truncate text-[17px] font-bold">{title}</div>
        {actions}
      </div>
      <div className={cn("sub-body flex-1 overflow-y-auto px-4 py-6 md:mx-auto md:w-full md:max-w-[560px]", bodyClassName)}>
        {children}
      </div>
      {footer && <div className="sub-footer shrink-0 border-t border-sep bg-bg px-5 pt-4 pb-[calc(16px+env(safe-area-inset-bottom,0px))]">{footer}</div>}
    </div>
  );
}

export function SettingsGroup({ label, children, className }: { label?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <div className={cn("settings-group mb-2", className)}>
      {label && <div className="settings-group-label px-5 pt-2 pb-1.5 text-[13px] font-semibold tracking-[.05em] text-muted uppercase">{label}</div>}
      <div className="settings-rows mx-4 overflow-hidden rounded-card bg-surface">{children}</div>
    </div>
  );
}

export function SettingsRow({
  icon,
  label,
  value,
  valueTone,
  arrow = true,
  className,
  onClick,
  ...props
}: Omit<ComponentProps<"button">, "value"> & {
  icon?: ReactNode;
  label: ReactNode;
  value?: ReactNode;
  valueTone?: "ok" | "warn";
  arrow?: boolean;
}) {
  const interactive = Boolean(onClick);
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={!interactive}
      className={cn(
        "settings-row flex min-h-13 w-full items-center gap-3 border-b border-sep px-4 py-3.5 text-left text-fg last:border-b-0",
        interactive
          ? "transition-colors active:bg-surface-2 [@media(hover:hover)]:hover:bg-surface-2"
          : "no-action cursor-default",
        "focus-visible:-outline-offset-2",
        className,
      )}
      {...props}
    >
      {icon && <span className="settings-row-icon w-7 shrink-0 text-center text-xl">{icon}</span>}
      <span className="settings-row-label flex-1 text-base">{label}</span>
      {value !== undefined && (
        <span
          className={cn(
            "settings-row-value max-w-35 truncate text-[15px] text-muted",
            valueTone === "ok" && "val-ok text-success",
            valueTone === "warn" && "val-warn text-warning",
          )}
        >
          {value}
        </span>
      )}
      {interactive && arrow && <ChevronRight className="settings-row-arrow size-4 shrink-0 text-faint" aria-hidden="true" />}
    </button>
  );
}
