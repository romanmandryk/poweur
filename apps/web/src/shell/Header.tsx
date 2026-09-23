import { DropdownMenu } from "radix-ui";
import { Check, ChevronDown, Plus } from "lucide-react";
import { cn } from "../lib/cn";
import { domainOf, handleOf } from "../lib/identity";
import { listIdentities } from "../lib/storage.js";
import { useRoute } from "../state/route";
import { switchIdentity, useSession } from "../state/session";
import { Avatar } from "../ui/Avatar";
import { Wordmark } from "../ui/Logo";
import { ActivityIndicator } from "./ActivityIndicator";
import { ThemeToggle } from "./ThemeToggle";

export function Header({ className }: { className?: string }) {
  const identity = useSession((state) => state.identity);
  const push = useRoute((state) => state.push);

  return (
    <header
      className={cn(
        "app-header sticky top-0 z-100 flex h-header shrink-0 items-center justify-between border-b border-sep bg-chrome px-4 pt-safe",
        "backdrop-blur-xl backdrop-saturate-180 md:relative",
        className,
      )}
    >
      <Wordmark />
      <div className="header-actions flex items-center gap-2">
        <ThemeToggle />
        {identity ? (
          <IdentitySwitcher identity={identity} onAdd={() => push("add-id")} />
        ) : (
          <button
            id="btn-add-id-header"
            type="button"
            onClick={() => push("add-id")}
            className="btn-add-id-pill flex items-center gap-1.5 rounded-full bg-accent px-3.5 py-[7px] text-sm font-semibold text-white active:scale-[.97] active:opacity-85"
          >
            <Plus className="size-4" aria-hidden="true" /> Add ID
          </button>
        )}
      </div>
      <ActivityIndicator />
    </header>
  );
}

function IdentitySwitcher({ identity, onAdd }: { identity: string; onAdd: () => void }) {
  const push = useRoute((state) => state.push);
  const identities: string[] = listIdentities();

  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <button
          id="id-pill"
          type="button"
          className="id-pill group flex items-center gap-[7px] rounded-full bg-surface-2 py-[5px] pr-3 pl-[5px] text-fg transition-[background-color,transform] active:scale-[.97] active:bg-surface-3"
        >
          <Avatar identity={identity} />
          <span className="id-pill-handle max-w-30 truncate text-sm font-semibold">{handleOf(identity)}</span>
          <ChevronDown
            className="id-pill-chevron size-3 shrink-0 text-faint transition-transform duration-200 group-data-[state=open]:rotate-180"
            aria-hidden="true"
          />
        </button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          id="id-dropdown"
          align="end"
          sideOffset={10}
          className="id-dropdown z-500 w-70 animate-pop-in overflow-hidden rounded-card border border-sep bg-surface shadow-pop"
        >
          {identities.map((id) => (
            <DropdownMenu.Item
              key={id}
              data-switch={id}
              onSelect={() => {
                if (id === identity) return;
                switchIdentity(id);
                push("unlock");
              }}
              className={cn(ROW, "border-b border-sep", id === identity && "active")}
            >
              <Avatar identity={id} size="md" />
              <div className="id-dr-info min-w-0 flex-1">
                <div className="id-dr-name truncate text-[15px] font-semibold">{handleOf(id)}</div>
                <div className="id-dr-domain truncate text-xs text-muted">{domainOf(id)}</div>
              </div>
              {id === identity && <Check className="id-dr-check size-4 shrink-0 text-accent" aria-hidden="true" />}
            </DropdownMenu.Item>
          ))}
          <DropdownMenu.Item id="dd-add-id" onSelect={onAdd} className={cn(ROW, "id-dr-add")}>
            <div className="flex size-9 shrink-0 items-center justify-center rounded-full bg-accent-soft text-accent">
              <Plus className="size-4" aria-hidden="true" />
            </div>
            <div className="id-dr-name text-[15px] font-semibold text-accent">Add identity</div>
          </DropdownMenu.Item>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

const ROW =
  "id-dr-row flex w-full cursor-pointer items-center gap-3 px-3.5 py-3 text-left text-fg outline-none " +
  "data-highlighted:bg-bg active:bg-surface-3";
