/**
 * A QR code drawn as one SVG path — no canvas, no image, crisp at any size.
 * Dark modules on a white plate in both themes: cameras read dark-on-light.
 */
import { useMemo } from "react";
import { encode } from "uqr";
import { cn } from "../lib/cn";

export function QRCode({ value, label, className }: { value: string; label: string; className?: string }) {
  const { size, path } = useMemo(() => {
    const qr = encode(value, { ecc: "M", border: 2 });
    let d = "";
    qr.data.forEach((row, y) =>
      row.forEach((dark, x) => {
        if (dark) d += `M${x} ${y}h1v1h-1z`;
      }),
    );
    return { size: qr.size, path: d };
  }, [value]);
  return (
    <svg
      viewBox={`0 0 ${size} ${size}`}
      role="img"
      aria-label={label}
      shapeRendering="crispEdges"
      className={cn("block aspect-square rounded-[12px] bg-white", className)}
      data-qr={value}
    >
      <path d={path} fill="#000" />
    </svg>
  );
}
