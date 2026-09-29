import { Files as FilesIcon, MessageCircle, Rocket, Settings, Users, type LucideIcon } from "lucide-react";
import { useShallow } from "zustand/react/shallow";
import { cn } from "../lib/cn";
import { navBadges } from "../state/badges";
import { useData } from "../state/data";
import { useRoute, type Destination } from "../state/route";
import { useSession } from "../state/session";

const NAV: { page: Destination; label: string; Icon: LucideIcon }[] = [
  { page: "messages", label: "Messages", Icon: MessageCircle },
  { page: "contacts", label: "Contacts", Icon: Users },
  { page: "files", label: "Files", Icon: FilesIcon },
  { page: "launcher", label: "Apps", Icon: Rocket },
  { page: "settings", label: "Settings", Icon: Settings },
];

/**
 * A bottom bar on a phone, an icon rail from 768px, a labelled sidebar from
 * 1024px (E15-T11). Five tabs fit 375px and every one clears 44px.
 */
export function BottomNav({ className }: { className?: string }) {
  const page = useRoute((state) => state.page);
  const go = useRoute((state) => state.go);
  const identity = useSession((state) => state.identity);
  const unlocked = useSession((state) => state.unlocked);
  const badgeData = useData(
    useShallow((state) => ({
      messages: state.messages,
      anon: state.anon,
      history: state.history,
      requests: state.requests,
      contacts: state.contacts,
      policy: state.policy,
    })),
  );
  const badges = navBadges(badgeData, identity, unlocked);

  return (
    <nav
      role="tablist"
      aria-label="Primary"
      className={cn(
        "bottom-nav relative z-90 flex h-nav shrink-0 border-t border-sep bg-chrome pb-safe backdrop-blur-xl backdrop-saturate-180",
        "md:h-full md:flex-col md:justify-start md:gap-1 md:border-t-0 md:border-r md:px-2 md:pt-4 md:pb-[calc(16px+env(safe-area-inset-bottom,0px))]",
        "lg:px-3 lg:pt-5",
        className,
      )}
    >
      {NAV.map(({ page: destination, label, Icon }) => {
        const active = destination === page;
        const count = badges[destination] ?? 0;
        return (
          <button
            key={destination}
            type="button"
            role="tab"
            data-page={destination}
            aria-selected={active}
            aria-label={`${label}${count ? `, ${count} new` : ""}`}
            onClick={() => {
              // Contacts live on DAV and change on other devices; opening the
              // destination is the moment to look again.
              if (destination === "contacts") {
                useData.setState((state) => ({ contacts: { ...state.contacts, loaded: false } }));
              }
              go(destination);
            }}
            className={cn(
              "nav-tab relative flex min-h-13 min-w-11 flex-1 flex-col items-center justify-center gap-[3px] px-1 py-2",
              "text-[10px] font-medium tracking-[.02em] text-faint transition-colors focus-visible:-outline-offset-2 max-[380px]:text-[9px]",
              "md:flex-none md:rounded-control md:px-2 md:py-3",
              "lg:flex-row lg:justify-start lg:gap-3 lg:px-3.5 lg:text-[15px] lg:font-[550]",
              "[@media(hover:hover)]:hover:bg-surface-2",
              active && "active text-accent md:bg-accent-soft",
            )}
          >
            <span className="nav-icon relative">
              <Icon className="size-6 max-[380px]:size-[22px] lg:size-[22px]" strokeWidth={active ? 2.4 : 1.8} aria-hidden="true" />
              {count > 0 && (
                <span
                  aria-hidden="true"
                  className="nav-badge absolute -top-1 -right-2.5 flex h-4 min-w-4 items-center justify-center rounded-full bg-danger px-1 text-[10px] leading-none font-bold text-white"
                >
                  {count > 99 ? "99+" : count}
                </span>
              )}
            </span>
            <span className="max-w-full truncate">{label}</span>
          </button>
        );
      })}
    </nav>
  );
}
