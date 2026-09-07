/**
 * Typed errors. Every failure the SDK raises is one of these so callers can
 * branch on `err.code` instead of matching message strings.
 */
export class PoweurError extends Error {
    code;
    status;
    detail;
    constructor(code, message, options = {}) {
        super(message, options.cause !== undefined ? { cause: options.cause } : undefined);
        this.name = "PoweurError";
        this.code = code;
        if (options.status !== undefined)
            this.status = options.status;
        if (options.detail !== undefined)
            this.detail = options.detail;
    }
}
/** The relay answered a non-2xx status. */
export class RelayError extends PoweurError {
    relayCode;
    constructor(message, options = {}) {
        super(options.relayCode === "session_expired" ? "session_expired" : "relay_error", message, options);
        this.name = "RelayError";
        if (options.relayCode !== undefined)
            this.relayCode = options.relayCode;
    }
}
/**
 * A 428 from the relay: the recipient's policy demands a challenge before an
 * anonymous message is accepted (EPIC-014).
 */
export class ChallengeRequiredError extends PoweurError {
    challenge;
    constructor(challenge) {
        super("challenge_required", `recipient requires a ${challenge.type} challenge`);
        this.name = "ChallengeRequiredError";
        this.challenge = challenge;
    }
}
export function invalidArgument(message) {
    return new PoweurError("invalid_argument", message);
}
