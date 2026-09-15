/**
 * Inbox policy vocabulary, from apps/web/js/components/policy-controls.js.
 * Kept apart from the component so settings, onboarding and EPIC-012's public
 * pages can describe a policy without rendering the editor.
 */

export type InboxMode = "open" | "contacts_only" | "contacts_and_requests";
export type Challenge = "none" | "pow" | "verified" | "payment";

export interface InboxPolicy {
  version?: number;
  mode?: InboxMode | string;
  anonymous?: {
    allow?: boolean;
    challenge?: Challenge | string;
    pow_bits?: number;
    max_bytes?: number;
    max_per_day?: number;
  };
  read_receipts?: { enabled?: boolean; disabled_for?: string[] };
}

/** Plain language for each mode — the relay's rules, not a paraphrase. */
export const INBOX_MODES: { id: InboxMode; label: string; detail: string }[] = [
  {
    id: "open",
    label: "Anyone",
    detail: "Any Poweur ID can message you. Contact requests still wait in Requests. Simple, and the default for identities that never set a policy.",
  },
  {
    id: "contacts_only",
    label: "Contacts only",
    detail: "Only people you have accepted. Everyone else is refused — including contact requests, so nobody can ask.",
  },
  {
    id: "contacts_and_requests",
    label: "Contacts, and requests from others",
    detail: "Contacts message you normally; a stranger gets one contact request, which waits in your Requests tray. Recommended.",
  },
];

/**
 * What a difficulty costs *the sender's browser*, which is who pays it.
 *
 * Derived from the measured table in `apps/docs/docs/trust/anonymous-and-
 * challenges.md` with that document's 5–10× penalty for browser JS applied.
 */
const POW_COST = [
  { bits: 8, laptop: "instant", phone: "instant" },
  { bits: 12, laptop: "instant", phone: "instant" },
  { bits: 16, laptop: "~0.3 s", phone: "~1 s" },
  { bits: 18, laptop: "~1 s", phone: "~3 s" },
  { bits: 20, laptop: "~4 s", phone: "~10 s" },
  { bits: 22, laptop: "~15 s", phone: "~40 s" },
  { bits: 24, laptop: "~1 min", phone: "~3 min" },
  { bits: 26, laptop: "~4 min", phone: "~10 min" },
  { bits: 28, laptop: "~15 min", phone: "~40 min" },
  { bits: 30, laptop: "~1 hour", phone: "hours" },
];

/** The nearest measured row at or below `bits`. */
export function powCost(bits: number) {
  let match = POW_COST[0];
  for (const row of POW_COST) if (row.bits <= bits) match = row;
  return match;
}

/** Above this, a phone browser stops feeling like it is working. */
export const POW_UNCOMFORTABLE_BITS = 20;

export function describePowBits(bits: number): string {
  const cost = powCost(bits);
  return `${bits} bits — about ${cost.laptop} on a laptop, ${cost.phone} on a phone`;
}
