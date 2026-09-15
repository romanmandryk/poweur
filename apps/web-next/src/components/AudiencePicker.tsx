/**
 * AudiencePicker — "who is this for?" as a multi-select over contacts, local
 * groups and free-typed identities (from apps/web/js/components/audience-picker.js).
 *
 * The selection is a flat, insertion-ordered list of lowercase identities: a
 * group is a shortcut for its members, never a member itself.
 */
import { useEffect, useImperativeHandle, useLayoutEffect, useRef, useState, type Ref } from "react";
import { cn } from "../lib/cn";
import { handleOf } from "../lib/identity";
import { Avatar } from "../ui/Avatar";
import { SectionLabel } from "../ui/Display";
import { IdentityInput, type ContactOption, type IdentityInputHandle } from "./IdentityInput";
import type { ResolveIdentity } from "./ProfileCard";

export interface AudienceGroup {
  id: string;
  name?: string;
  members?: string[];
}

export interface AudiencePickerHandle {
  selection(): string[];
  add(identity: string): void;
  remove(identity: string): void;
  clear(): void;
}

const normalize = (identity: string) => String(identity || "").trim().toLowerCase();

function unique(list: string[]): string[] {
  return [...new Set(list.map(normalize).filter(Boolean))];
}

export function AudiencePicker({
  ref,
  resolve,
  contacts = [],
  groups = [],
  selected = [],
  onChange,
}: {
  ref?: Ref<AudiencePickerHandle>;
  resolve: ResolveIdentity;
  contacts?: ContactOption[];
  groups?: AudienceGroup[];
  selected?: string[];
  onChange?: (selection: string[]) => void;
}) {
  const [selection, setSelection] = useState<string[]>(() => unique(selected));
  const selectionRef = useRef(selection);
  const onChangeRef = useRef(onChange);
  const identityInput = useRef<IdentityInputHandle>(null);
  useLayoutEffect(() => {
    onChangeRef.current = onChange;
  });

  const commit = (next: string[]) => {
    selectionRef.current = next;
    setSelection(next);
    onChangeRef.current?.(next);
  };
  const has = (identity: string) => selectionRef.current.includes(normalize(identity));
  const add = (identity: string) => {
    const name = normalize(identity);
    if (!name) return;
    commit(has(name) ? [...selectionRef.current] : [...selectionRef.current, name]);
  };
  const remove = (identity: string) => commit(selectionRef.current.filter((id) => id !== normalize(identity)));
  const toggle = (identity: string) => (has(identity) ? remove(identity) : add(identity));

  useImperativeHandle(ref, () => ({
    selection: () => [...selectionRef.current],
    add,
    remove,
    clear: () => commit([]),
  }));

  // The legacy picker reported its initial selection once on construction.
  useEffect(() => {
    onChangeRef.current?.(selectionRef.current);
  }, []);

  return (
    <div className="audience-picker flex flex-col gap-2.5">
      <div role="list" className="audience-chips flex min-h-[34px] flex-wrap items-center gap-2">
        {selection.length === 0 ? (
          <p className="text-[13px] text-muted">Nobody selected yet.</p>
        ) : (
          selection.map((identity) => (
            <span
              key={identity}
              role="listitem"
              className="audience-chip inline-flex items-center gap-1.5 rounded-full bg-surface-3 py-[5px] pr-1.5 pl-[5px] text-[13px]"
            >
              <Avatar identity={identity} size="md" className="size-6 text-[11px]" />
              <span className="audience-chip-name" title={identity}>
                {handleOf(identity)}
              </span>
              <button
                type="button"
                aria-label={`Remove ${identity}`}
                className="audience-chip-x min-h-7 min-w-7 px-1 text-lg leading-none text-muted"
                onClick={() => remove(identity)}
              >
                ×
              </button>
            </span>
          ))
        )}
      </div>

      {groups.length > 0 && (
        <>
          <SectionLabel className="px-0">Groups</SectionLabel>
          <div className="audience-groups flex flex-col">
            {groups.map((group) => {
              const members = group.members ?? [];
              const all = members.length > 0 && members.every((member) => has(member));
              return (
                <AudienceRow
                  key={group.id}
                  picked={all}
                  icon={<span className="audience-group-icon w-9 text-center text-xl">👥</span>}
                  name={group.name || group.id}
                  detail={`${members.length} member${members.length === 1 ? "" : "s"}`}
                  onClick={() => {
                    const current = selectionRef.current;
                    const memberSet = new Set(members.map(normalize));
                    commit(all ? current.filter((id) => !memberSet.has(id)) : unique([...current, ...members]));
                  }}
                />
              );
            })}
          </div>
        </>
      )}

      {contacts.length > 0 ? (
        <>
          <SectionLabel className="px-0">Contacts</SectionLabel>
          <div className="audience-list flex flex-col">
            {contacts.map((contact) => (
              <AudienceRow
                key={contact.identity}
                role="checkbox"
                picked={has(contact.identity)}
                icon={<Avatar identity={contact.identity} size="md" />}
                name={contact.petname || handleOf(contact.identity)}
                detail={contact.identity}
                onClick={() => toggle(contact.identity)}
              />
            ))}
          </div>
        </>
      ) : (
        <p className="text-[13px] text-muted">No contacts yet — type an identity below.</p>
      )}

      <IdentityInput
        ref={identityInput}
        resolve={resolve}
        contacts={contacts}
        label="Add by identity"
        preview={false}
        onSubmit={(identity) => {
          add(identity);
          void identityInput.current?.setValue("");
        }}
      />
    </div>
  );
}

function AudienceRow({
  role,
  picked,
  icon,
  name,
  detail,
  onClick,
}: {
  role?: "checkbox";
  picked: boolean;
  icon: React.ReactNode;
  name: string;
  detail: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      role={role}
      aria-checked={role ? picked : undefined}
      onClick={onClick}
      className={cn(
        "audience-row flex min-h-13 w-full items-center gap-2.5 border-b border-sep px-1 py-2 text-left text-fg",
        picked && "picked bg-accent-soft",
      )}
    >
      {icon}
      <span className="audience-row-body flex min-w-0 flex-1 flex-col">
        <span className="audience-row-name text-[15px] font-semibold">{name}</span>
        <span className="audience-row-id truncate text-[13px] text-muted">{detail}</span>
      </span>
      <span className="audience-row-tick w-5 text-center font-bold text-accent">{picked ? "✓" : ""}</span>
    </button>
  );
}
