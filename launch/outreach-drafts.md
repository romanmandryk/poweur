# Outreach drafts

First-pass messages for the Phase 1 communities in [`README.md`](README.md). Rewrite them in
your own voice, keep each one short, and read the channel's rules before posting. Post as a
person, ask for feedback rather than attention, and only post once the gate noted under each
draft is met. Replace `[…]` before sending.

Facts used here come from EPIC-022 (IndieAuth bridge: protocol-level tests pass, bridge live
at oauth.poweur.org, **live third-party IndieAuth clients still open in E22-T8**) and from the
launch README. Don't claim more than that.

---

## IndieWeb chat (`#indieweb-dev`)

**Gate:** two independent IndieAuth clients have signed in through oauth.poweur.org.

> Hi all, I'm Roman. I've been building Poweur, an open ID system (`alice.poweur.net`) with
> end-to-end messaging and file sharing. It includes an OAuth/OIDC bridge with IndieAuth
> support, so an ID can be the `me` for a site that speaks IndieAuth. I've tested it against
> [indieauth.rocks / indielogin.com / …], but I'd love people who know the spec to try to
> break it. The bridge is at https://oauth.poweur.org, the code is at [repo link], and I'd
> especially like to know how the profile URL mapping (`alice.poweur.net` →
> `https://alice.poweur.net/`) fits what your clients expect. Happy to fix whatever you find.

## IndieWeb wiki page (outline)

- What Poweur is, in two sentences
- How to sign in to an IndieAuth site with an ID (steps)
- Discovery: the relay's `Link: rel="indieauth-metadata"` header on hosted identities
- Known limits (`me` is the hosted handle; no self-hosted domain profile pages yet)
- Links: bridge docs (`apps/docs/docs/auth/oauth-oidc-bridge.md`), source, issue tracker

## Direct note to an IndieAuth client or server maintainer

**Gate:** as above. Send one per person, and mention something specific you like about their project.

> Hi [name], I've been using [project] and like [specific thing]. I'm building Poweur, an
> open ID system with an IndieAuth-compatible bridge (https://oauth.poweur.org). Would you be
> willing to try signing in to [project] with an ID like `alice.poweur.net` and tell me what
> breaks? No obligation. If it works I'd like to list [project] as a known-good client on the
> IndieWeb wiki, and if not I'd like to fix it. Ten minutes of your time would help a lot.

## IndieAuth spec community (review request)

> I'm implementing IndieAuth on top of an existing identity system where the user's
> identifier is an FQDN (`alice.poweur.net`), and the bridge exposes it as the profile URL
> `https://alice.poweur.net/`. Before I tell people it's compliant, I'd like a review of
> three things: [1. how discovery is advertised, 2. how `me` is canonicalised, 3. what the
> token endpoint returns for login-only requests]. Notes: [link to `oauth-oidc-bridge.md`].

## DIF Slack / W3C Credentials CG

**Gate:** none beyond the site being live.

> Sharing a project for feedback, not promotion. Poweur treats a DNS name as the account
> identifier: `alice.poweur.net` resolves to a signed identity document, and the same ID is
> projected as `did:web:alice.poweur.net`. We use it for E2E messaging, file sharing and
> sign-in. I'd like feedback on the `did:web` projection and on key rotation and recovery:
> [spec link]. What have I missed compared with how you handle these?

## Solid forum

> Poweur overlaps with Solid in wanting people to own their identity and data. The
> difference is that [honest, specific difference: e.g. keys held by the user with
> passkey-backed recovery, E2E encryption by default, relay-based delivery]. I'd like an
> honest comparison from people who know Solid well, especially on where I'm wrong or
> reinventing something.

## Local-first Discord

> Poweur is a small experiment in identity + messaging + file sync where the user holds the
> keys. I'd like feedback from people who've built local-first sync on whether my file-sync
> model ([link]) has problems I'm not seeing.

## r/selfhosted

**Gate:** you have followed the self-hosting guide on a fresh VM, with a stopwatch.

Title: `Poweur: a self-hostable relay for open IDs, E2E messaging and file sharing (feedback wanted)`

> I've been building an open ID system with E2E messaging and file sharing. The relay is a
> single Go binary and [Docker image]. Compose example below. What I want to know: how long
> did it take you, and where did you get stuck? It's AGPL-3.0, so [what that means for
> hosting]. It's not audited yet (external review is outstanding), so I'm not suggesting you
> put anything critical on it. [compose snippet] [link to the self-hosting guide]

## Privacy Guides forum

**Gate:** the threat-model page exists.

> Poweur's relay sees [what it sees] and cannot see [what it can't]. The threat model is at
> [link]. I'd like the harshest reading you can give it, especially on metadata and on
> trusting the relay operator.

## Fediverse / Bluesky (short)

> I'm building Poweur, an open ID for sign-in, E2E messaging and file sharing that you can
> also self-host. It's early and I want feedback. Claim a name and message `hello.poweur.net`
> to try it: https://poweur.org #OpenSource #Privacy #SelfHosted #IndieWeb

## NLnet / NGI Zero

Not a message: an application. Check the next call deadline at nlnet.nl, read the call text,
and reuse the blog post's problem statement plus the licence split (AGPL-3.0 and Apache-2.0),
the protocol conformance vectors and the outstanding security review as the work packages.
