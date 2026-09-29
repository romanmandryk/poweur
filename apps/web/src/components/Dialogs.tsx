/**
 * Small questions on the panel instead of `prompt()` / `confirm()`: those
 * block the page, cannot be styled or read by assistive tech in context, and
 * behave inconsistently inside the Capacitor webview.
 */
import { useRef } from "react";
import { openPanel } from "../state/ui";
import { Button } from "../ui/Button";
import { FormGroup, Input, Label } from "../ui/Field";

/** Ask for one line of text. Resolves the trimmed answer, or null if dismissed or empty. */
export function askText({
  title,
  label,
  initial = "",
  confirmLabel = "OK",
  message = "",
}: {
  title: string;
  label: string;
  initial?: string;
  confirmLabel?: string;
  /** Shown above the field: what the answer will do. */
  message?: string;
}): Promise<string | null> {
  return new Promise((resolve) => {
    let answer: string | null = null;
    openPanel(
      title,
      (close) => (
        <TextForm
          label={label}
          initial={initial}
          confirmLabel={confirmLabel}
          message={message}
          onSubmit={(value) => {
            answer = value.trim() || null;
            close();
          }}
          onCancel={close}
        />
      ),
      () => resolve(answer),
    );
  });
}

function TextForm({
  label,
  initial,
  confirmLabel,
  message,
  onSubmit,
  onCancel,
}: {
  label: string;
  initial: string;
  confirmLabel: string;
  message: string;
  onSubmit: (value: string) => void;
  onCancel: () => void;
}) {
  const field = useRef<HTMLInputElement>(null);
  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        onSubmit(field.current?.value ?? "");
      }}
    >
      {message && <p className="dialog-message mb-4 text-sm text-muted">{message}</p>}
      <FormGroup>
        <Label htmlFor="dialog-text">{label}</Label>
        <Input ref={field} id="dialog-text" type="text" defaultValue={initial} autoComplete="off" onFocus={(event) => event.currentTarget.select()} />
      </FormGroup>
      <div className="panel-actions flex flex-wrap gap-2">
        <Button id="dialog-cancel" variant="secondary" className="min-h-11 flex-[1_1_40%]" onClick={onCancel}>
          Cancel
        </Button>
        <Button id="dialog-ok" type="submit" className="min-h-11 flex-[1_1_40%] p-3 text-[15px]">
          {confirmLabel}
        </Button>
      </div>
    </form>
  );
}

/** Ask to confirm something irreversible. Resolves true only on the confirm button. */
export function askConfirm({
  title,
  message,
  confirmLabel,
  confirmId = "dialog-confirm",
  danger = true,
}: {
  title: string;
  message: React.ReactNode;
  confirmLabel: string;
  confirmId?: string;
  danger?: boolean;
}): Promise<boolean> {
  return new Promise((resolve) => {
    let confirmed = false;
    openPanel(
      title,
      (close) => (
        <div>
          <div className="mb-4 text-[15px] text-fg">{message}</div>
          <div className="panel-actions flex flex-wrap gap-2">
            <Button id="dialog-cancel" variant="secondary" className="min-h-11 flex-[1_1_40%]" onClick={close}>
              Cancel
            </Button>
            <Button
              id={confirmId}
              variant={danger ? "danger" : "primary"}
              className="min-h-11 flex-[1_1_40%] p-3 text-[15px]"
              onClick={() => {
                confirmed = true;
                close();
              }}
            >
              {confirmLabel}
            </Button>
          </div>
        </div>
      ),
      () => resolve(confirmed),
    );
  });
}
