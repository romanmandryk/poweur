import { useRef, type ReactNode } from "react";
import { Dialog } from "radix-ui";
import { X } from "lucide-react";
import { IconButton } from "./Button";

/**
 * The panel: a bottom sheet on a phone, a centred dialog from 768px. Radix
 * owns the focus trap, Escape and focus return that `showPanel()` did by hand;
 * the ids (`#panel-root`, `#panel-title`, `#panel-close-btn`) are the ones the
 * Playwright suite already addresses.
 */
export function Sheet({
  open,
  onOpenChange,
  title,
  returnFocus,
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  /** The control to focus again when the sheet closes (it opened from code, not a trigger). */
  returnFocus?: HTMLElement | null;
  children?: ReactNode;
}) {
  const contentRef = useRef<HTMLDivElement>(null);
  // By the time Radix asks where to put focus, the panel prop is already gone.
  const returnRef = useRef<HTMLElement | null>(null);
  if (open) returnRef.current = returnFocus ?? null;
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="panel-backdrop fixed inset-0 z-300 animate-fade-in bg-black/50 backdrop-blur-xs" />
        <Dialog.Content
          ref={contentRef}
          id="panel-root"
          // The title keeps the legacy `#panel-title` id, so Radix's generated
          // label reference would point at nothing and the dialog would have
          // no accessible name.
          aria-labelledby="panel-title"
          aria-describedby={undefined}
          onOpenAutoFocus={(event) => {
            // The first thing worth typing into, else the primary action, else
            // close — never nothing.
            event.preventDefault();
            const root = contentRef.current;
            const target =
              root?.querySelector<HTMLElement>("input:not([type=file]), textarea, .btn-primary") ??
              root?.querySelector<HTMLElement>("#panel-close-btn");
            target?.focus();
          }}
          onCloseAutoFocus={(event) => {
            // Back where it came from — or nowhere in particular, rather than
            // parked on a control inside a panel that is gone.
            const target = returnRef.current;
            returnRef.current = null;
            if (target?.isConnected) {
              event.preventDefault();
              target.focus();
            }
          }}
          className={
            "panel-root fixed inset-x-0 bottom-0 z-400 mx-auto max-h-[85dvh] max-w-[480px] animate-slide-up overflow-y-auto " +
            "rounded-t-[20px] bg-surface pb-safe outline-none " +
            "md:inset-auto md:top-1/2 md:left-1/2 md:max-h-[min(80dvh,760px)] md:w-[min(520px,calc(100vw-64px))] md:max-w-none " +
            "md:-translate-x-1/2 md:-translate-y-1/2 md:animate-pop-in md:rounded-card md:pb-0 md:shadow-pop"
          }
        >
          <div className="panel-handle mx-auto mt-2.5 h-1 w-9 rounded-xs bg-sep md:hidden" />
          <div className="panel-header flex items-center justify-between border-b border-sep px-5 pt-4 pb-2.5">
            <Dialog.Title id="panel-title" className="panel-title text-[17px] font-bold">
              {title}
            </Dialog.Title>
            <Dialog.Close asChild>
              <IconButton id="panel-close-btn" aria-label="Close">
                <X className="size-4" aria-hidden="true" />
              </IconButton>
            </Dialog.Close>
          </div>
          <div className="panel-body p-5">{children}</div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
