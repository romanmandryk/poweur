/**
 * Add a contact by typed ID (E15-T2). Resolved first, so a typo fails here
 * rather than later — and the key resolved now is the one pinned.
 */
import { useRef, useState } from "react";
import { requestContact } from "../../actions/contacts";
import { openThread } from "../../actions/messages";
import { resolveForActive } from "../../actions/relay";
import { IdentityInput, type IdentityInputHandle, type ResolvedIdentity } from "../../components/IdentityInput";
import { domainOf } from "../../lib/identity";
import { useData } from "../../state/data";
import { useSession } from "../../state/session";
import { openPanel, toast } from "../../state/ui";
import { Button } from "../../ui/Button";
import { FormGroup, Input, Label } from "../../ui/Field";

export function openAddContactPanel(preset = "") {
  openPanel("Add a contact", (close) => <AddContact preset={preset} close={close} />);
}

function AddContact({ preset, close }: { preset: string; close: () => void }) {
  const identity = useSession((state) => state.identity) ?? "";
  const contacts = useData((state) => state.contacts.list);
  const input = useRef<IdentityInputHandle>(null);
  const picked = useRef<ResolvedIdentity | null>(null);
  const intro = useRef<HTMLInputElement>(null);
  const petname = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);

  /**
   * Resolve on the way out rather than gating the button on it: pressing the
   * button someone is looking at forces the lookup it was waiting for.
   */
  const resolveNow = async (): Promise<ResolvedIdentity | null> => {
    if (picked.current) return picked.current;
    setBusy(true);
    try {
      return (await input.current?.lookup()) ?? null;
    } finally {
      setBusy(false);
    }
  };

  const sendRequest = async () => {
    const target = await resolveNow();
    if (!target) {
      toast("Enter a Poweur ID we can find", "warning");
      return;
    }
    const hello = intro.current?.value.trim() || undefined;
    const name = petname.current?.value.trim() || undefined;
    close();
    await requestContact(target.identity, { intro: hello, petname: name });
  };

  const justMessage = async () => {
    const target = await resolveNow();
    if (!target) {
      toast("Enter a Poweur ID we can find", "warning");
      return;
    }
    close();
    openThread(target.identity);
  };

  return (
    <div>
      <p className="mb-3 text-[13px] text-muted">
        Type a Poweur ID. We resolve it first, so a typo fails here rather than silently later — and the key we resolve now is the one we
        pin.
      </p>
      <div id="add-contact-input">
        <IdentityInput
          ref={input}
          resolve={resolveForActive}
          contacts={contacts}
          value={preset}
          // On a hosted relay everyone shares a domain: "alice" is what people type.
          defaultDomain={domainOf(identity)}
          label="Identity"
          onChange={(result) => {
            picked.current = result;
          }}
          onSubmit={() => void sendRequest()}
        />
      </div>
      <FormGroup className="mt-4">
        <Label htmlFor="ac-intro">Say hello (optional)</Label>
        <Input ref={intro} id="ac-intro" type="text" placeholder="contact request" autoComplete="off" />
      </FormGroup>
      <FormGroup>
        <Label htmlFor="ac-petname">Petname (optional)</Label>
        <Input ref={petname} id="ac-petname" type="text" placeholder="What you call them" autoComplete="off" />
      </FormGroup>
      <Button id="btn-add-contact-go" className="mt-4" disabled={busy} onClick={() => void sendRequest()}>
        Send request
      </Button>
      <Button id="btn-add-contact-msg" variant="ghost" className="mt-2" disabled={busy} onClick={() => void justMessage()}>
        Just message them
      </Button>
    </div>
  );
}
