/**
 * Typed errors. Every failure the SDK raises is one of these so callers can
 * branch on `err.code` instead of matching message strings.
 */

export type PoweurErrorCode =
  | "invalid_argument"
  | "invalid_signature"
  | "invalid_document"
  | "key_mismatch"
  | "pairing_mismatch"
  | "invalid_code"
  | "not_found"
  | "resolve_failed"
  | "relay_error"
  | "session_expired"
  | "challenge_required"
  | "decrypt_failed"
  | "policy_rejected"
  | "unsupported";

export class PoweurError extends Error {
  readonly code: PoweurErrorCode;
  readonly status?: number;
  readonly detail?: string;

  constructor(
    code: PoweurErrorCode,
    message: string,
    options: { status?: number; detail?: string; cause?: unknown } = {},
  ) {
    super(message, options.cause !== undefined ? { cause: options.cause } : undefined);
    this.name = "PoweurError";
    this.code = code;
    if (options.status !== undefined) this.status = options.status;
    if (options.detail !== undefined) this.detail = options.detail;
  }
}

/** The relay answered a non-2xx status. */
export class RelayError extends PoweurError {
  readonly relayCode?: string;

  constructor(
    message: string,
    options: { status?: number; detail?: string; relayCode?: string; cause?: unknown } = {},
  ) {
    super(options.relayCode === "session_expired" ? "session_expired" : "relay_error", message, options);
    this.name = "RelayError";
    if (options.relayCode !== undefined) this.relayCode = options.relayCode;
  }
}

/**
 * A 428 from the relay: the recipient's policy demands a challenge before an
 * anonymous message is accepted (EPIC-014).
 */
export class ChallengeRequiredError extends PoweurError {
  readonly challenge: {
    type: string;
    algo?: string;
    token: string;
    bits: number;
    expires_at?: string;
    detail?: string;
  };

  constructor(challenge: ChallengeRequiredError["challenge"]) {
    super("challenge_required", `recipient requires a ${challenge.type} challenge`);
    this.name = "ChallengeRequiredError";
    this.challenge = challenge;
  }
}

export function invalidArgument(message: string): PoweurError {
  return new PoweurError("invalid_argument", message);
}
