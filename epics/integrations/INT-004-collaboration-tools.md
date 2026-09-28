# INT-004 — Collaboration, productivity & federation tool integrations

- **Status:** proposed
- **Poweur prerequisites:** EPIC-022 (generic OAuth/OIDC + IndieAuth bridge), EPIC-003
  (WebDAV — many tools connect with zero upstream code), EPIC-005 (shares), EPIC-007
  (contacts/anti-spam), EPIC-009 (messaging)
- **Goal:** put Poweur ID inside the tools people already collaborate in — as login, as share
  target, as sync backend, as verified federation identity — so that "collaborate with
  alice.poweur.net" works the same whether the surface is a wiki, a chat server, a git forge or
  a notes app.

---

## A. File & groupware platforms (T2–T5)

### Nextcloud — `nextcloud/server`
The flagship integration of the whole program — the existing analysis in
`apps/docs/docs/future/capabilities.md` already singles it out as the closest fit. Three
layers: (1) Poweur sign-in via their mature OIDC support (works through EPIC-022); (2) an
**external storage** provider mounting the user's Poweur home next to Nextcloud files; (3) the
deep one — extend **Federated Cloud sharing** to accept `alice.poweur.net` as a share target,
resolving recipients through the Poweur resolver chain. Nextcloud federation today only reaches
other Nextcloud/oCIS servers and trusts them blindly; Poweur gives them cryptographically
verified recipients and reach into an entire non-Nextcloud population — federation that
finally crosses the product boundary, which is precisely the interop story Nextcloud sells.

### ownCloud Infinite Scale (oCIS) — `owncloud/ocis`
Go-based, spaces-oriented sibling of Nextcloud sharing the OCM federation protocol — the same
three-layer integration applies, and landing it in both proves the **Open Cloud Mesh ↔ Poweur
bridge** is a standard, not a one-off. Worth pursuing OCM upstream too: Poweur as an OCM
identity provider benefits every OCM implementer at once.

### Seafile — `haiwen/seafile`
Performance-focused file sync with a large self-hosting user base. Poweur sign-in plus
share-to-Poweur-ID gives Seafile the federation story it entirely lacks today — its users
currently cannot share to anyone off-server without anonymous links.

---

## B. Chat & real-time communication (T2 + T5)

### Matrix — Synapse `element-hq/synapse` / Element
Matrix solved federated messaging but its *identity* layer remains its known weak point —
third-party-ID lookups depend on centralized identity servers, and key verification is manual
emoji-comparison. Integration: a Poweur **identity-binding spec** (MSC proposal): publish your
MXID in your Poweur identity and vice versa, letting Matrix clients auto-verify "this MXID is
really alice.poweur.net" via signature instead of emoji ceremonies, plus discovery
(find a contact's MXID from their Poweur ID without a central identity server). For Matrix this
removes a centralization criticism using exactly the decentralized primitives their community
respects; for Poweur it's a bridge to the largest open federated chat network.

### XMPP servers — ejabberd `processone/ejabberd`, Prosody
The elder statesman of federated chat, with an extension culture (XEPs) built for this.
A "Poweur authentication & identity-binding" XEP — JID ↔ Poweur ID with signature proof, plus
SASL login via Poweur — gives the XMPP network verified cross-server identity and modern
passwordless auth, both periodic pain points, at the cost of one module per server.

### Mattermost — `mattermost/mattermost` / Rocket.Chat — `RocketChat/Rocket.Chat` / Zulip — `zulip/zulip`
Team chat's universal unsolved pain: **external collaborators**. Today, inviting a contractor
means provisioning a guest account and praying. With Poweur sign-in plus contact-verified guest
access, an external person joins with an identity your org can verify and your compliance team
can audit — and their identity (and DM reachability via Poweur messaging) survives after the
guest account is gone. All three accept SSO providers (OIDC/SAML), so T2 lands via the bridge;
the guest-access story is the differentiating follow-up PR.

### Jitsi Meet — `jitsi/jitsi-meet`
Open-source video meetings; supports JWT-based room auth. Integration: meeting links that admit
Poweur contacts automatically — "anyone in this group identity (EPIC-005) can join" — with
display names backed by verified IDs instead of free-text fields. Solves meeting-bombing
without lobbies-and-passwords friction, an actual documented pain for public Jitsi instances.

---

## C. Forges, forums, publishing (T2 + anti-spam superpowers)

### Forgejo — `forgejo/forgejo` / Gitea — `go-gitea/gitea`
Community git forges (Forgejo powers Codeberg). Layer 1: OAuth sign-in. Layer 2 is the gem:
**commit signature verification against Poweur identities** — both use Ed25519 SSH signing
keys, and a Poweur identity can publish its signing keys (`poweur-sys/public/`), so the forge
shows "verified: alice.poweur.net" on commits with key provenance that survives moving between
forges. Forgejo is also actively building ActivityPub federation (ForgeFed) and wrestling with
federated identity — Poweur is a ready answer they can adopt instead of inventing one.

### GitLab CE — `gitlabhq/gitlabhq`
OmniAuth provider for Poweur sign-in — straightforward, and it puts Poweur in front of
thousands of self-managed enterprise instances.

### Discourse — `discourse/discourse`
The standard community forum, where spam fighting is a permanent arms race of trust levels,
CAPTCHAs and moderation queues. A Poweur authenticator plugin (their managed-authenticator API
makes this small) plus a "verified Poweur ID" badge gives forums a signup path where the
anti-spam work is already done at the identity layer — and account recovery, Discourse's #1
support burden, disappears for those users (your identity is yours, not an email password).

### WordPress — plugin ecosystem
43% of the web. Two plugins: "Login with Poweur ID", and **comments restricted to valid Poweur
IDs** — comment spam, the oldest plague of the open web, filtered by identity verification
rather than Akismet heuristics. Even modest adoption makes Poweur visible to an enormous
audience, and the plugin needs nothing from WordPress core.

### Mastodon — `mastodon/mastodon` (and the Fediverse)
Mastodon's identity verification is rel="me" link checks; impersonation remains a real problem.
Integration: WebFinger alignment (a Poweur ID answers `acct:` lookups — both systems are
HTTPS-name-based, the mapping is nearly mechanical) plus profile-field verification where
`alice.poweur.net` proves control cryptographically, rendering a verified checkmark no one can
buy. Gives the Fediverse a decentralized blue-check it has wanted since the Twitter exodus,
with zero new authority introduced.

---

## D. Notes, knowledge & creative tools (T3 — the "bring your own storage" wave)

For this whole group the WebDAV decision in EPIC-003 pays off: several integrate with **zero
upstream code** — the task is a tested recipe, a settings preset PR, and a tutorial.

> **Changed by EPIC-020.** The relay no longer serves WebDAV and private/shared files are end-to-end
> encrypted. The zero-code path is now `rclone serve webdav` on the user's device (E20-T13), which
> decrypts locally; recipes move to INT-006 section D. Mobile-only WebDAV use has no path until
> native file providers (E20-T14). **INT-004-T8 (WOPI host on the relay) conflicts with E2EE:** an
> office document server must read plaintext, so it either runs on the user's side or is granted
> a share as a service identity — redesign before building.

### Joplin — `laurent22/joplin`
Popular open notes app with *native WebDAV sync*. Pointing it at the Poweur home works on day
one; the upstream PR is a "Poweur" preset in the sync-target list plus token-based auth flow.
Joplin users get encrypted-transport sync under their own identity and — via Poweur shares —
notebook sharing with any Poweur ID, a long-requested Joplin feature that otherwise requires
their paid Joplin Cloud.

### Obsidian (community plugins, e.g. `remotely-save/remotely-save`)
Obsidian core is closed but its plugin ecosystem is open source and WebDAV-ready. A documented
Poweur target in remotely-save (small PR) gives the largest PKM community vault sync +
selective vault *sharing* — "share this folder of notes with my co-author's ID" — without
Obsidian Sync subscriptions.

### Logseq — `logseq/logseq`
Open-source outliner whose community continually asks for self-controlled sync; same
WebDAV/share recipe, same pitch: your graph in your home, shared subgraphs by identity.

### Outline — `outline/outline` / BookStack — `BookStackApp/BookStack`
Team wikis with OIDC support — sign-in works via the bridge immediately; the follow-up is
share-a-document-to-a-Poweur-ID for cross-org wiki access (today: PDF email attachments).

### CryptPad — `cryptpad/cryptpad`
End-to-end-encrypted collaborative suite — philosophically the closest collaborative editor to
Poweur. Their hardest UX problem is contact discovery and key exchange between users; Poweur
contacts (pinned keys, EPIC-007) solve exactly that. Integration: use Poweur IDs as CryptPad
contact/share targets, exchanging CryptPad's own keys over Poweur's verified encrypted channel.

### Excalidraw — `excalidraw/excalidraw`
The beloved whiteboard. Scene files saved to / loaded from Poweur folders (their storage
backend is pluggable), making "send bob the architecture sketch" a share instead of a PNG
export — and collaboration rooms gated to contacts instead of anyone-with-the-link.

### OnlyOffice — `ONLYOFFICE/DocumentServer` / Collabora Online — `CollaboraOnline/online`
Open-source office suites that integrate via WOPI. A WOPI host on the Poweur relay means
office documents in a Poweur home open and co-edit *in place* — together with shares this makes
the Poweur home a genuine Drive/Docs alternative, and brings both suites new deployment surface
beyond their Nextcloud/ownCloud niches.

### Penpot — `penpot/penpot`
Open-source design platform (Figma alternative): OIDC sign-in now, share-design-to-ID later —
design review with external clients under verified identity is a sales-friendly story for them.

---

## E. Project & task tools (T2 + EPIC-006 tasks convention)

### Vikunja — `go-vikunja/vikunja` / Focalboard — `mattermost-community/focalboard` / Planka
Self-hosted task managers — ideal first adopters of the **tasks convention** (E06-T5): OIDC
sign-in, then import/export or native storage of tasks in `/apps/<id>/` with project sharing
via Poweur grants. Vikunja in particular is small, active, and CalDAV-friendly — a receptive
first upstream for proving the "apps converge on domain conventions" thesis.

### OpenProject — `opf/openproject` / Taiga — `taigaio` / Plane — `makeplane/plane`
Larger PM suites: start with sign-in (all support OIDC), then cross-org work packages — "assign
this task to a contractor's Poweur ID, artifacts flow through a shared folder" — the
external-collaborator story again, which none of them solve today without provisioning seats.

---

## F. Infrastructure neighbors (T1/T5 — coexistence plays)

### Syncthing — `syncthing/syncthing`
Peer-to-peer sync with cryptic device IDs and manual introductions. Integration: publish
Syncthing device IDs in `poweur-sys/public/`, so adding a sync peer is "connect to
alice.poweur.net" with keys verified through her identity — Poweur as the introduction layer,
Syncthing as the LAN-fast transport. Complementary, not competitive, and their community knows
the device-ID UX is their biggest onboarding wall.

### Radicale — `Kozea/Radicale` / Baïkal (CalDAV/CardDAV)
Calendars and contacts are files; host them in the Poweur home (Radicale's storage is
literally a directory tree). Then `contacts.json` ↔ CardDAV bridging and calendar-invite
delivery over Poweur messaging replace the email plumbing of scheduling — self-hosted
calendaring gains the invite interop it never had.

---

## Tasks

- [ ] **INT-004-T1** Nextcloud: OIDC tutorial → external-storage app → Federated-Cloud-share
      bridge PoC (flagship); file the OCM upstream issue in parallel
- [ ] **INT-004-T2** Joplin preset PR + Obsidian remotely-save support + Logseq recipe (the
      zero-code WebDAV wave — fastest visible wins)
- [ ] **INT-004-T3** Discourse authenticator plugin + WordPress login/comments plugins
- [ ] **INT-004-T4** Forgejo/Gitea OAuth + commit-verification PoC; engage ForgeFed discussion
- [ ] **INT-004-T5** Matrix identity-binding MSC draft + Synapse module PoC
- [ ] **INT-004-T6** Mattermost/Rocket.Chat/Zulip SSO guides + external-guest design doc
- [ ] **INT-004-T7** CryptPad contact-exchange PoC; Excalidraw storage adapter
- [ ] **INT-004-T8** WOPI host on relay + OnlyOffice/Collabora demo (the "Drive alternative" demo)
- [ ] **INT-004-T9** Vikunja tasks-convention pilot (pairs with E06-T5)
- [ ] **INT-004-T10** Syncthing introduction-layer convention + Radicale home-hosted storage recipe
