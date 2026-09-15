import { useState } from "react";
import { cn } from "../lib/cn";
import { avatarColor, initialOf } from "../lib/identity";

const SIZES = {
  sm: "size-7 text-[13px]",
  md: "size-9 text-[15px]",
  lg: "size-14 text-[22px]",
  xl: "size-18 text-[28px]",
} as const;

export type AvatarSize = keyof typeof SIZES;

/** Initials on the identity's colour; the resolved image covers them once it loads. */
export function Avatar({
  identity,
  size = "sm",
  src,
  className,
}: {
  identity: string;
  size?: AvatarSize;
  src?: string | null;
  className?: string;
}) {
  const [loadedSrc, setLoadedSrc] = useState<string | null>(null);
  return (
    <div
      aria-hidden="true"
      className={cn(
        "id-avatar relative flex shrink-0 items-center justify-center overflow-hidden rounded-full font-bold text-white",
        SIZES[size],
        className,
      )}
      style={{ background: avatarColor(identity) }}
    >
      {initialOf(identity)}
      {src && (
        // A broken or slow avatar leaves the initials showing.
        <img
          src={src}
          alt=""
          loading="lazy"
          onLoad={() => setLoadedSrc(src)}
          className={cn("id-avatar-img absolute inset-0 size-full object-cover", loadedSrc !== src && "invisible")}
        />
      )}
    </div>
  );
}
