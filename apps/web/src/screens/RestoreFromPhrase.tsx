/**
 * Restore an identity from its 24-word recovery kit: for the day every device
 * and passkey is gone. The kit rebuilds the keys; a new passkey (or the
 * device keystore) then protects them on this device.
 */
import { useRef } from "react";
import { restoreFromRecoveryPhrase } from "../actions/identity";
import { closePanel, openPanel } from "../state/ui";
import { Button } from "../ui/Button";
import { FormGroup, Input, Label, Note, Textarea } from "../ui/Field";

export function openRestoreFromPhrasePanel(knownIdentity = "") {
  openPanel("Restore from recovery kit", () => <RestoreFromPhrase knownIdentity={knownIdentity} />);
}

function RestoreFromPhrase({ knownIdentity }: { knownIdentity: string }) {
  const identity = useRef<HTMLInputElement>(null);
  const words = useRef<HTMLTextAreaElement>(null);

  const restore = async () => {
    const ok = await restoreFromRecoveryPhrase(knownIdentity || identity.current?.value || "", words.current?.value ?? "");
    if (ok) closePanel();
  };

  return (
    <div>
      <p className="mb-3 text-[13px] text-muted">
        Lost every device? Type the 24 words you wrote down. This browser gets its own passkey; nothing leaves it.
      </p>
      {!knownIdentity && (
        <FormGroup>
          <Label htmlFor="restore-identity">Your identity</Label>
          <Input
            ref={identity}
            id="restore-identity"
            type="text"
            placeholder="alice.poweur.net"
            autoCapitalize="none"
            autoCorrect="off"
            autoComplete="off"
            spellCheck={false}
            inputMode="url"
          />
        </FormGroup>
      )}
      <FormGroup className="mt-4">
        <Label htmlFor="restore-words">Recovery kit — 24 words</Label>
        <Textarea
          ref={words}
          id="restore-words"
          rows={4}
          className="min-h-0"
          placeholder="word1 word2 …"
          autoCapitalize="none"
          autoCorrect="off"
          autoComplete="off"
          spellCheck={false}
        />
      </FormGroup>
      <Button id="btn-restore-phrase" className="mt-4" onClick={() => void restore()}>
        Restore{knownIdentity ? ` ${knownIdentity}` : ""}
      </Button>
      <Note className="mt-2">Anyone with these words is you. Only type them on a device you trust.</Note>
    </div>
  );
}
