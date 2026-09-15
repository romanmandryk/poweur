/**
 * The Contacts destination (E15-T2): who you trust, what state each is in,
 * and the one tap that matters — message them — with everything else behind
 * the row's overflow so a row fits a 375px viewport.
 */
import { useEffect } from "react";
import { MoreHorizontal, Plus, Users } from "lucide-react";
import { loadContacts, loadRequests, refreshContactsScreen } from "../../actions/contacts";
import { loadHistory, openThread } from "../../actions/messages";
import { resolveForActive } from "../../actions/relay";
import { ProfileCard } from "../../components/ProfileCard";
import { onActivateKeys } from "../../lib/a11y";
import { useData, type Contact } from "../../state/data";
import { Button, IconButton } from "../../ui/Button";
import { Chip, EmptyState, type ChipTone } from "../../ui/Display";
import { Input } from "../../ui/Field";
import { DestHeader } from "../../ui/Layout";
import { PullToRefresh } from "../../ui/PullToRefresh";
import { usePeerAvatars } from "../../actions/avatars";
import { openAddContactPanel } from "./AddContactPanel";
import { openContactPanel } from "./ContactPanel";

export const CONTACT_STATE_CHIP: Record<string, { label: string; tone: ChipTone }> = {
  accepted: { label: "Contact", tone: "success" },
  requested: { label: "Requested", tone: "warning" },
  blocked: { label: "Blocked", tone: "danger" },
};

export function Contacts() {
  const contacts = useData((state) => state.contacts);

  // Contacts live on DAV and change on other devices: opening the destination
  // marks them stale (BottomNav), and stale means read again. Requests drain
  // too, so an accepted request of ours is promoted while we look.
  useEffect(() => {
    if (!contacts.loaded) void loadContacts();
    void loadRequests();
    void loadHistory();
  }, [contacts.loaded]);

  // Their photos, for this list and every picker that shows the same people.
  usePeerAvatars(contacts.list.map((contact) => contact.identity));
  const needle = contacts.filter.trim().toLowerCase();
  const filtered = contacts.list.filter((contact) => !needle || `${contact.identity} ${contact.petname ?? ""}`.toLowerCase().includes(needle));

  return (
    <PullToRefresh onRefresh={refreshContactsScreen}>
      <DestHeader title="Contacts">
        <Button id="btn-add-contact" size="sm" onClick={() => openAddContactPanel()}>
          <Plus className="size-4" aria-hidden="true" /> Add
        </Button>
      </DestHeader>

      {contacts.list.length > 0 && (
        <div className="dest-toolbar px-4 pb-3">
          <Input
            id="contacts-filter"
            type="search"
            placeholder="Search contacts"
            autoComplete="off"
            aria-label="Search contacts"
            value={contacts.filter}
            onChange={(event) => {
              const filter = event.currentTarget.value;
              useData.setState((state) => ({ contacts: { ...state.contacts, filter } }));
            }}
          />
        </div>
      )}

      {contacts.error && <p className="val-warn p-4 text-[13px] text-warning">{contacts.error}</p>}

      {contacts.list.length === 0 && !contacts.loading && Boolean(contacts.loaded || contacts.error) && (
        <EmptyState
          icon={Users}
          title="No contacts yet"
          body="Add someone by their Poweur ID and you can message them without either of you sharing a phone number."
          action={
            <Button id="btn-add-contact-empty" className="w-auto px-6" onClick={() => openAddContactPanel()}>
              Add a contact
            </Button>
          }
        />
      )}

      {filtered.length > 0 && (
        <div className="conv-list bg-surface">
          {filtered.map((contact) => (
            <ContactRow key={contact.identity} contact={contact} />
          ))}
        </div>
      )}

      {contacts.list.length > 0 && filtered.length === 0 && (
        <p className="p-4 text-[13px] text-muted">No contact matches “{contacts.filter}”.</p>
      )}
    </PullToRefresh>
  );
}

function ContactRow({ contact }: { contact: Contact }) {
  const chip = CONTACT_STATE_CHIP[contact.state ?? "accepted"] ?? CONTACT_STATE_CHIP.accepted;
  // Messaging a blocked contact is not the action they meant.
  const open = () => (contact.state === "blocked" ? openContactPanel(contact.identity) : openThread(contact.identity));

  return (
    <div
      role="button"
      tabIndex={0}
      data-contact-open={contact.identity}
      data-contact-state={contact.state}
      onClick={open}
      onKeyDown={onActivateKeys(open)}
      className="contact-row flex min-h-13 cursor-pointer items-center gap-2 border-b border-sep bg-surface pr-3 last:border-b-0 focus-visible:-outline-offset-2 [@media(hover:hover)]:hover:bg-surface-2"
    >
      <div className="min-w-0 flex-1">
        <ProfileCard
          identity={contact.identity}
          resolve={resolveForActive}
          compact
          cached={contact.petname ? { displayName: contact.petname, links: [] } : null}
          className="border-b-0 bg-transparent"
        />
      </div>
      <div className="contact-row-meta flex shrink-0 items-center gap-2">
        <Chip tone={chip.tone}>{chip.label}</Chip>
        <IconButton
          className="contact-more size-11 bg-transparent text-xl"
          data-contact-menu={contact.identity}
          aria-label={`More actions for ${contact.identity}`}
          onClick={(event) => {
            event.stopPropagation();
            openContactPanel(contact.identity);
          }}
        >
          <MoreHorizontal className="size-5" aria-hidden="true" />
        </IconButton>
      </div>
    </div>
  );
}
