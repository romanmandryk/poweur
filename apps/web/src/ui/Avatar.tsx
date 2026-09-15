import { useState } from "react";
import { cn } from "../lib/cn";
import { avatarColor, initialOf } from "../lib/identity";
import { useAvatarSrc } from "../state/avatars";

const SIZES = {
  sm: "size-7 text-[13px]",
  md: "size-9 text-[15px]",
  lg: "size-14 text-[22px]",
  xl: "size-18 text-[28px]",
} as const;

export type AvatarSize = keyof typeof SIZES;

/**
 * Initials on the identity's colour; the photo covers them once it loads.
 * Without `src` the circle shows whatever the avatar store knows — our own
 * photo, or a resolved profile's — so every place that draws someone gets it.
 * `src={null}` forces initials (a removed photo in the editor).
 */
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
  const stored = useAvatarSrc(identity);
  const image = src === undefined ? stored : src;
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
      {image && (
        // A broken or slow photo leaves the initials showing.
        <img
          src={image}
          alt=""
          loading="lazy"
          decoding="async"
          onLoad={() => setLoadedSrc(image)}
          className={cn("id-avatar-img absolute inset-0 size-full object-cover", loadedSrc !== image && "invisible")}
        />
      )}
    </div>
  );
}
