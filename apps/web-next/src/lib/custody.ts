/** What holds an identity's keys, and how the UI talks about it (from app.js). */
import { KeyRound, ShieldCheck, type LucideIcon } from "lucide-react";

export type Custody = "native" | "prf";

export function custodyOf(record: { encryptedKeys?: { kdf?: string } } | null | undefined): Custody {
  return record?.encryptedKeys?.kdf === "native" ? "native" : "prf";
}

export const CUSTODY_COPY: Record<Custody, { action: string; note: string; chip: string; icon: LucideIcon }> = {
  native: {
    action: "Unlock",
    note: "Your device will ask for Face ID, Touch ID or your passcode",
    chip: "Device keystore",
    icon: ShieldCheck,
  },
  prf: {
    action: "Unlock with passkey",
    note: "Touch ID, Face ID, or Windows Hello",
    chip: "Passkey (PRF)",
    icon: KeyRound,
  },
};
