# EPIC-027 — Public web: feeds, following, community boards & indexers

- **Status:** proposed
- **Priority:** P1 (after EPIC-020 waves 1–3; community boards need nothing beyond them)
- **Depends on:** EPIC-020 (public folders, append files, change feeds, caps, proof-of-work
  links), EPIC-009 T7–T10 (intent types, typed routing, follow feeds), EPIC-014 (anonymous
  messages), EPIC-024 (group identities as hosts), EPIC-026 (headless scenarios)
- **Interacts with:** COM-5 (paid feeds as renewing shares, broadcast channels), INT-006
  (Bluesky/Nostr/Fediverse handles), INT-002 (payments for listings)
- **Unlocks:** microblogs, blogs and newsletters for every ID; community and classifieds boards;
  network-wide timelines, search, topics and location search through replaceable indexers

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E27-T1 Post & feed convention | **open** | post/reply/repost/like records, `feed.json`, Atom, microformats, tombstones |
| E27-T2 Following & followers-only feeds | **open** | private follow list, client timelines, approved followers via shares with epoch keys |
| E27-T3 Social notifications | **open** | mention/reply (and optional follow) intents with inbox-policy rules |
| E27-T4 Public caching & subscription proxy | **open** | CDN-friendly public nodes; one client connection fanned in by the user's relay |
| E27-T5 Community boards | **open** | a group identity's public append log; members, proof-of-work links, moderation, removal |
| E27-T6 Listings & reviews | **open** | classifieds schema with coarse location cells, expiry, categories; signed reviews |
| E27-T7 Indexer role | **open** | service identities that crawl opted-in public data and answer signed queries |
| E27-T8 Reference indexer | **open** | timelines, threads, hashtags, search, counts, location search — outside the relay |
| E27-T9 Interop: Fediverse & Bluesky | **open** | webmentions + microformats for Bridgy Fed; handle linking via INT-006 |

## Goal

Let any Poweur ID publish (posts, a blog, a newsletter, listings) and let anyone follow, reply and
trade — with the author's drive as the source of truth, the relay unchanged and stateless, and
network-wide features (discovery, search, topics, "near me") provided by **indexers** that
anyone can run and any client can swap.

## Design direction

### Three shapes of social data

| Shape | Examples | Built on |
|-------|----------|----------|
| **One → many** | microblog, blog, newsletter, a seller's own listings | a public (or follower-shared) feed folder in the author's drive; followers pull it |
| **Many → many in a community** | neighbourhood board, club, topic group, city classifieds | a public append log in a **group identity's** drive; members append, readers follow |
| **Many → many across the network** | global timeline, hashtags, search, trending, "within 10 km" | an **indexer** — a service identity with its own database |

The first two need nothing beyond EPIC-020 and EPIC-009. The third is the one place where
centralization is legitimate, and it is kept replaceable.

### Everyone writes to their own drive

A reply or repost is a post in *the replier's* feed that references the original; a like is a
record in the liker's public `likes` log; a listing lives in the seller's `Classifieds/` folder.
Every record is signed by its author (EPIC-020 manifests and append records), so readers and
indexers verify it without trusting the relay that served it. Deleting publishes a tombstone
that followers and indexers must honour.

### Following is lighter than a contact

Following a public feed needs no consent and is recorded only in the follower's own encrypted
`.poweur/private/follows.json` — the author need not know. Followers-only feeds are a share on
the feed folder that the author approves; the folder key rotates by **epoch** (e.g. monthly or
on removal), sealed to each approved follower (~100 bytes each), so thousands of followers or
paid subscribers are practical. Public feeds have no per-follower cost at all.

### Pull scales with two additions

1. **Public nodes are CDN-cacheable.** Posts and media are immutable plaintext chunks with
   long cache lifetimes; the feed head is cached for tens of seconds. A popular author's relay
   serves the CDN, not a million readers.
2. **A subscription proxy on the reader's relay.** The client keeps one event stream to its own
   relay; the relay keeps one upstream stream per remote relay, shared by all its users
   following authors there, plus a batch "feed heads" call. The proxy's state is in memory and
   re-registered by clients on connect, so the relay stays stateless.

Push fan-out (one message per follower per post) stays for email subscribers and explicit
alerts only (COM5-T5).

### Indexers

An indexer is **a Poweur ID running a service** — any infrastructure it likes (Postgres,
queues, search, geo indexes). It is never part of the relay.

- **Input:** public data only, from authors who opt in (an announcement message to the indexer,
  or a public `index: allow` flag in the feed) plus what is discoverable through mentions and
  boards. It reads through the public change feeds; it honours tombstones and opt-outs.
- **Output:** signed query responses whose items are references to signed records, so a client
  can verify every result against the author. Timelines, threads, hashtags, search, counts,
  trending, location search.
- **Replaceable:** several indexers can coexist; clients pick one or more and can always fall
  back to pulling followed feeds directly.
- **Moderation** is per indexer (its own policy, optional labels from others); authors keep
  their data whatever an indexer decides.
- **Not AT Protocol.** Its public-first repositories conflict with the encrypted drive and a
  PDS is a database-backed service. Interop comes through handles (INT-006) and Bridgy Fed
  (E27-T9); a Poweur PDS can be a separate service identity later if demand appears.

### Community boards

A board is a group identity (EPIC-024) hosting `board.log`, a **public** append file:

- members get `append`; outsiders post through a link with caps and proof-of-work (their
  records are marked anonymous); readers follow the board like any feed
- entries are either whole posts or references to records in the poster's own drive
  (a listing stays in the seller's `Classifieds/` and the board points to it)
- moderators append `hide` records that every reducer honours; **legal removal** is a
  compacting rewrite plus trim that deletes the bytes from the store
- the group's drive pays, bounded by per-member and per-link caps (EPIC-020 E20-T7)

### Listings and location

A listing is a structured post: title, description, category (open, namespaced taxonomy),
price and currency, photos, expiry, and a **coarse location cell** (H3, neighbourhood-sized).
The exact address is never in the listing; it goes in the buyer–seller conversation. Buyers
contact sellers with `sys.contact.message` carrying the listing reference (signed, or anonymous
with proof-of-work). Reviews are signed records in the reviewer's drive referencing the
listing and the counterparty; indexers aggregate them.

## Tasks

### E27-T1 — Post & feed convention

- [ ] PCP: post, reply, repost, like, tombstone records (stable ids, references by
      `identity + node + record id`, Markdown body, media as sibling files); `feed.json` index
- [ ] Atom output and microformats (h-entry/h-feed) generated next to `feed.json`, so feed
      readers and IndieWeb tools work with no integration (supersedes INT-006-T12's Atom item)
- [ ] Go/TS validators and vectors; headless publish/read helpers in both SDKs
- [ ] CLI `poweur post|reply|repost|like|unpost`

**Acceptance:** a post, a reply on another relay and a delete round-trip through `feed.json`,
Atom and microformats; a feed reader (Miniflux in INT-006 CI) shows the posts.

### E27-T2 — Following & followers-only feeds

- [ ] Follow list in `.poweur/private/follows.json`; `poweur follow|unfollow|timeline`;
      client-side timeline assembly from followed feeds (reuses E09-T10)
- [ ] Followers-only feeds: follow request → owner approval → share with epoch keys; removal
      starts a new epoch; paid tiers are renewing shares (COM-5)
- [ ] Measured budget: sealing cost and time per epoch for 10k followers

**Acceptance:** a follower on relay C sees public posts without the author knowing; an approved
follower reads followers-only posts and loses access to posts in the next epoch after removal.

### E27-T3 — Social notifications

- [ ] Intent types in the E09-T7 registry: `sys.social.mention`, `sys.social.reply`, optional
      `sys.social.follow` (off by default for public follows)
- [ ] Inbox-policy rules: accepted from contacts and people the recipient follows; strangers
      need proof-of-work and are rate-limited; never opens a chat
- [ ] Clients assemble threads from reply notifications when no indexer is configured

**Acceptance:** a stranger's reply reaches the author as a notification under proof-of-work; a
flood is refused; the thread renders from notifications alone.

### E27-T4 — Public caching & subscription proxy

- [ ] Public nodes: immutable chunk URLs with long `Cache-Control`, short-lived feed heads with
      `ETag`; documented CDN setup (Cloudflare in production, per deploy docs)
- [ ] Relay subscription proxy: clients register follows on connect; the relay holds one
      upstream stream per remote relay and fans events in; batch "feed heads" endpoint; memory
      only, rebuilt from client registrations (implemented in E20-T5's API)
- [ ] Load test: one author, simulated 100k followers behind the CDN; one reader following 300
      feeds on 150 relays through the proxy

**Acceptance:** the author's relay request rate stays flat as followers grow behind the CDN; the
reader holds one connection and receives every followed post.

### E27-T5 — Community boards

- [ ] Board convention: group identity, public `board.log`, entry types (post, reference, hide,
      pin), `board.json` (name, rules, categories, moderators)
- [ ] Member `append`, anonymous posting links with caps and proof-of-work, moderator hide,
      legal removal by compaction + trim
- [ ] CLI `poweur board create|post|hide|remove|follow`
- [ ] Headless scenario E26-T9

**Acceptance:** E26-T9 passes on one relay and across relays.

### E27-T6 — Listings & reviews

- [ ] Listing PCP (fields above, H3 cell resolution guidance, expiry), category taxonomy with
      namespaces; review record PCP
- [ ] Buyer contact flow on `sys.contact.message` with a listing reference
- [ ] Validators and vectors in Go and TS

**Acceptance:** a listing validates in both languages; an expired listing is hidden by reducers
and indexers; no listing carries more than a coarse cell.

### E27-T7 — Indexer role

- [ ] Spec: indexer identity and capabilities document, opt-in announcement (`sys.index.announce`
      or a feed flag), crawl over public change feeds, tombstone and opt-out handling, signed
      query responses referencing signed records, rate and abuse rules
- [ ] Client configuration: choose indexers, verify results, fall back to direct pull
- [ ] Moderation labels format (indexers may publish and consume them)

**Acceptance:** spec merged with worked examples; a client verifies an indexer's result against
the author's signature and detects a forged item.

### E27-T8 — Reference indexer

- [ ] `apps/indexer`: a separate service (its own database and queue — not the relay) with
      timelines for a user's follows, threads, hashtags, full-text search, like/reply counts,
      board aggregation and location search over listing cells
- [ ] Runs against the integration relays; deployable next to the hosted relay but never
      required by it

**Acceptance:** with the indexer on, a reader finds a listing by category within a radius and
a post by hashtag; with it off, following and boards still work.

### E27-T9 — Interop: Fediverse & Bluesky

- [ ] Webmention endpoint for public feeds and microformats (E27-T1) so Bridgy Fed can bridge
      posts to Mastodon and Bluesky; document opt-in
- [ ] Handle linking via INT-006-T5 (atproto-did, NIP-05, WebFinger)

**Acceptance:** a Poweur post appears on a Mastodon account through Bridgy Fed, and a reply
there comes back as a webmention.

## Non-goals

- Indexers inside the relay, or any relay database
- Implementing AT Protocol or ActivityPub natively (bridges and handles instead)
- Global push fan-out for public posts
- Exact locations in public listings
