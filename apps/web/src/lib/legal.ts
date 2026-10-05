/**
 * The hosted service's legal pages. They belong to poweur.net's operator, so
 * they are shown only where that service is the one in use: a self-hosted
 * relay serves this same app under its own terms, not these.
 */
export const LEGAL_LINKS = {
  privacy: "https://poweur.org/legal/privacy/",
  terms: "https://poweur.org/legal/terms/",
  legal: "https://poweur.org/legal/",
} as const;

/** The hosted domain whose operator these pages are for. */
const POWEUR_HOSTED_DOMAIN = "poweur.net";

/**
 * Whether the legal pages apply: the relay hosts `poweur.net`, or the identity
 * in use lives under it.
 */
export function showsPoweurLegal(hostedDomains: readonly string[] | undefined, identity?: string | null): boolean {
  if ((hostedDomains ?? []).includes(POWEUR_HOSTED_DOMAIN)) return true;
  const id = String(identity ?? "").toLowerCase();
  return id.endsWith(`.${POWEUR_HOSTED_DOMAIN}`);
}

/**
 * Where hosted-service feedback goes: a Poweur ID, so people message us with the product itself.
 * It belongs to poweur.net's operator, so it is offered only where `showsPoweurLegal` is true.
 */
export const FEEDBACK_ID = "support.poweur.net";
