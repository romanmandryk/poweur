/** Everything you can do to one contact, off the row's overflow (from app.js). */
import { useRef } from "react";
import { fingerprintOrKey } from "@poweur/client";
import { acceptContact, blockContact, removeContact, setPetname } from "../../actions/contacts";
import { openThread } from "../../actions/messages";
import { handleOf } from "../../lib/identity";
import { contactFor } from "../../state/badges";
import { useData } from "../../state/data";
import { openPanel } from "../../state/ui";
import { Button } from "../../ui/Button";
import { Chip, KvRow } from "../../ui/Display";
import { FormGroup, Input, Label } from "../../ui/Field";
import { CONTACT_STATE_CHIP } from "./Contacts";

export function openContactPanel(identity: string) {
  const contact = contactFor(useData.getState().contacts.list, identity);
  if (!contact) return;
  openPanel(contact.petname || handleOf(identity), (close) => <ContactPanel identity={identity} close={close} />);
}

const ACTION = "min-h-11 flex-[1_1_40%] px-4 py-3 text-[15px]";

function ContactPanel({ identity, close }: { identity: string; close: () => void }) {
  const contact = useData((state) => contactFor(state.contacts.list, identity));
  const petname = useRef<HTMLInputElement>(null);
  if (!contact) return <p className="text-muted">{identity} is no longer in your contacts.</p>;
  const chip = CONTACT_STATE_CHIP[contact.state ?? "accepted"] ?? CONTACT_STATE_CHIP.accepted;
  const pinned = (contact.pinnedKey as string | null | undefined) ?? null;

  const then = (action: () => void) => () => {
    close();
    action();
  };

  return (
    <div>
      <KvRow label="Identity" mono>
        {identity}
      </KvRow>
      <KvRow label="State">
        <Chip tone={chip.tone}>{chip.label}</Chip>
      </KvRow>
      <KvRow label="Safety number" mono>
        {pinned ? fingerprintOrKey(pinned) : "not pinned"}
      </KvRow>
      <KvRow label="Pinned key" mono>
        {pinned ?? "not pinned"}
      </KvRow>

      <FormGroup className="mt-4">
        <Label htmlFor="cp-petname">Petname</Label>
        <Input ref={petname} id="cp-petname" type="text" defaultValue={contact.petname ?? ""} placeholder="What you call them" autoComplete="off" />
      </FormGroup>
      <Button id="cp-save-petname" onClick={then(() => void setPetname(identity, petname.current?.value.trim() ?? ""))}>
        Save petname
      </Button>

      <div className="panel-actions mt-4 flex flex-wrap gap-2">
        <Button id="cp-message" variant="secondary" className={ACTION} onClick={then(() => openThread(identity))}>
          Message
        </Button>
        {contact.state === "blocked" ? (
          <Button id="cp-unblock" variant="secondary" className={ACTION} onClick={then(() => void acceptContact(identity, { silent: true }))}>
            Unblock
          </Button>
        ) : (
          <Button id="cp-block" variant="secondary" className={ACTION} onClick={then(() => void blockContact(identity))}>
            Block
          </Button>
        )}
        <Button id="cp-remove" variant="danger" className={ACTION} onClick={then(() => void removeContact(identity))}>
          Remove
        </Button>
      </div>
    </div>
  );
}
