// Package bridge is the Poweur email bridge (EPIC-023): it carries mail
// between the email network and the Poweur IDs that opted into it.
//
// It does not speak SMTP to the internet. A mail server (Stalwart for the
// public deployment) owns the MX, TLS, SPF/DKIM/DMARC checks, spam scoring,
// queueing, DKIM signing and outbound relaying; the bridge sits behind it on
// three network paths, so the two can run on different hosts:
//
//   - RCPT hook (HTTPS, shared secret): the mail server asks at RCPT TO
//     whether an address exists, and the bridge answers from the recipient's
//     signed opt-in — unknown and opted-out addresses are refused before
//     anything is queued, so the bridge never causes backscatter;
//   - inbound LMTP (STARTTLS + AUTH): the mail server's queue hands each
//     accepted message over; the bridge parses it, sanitizes it, encrypts it
//     to the recipient and delivers it as an `email.message` from its own
//     Poweur ID, answering per recipient so a relay outage is a 4xx the mail
//     server retries;
//   - outbound submission (SMTP AUTH): `email.send` messages the bridge
//     drains from its own inbox become MIME submitted to the mail server,
//     which signs and relays them.
//
// The bridge is an ordinary Poweur identity. It holds no relay credential,
// keeps no plaintext after delivery (large parts and the original message
// are kept only sealed to the recipient, for a while), and its relays store
// only ciphertext.
package bridge

// Version is the bridge semver. Bump the patch when shipping apps/email
// changes (AGENTS.md "Version bumps").
const Version = "0.1.1"
