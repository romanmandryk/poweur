import { useId, useState, type ReactNode } from "react";
import { Info } from "lucide-react";

/**
 * A small ⓘ that reveals a sentence or two. Tap or click toggles it (there is
 * no hover on a phone); the text is also the button's `title`, so a mouse
 * user gets it on hover too.
 */
export function InfoTip({ label, children }: { label: string; children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const id = useId();
  return (
    <span className="info-tip inline-flex flex-col align-middle">
      <button
        type="button"
        className="inline-flex items-center gap-1 text-[13px] text-muted underline decoration-dotted underline-offset-2"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => setOpen((value) => !value)}
      >
        <Info className="size-[14px]" strokeWidth={1.8} aria-hidden="true" />
        {label}
      </button>
      {open && (
        <span id={id} role="note" className="info-tip-body mt-1 block text-[13px] text-muted">
          {children}
        </span>
      )}
    </span>
  );
}
