/**
 * ProfileCard — who an identity is, with the right action for the context
 * (from apps/web/js/components/profile-card.js).
 *
 * Renders three states in order — loading, ready, error — because a card that
 * blocks its container until DNS answers is worse than one that fills in.
 * Free of app-shell imports: EPIC-012 embeds it in a public contact form.
 */
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { cn } from "../lib/cn";
import { domainOf, handleOf } from "../lib/identity";
import { Avatar } from "../ui/Avatar";
import { Button } from "../ui/Button";
import { Chip } from "../ui/Display";

export interface ProfileEntry {
  identity?: string;
  displayName?: string | null;
  bio?: string | null;
  avatar?: string | null;
  links?: { label?: string; url?: string }[];
  capabilities?: { features?: Record<string, unknown> } | null;
  [key: string]: unknown;
}

export type ResolveIdentity = (identity: string) => Promise<ProfileEntry>;

export interface ProfileAction {
  label: string;
  onSelect: (identity: string, entry: ProfileEntry | null) => void;
  /** Default true: the action is disabled when the identity did not resolve. */
  requiresResolution?: boolean;
}

type Status = "loading" | "ready" | "error";

export function ProfileCard({
  identity,
  resolve,
  cached = null,
  action = null,
  compact = false,
  className,
}: {
  identity: string;
  resolve: ResolveIdentity;
  cached?: ProfileEntry | null;
  action?: ProfileAction | null;
  compact?: boolean;
  className?: string;
}) {
  const [resolved, setResolved] = useState<{ identity: string; status: Status; entry: ProfileEntry | null } | null>(null);
  const resolveRef = useRef(resolve);
  useLayoutEffect(() => {
    resolveRef.current = resolve;
  });

  useEffect(() => {
    if (cached) return;
    let live = true;
    resolveRef.current(identity).then(
      (entry) => live && setResolved({ identity, status: "ready", entry }),
      () => live && setResolved({ identity, status: "error", entry: null }),
    );
    return () => {
      live = false;
    };
  }, [identity, cached]);

  const current = cached
    ? { status: "ready" as Status, entry: cached }
    : resolved?.identity === identity
      ? resolved
      : { status: "loading" as Status, entry: null };
  const { status, entry } = current;

  const name = entry?.displayName || handleOf(identity);
  const features = Object.keys(entry?.capabilities?.features ?? {});
  // Never render javascript: (or anything but http/https) from a remote profile.
  const links = (entry?.links ?? []).slice(0, 4).filter((link) => /^https?:\/\//i.test(link.url ?? ""));

  return (
    <div
      data-identity={identity}
      data-state={status}
      className={cn(
        "profile-card flex min-h-15 items-center gap-3 bg-surface px-4 py-3",
        compact ? "compact border-b border-sep last:border-b-0" : "items-start rounded-card shadow-card",
        className,
      )}
    >
      <Avatar identity={identity} size={compact ? "md" : "lg"} src={entry?.avatar ?? null} />
      <div className="profile-card-body flex min-w-0 flex-1 flex-col gap-0.5">
        <div className="profile-card-name text-base font-semibold">{name}</div>
        <div className="profile-card-id truncate text-xs text-muted" title={identity}>
          {identity}
        </div>
        {status === "loading" && <div className="profile-card-meta text-[13px] text-muted">Resolving…</div>}
        {status === "error" && (
          <div className="profile-card-meta val-warn text-[13px] text-warning">Could not resolve this identity</div>
        )}
        {status === "ready" && !compact && (
          <>
            {entry?.bio && <div className="profile-card-bio mt-1 text-sm text-muted">{entry.bio}</div>}
            {features.length > 0 && (
              <div className="profile-card-caps mt-1.5 flex flex-wrap gap-1.5">
                {features.map((feature) => (
                  <Chip key={feature}>{feature}</Chip>
                ))}
              </div>
            )}
            {links.map((link) => (
              <a
                key={link.url}
                className="profile-card-link mt-1 block text-[13px]"
                href={link.url}
                target="_blank"
                rel="noopener noreferrer nofollow"
              >
                {link.label || link.url}
              </a>
            ))}
          </>
        )}
      </div>
      {status !== "loading" && !compact && (
        <div className="profile-card-domain truncate text-xs text-muted">{domainOf(identity)}</div>
      )}
      {action && (
        <Button
          size="sm"
          className="profile-card-action min-h-11 shrink-0"
          disabled={status === "error" && action.requiresResolution !== false}
          onClick={(event) => {
            event.stopPropagation();
            action.onSelect(identity, entry);
          }}
        >
          {action.label}
        </Button>
      )}
    </div>
  );
}
