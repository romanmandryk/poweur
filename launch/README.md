# Launch checklist

Three phases, starting Thursday 2026-09-24:

| Phase | Dates | Goal | Audience |
|-------|-------|------|----------|
| **0. Ready** | Sep 24 – Oct 1 | Nothing embarrassing, nothing illegal, nothing that loses data | Just us |
| **1. Pre-launch** | Oct 1 – Oct 12 | Real users, real feedback, fix the top issues | Friends, then small expert communities (identity/auth, open internet, self-hosters) |
| **2. Launch** | Tue Oct 13 (+ that week) | Public story, as much reach as possible in one week | Hacker News, Product Hunt, Reddit, newsletters, press |

The blog post draft is [`blog-why-poweur.md`](blog-why-poweur.md). Both the pre-launch posts and
the launch day link to it.

**Rule for every item:** if it isn't done by the phase gate, it moves to the next phase or
comes off the list. The launch date doesn't move for a feature.

---

## Part 1 — Product (Phase 0, one week)

What exists today is enough to launch: IDs, E2E messaging, files and sharing, sign-in and the
OIDC bridge, CLI, SDK and web app. This week should go on **trust, legal and first-run
polish, not new epics**.

### P0 — blockers (don't share a link until these are done)

- [x] **Pick and add a LICENSE.** *Done: AGPL-3.0 at the root; Apache-2.0 in `packages/identity`,
      `packages/client-ts`, `apps/cli`, `apps/guestbook`, `conventions`, `apps/docs`; README
      License section, DCO sign-off in CONTRIBUTING, TRADEMARKS.md.* Original note: The README says the repo isn't licensed yet. "Open source"
      on the site and in every post isn't true until it is, and HN and r/selfhosted will
      notice straight away. Suggested split:
      AGPL-3.0 for the relay, OAuth bridge and web app (`apps/api`, `apps/oauth`, `apps/web`),
      and Apache-2.0 for anything others embed (`packages/identity`, `packages/client-ts`,
      `apps/cli`, `conventions/`, the docs). Add `LICENSE` files, SPDX headers, and a
      license line in `package.json` / the README
- [x] **Analytics without third parties** *(done: Better Stack removed from every frontend.
      Grafana Faro sends anonymous page views, errors and web vitals first-party to Alloy/Loki
      (dashboard "Poweur Web: analytics & errors"); the ID is attached only after Settings →
      Diagnostics → "Include my ID")*
- [ ] **Legal pages on poweur.org, linked from the footer and the claim screen:** *Drafted
      at `apps/site/legal/` (overview/contact, privacy, terms + acceptable use) and linked
      from the site footer, the app's landing, the claim boxes and Settings → About. Before
      publishing, fill in every highlighted `[…]` detail (operator, address, emails, country,
      backup retention, how to delete an ID).* Originally: privacy
      policy (what the relay stores; that telemetry is opt-in, as EPIC-013 implements; no
      message contents), terms of service + acceptable use (spam, abuse, illegal content,
      name squatting), and an imprint/contact page if you're operating from the EU
- [ ] **Security contact:** `SECURITY.md` + `/.well-known/security.txt` on poweur.org and
      poweur.net. *Drafted for poweur.org; needs the security email. poweur.net (the relay)
      doesn't serve one yet.* Be honest in the docs that the external security review (E11-T7) is still
      outstanding
- [ ] **Abuse path:** reports go to `info@poweur.org` (see operational accounts below), a runbook for suspending a hosted name (who, how, how
      fast), and a list of reserved names checked for obvious impersonation (brands, `admin`,
      `support`, `poweur`, `security`, …)
- [ ] **Backups: prove them.** Production has Hetzner's automatic server backups (nightly,
      7 days), which is what the privacy policy says. Restore one to a scratch server once
      and write down how long it took. Later: turn on the off-site encrypted restic backup
      to a Storage Box, which is already built ([`deploy/BACKUP.md`](../deploy/BACKUP.md)),
      and update the policy's backup row if its retention differs
- [ ] **Alerts that wake you up:** relay down, disk > 80 %, error-rate spike, cert expiry
      (EPIC-013 has the stack; check the routes actually reach your phone)
- [ ] **Make the site match reality:**
  - [x] Hero said "12 GB home"; removed. Free storage is not advertised anywhere
  - [ ] The site's claim box ignores the typed name: make it go to
        `https://poweur.net/app/?handle=<name>` (the app already pre-fills from `?handle=`)
  - [ ] Hosting card "Your own domain → How it works": decide whether bring-your-own-domain
        is a launch story now that the app's DNS path is gone. Link it to self-hosting, or
        keep the web-identity doc and say it's CLI-only for now
  - [ ] Footer X / LinkedIn "soon": create the accounts or remove the links
  - [ ] Self-host Sora/Inter (the TODO in `index.html`): privacy people will check for
        Google Fonts requests
  - [ ] Replace the `ph` placeholder icons (primitives and agents sections)
  - [ ] Check every "Available" badge on the use-case cards against the epics. Send
        (E05-T7) is still partial
- [ ] **First-run test on real devices, fresh browser profiles:** iPhone Safari, Android
      Chrome, macOS Safari/Chrome, Windows Chrome/Edge, Firefox. Passkeys with PRF are
      required, so write down exactly which browsers work and say it on the claim screen and
      in a docs FAQ. Firefox users will be vocal
- [ ] **The recovery kit works:** claim → save kit → wipe browser → restore. Losing an ID is
      the worst launch-week story you can get

### P0 — operational accounts and contacts

- [ ] **`info@poweur.org` mailbox.** The legal pages, `security.txt` and `SECURITY.md`
      already point at it, so it must work before the pages go live. Cheapest setup:
      Cloudflare Email Routing (free) forwarding to your inbox for receiving, plus a
      mailbox provider for replying from the address (Fastmail, Zoho, Proton, Google
      Workspace). Set SPF, DKIM and DMARC on poweur.org; the email-bridge epic (EPIC-023)
      lists the records poweur.org needs if it never sends mail itself
- [ ] **`support.poweur.net`, created with the operator token** (steps: `deploy/OPS.md` →
      "Operator IDs"). Store the recovery kit in your password manager, add the ID to your
      laptop's browser with "Add this device" + `poweur key approve`, and set
      `QUOTA_CONTACT=support.poweur.net` in the deploy environment. Do `hello.poweur.net`
      (the demo bot) the same way. Unset `OPERATOR_TOKEN` afterwards
- [ ] **npm:** create the `@poweur` org and 2FA-protect the account now; the names
      `@poweur/client` and `poweur` are free today (checked 25 Sep 2026) and cost nothing
      to hold. See the publish item under P1
- [ ] **Developer accounts for the mobile beta** (Part 1b): Apple Developer Program and
      Google Play Console. Start these on day one: Apple verification and Google's
      identity checks can take days

### P1 — very high value, small (do in Phase 0 if possible, otherwise Phase 1)

- [ ] **A live thing to talk to:** `hello.poweur.net`, a bot ID that replies to any message
      (a few dozen lines on `@poweur/client`). "Claim a name, message hello.poweur.net" is a
      30-second demo for every post
- [ ] **A live sign-in demo:** deploy `apps/guestbook` behind the OAuth bridge so "Sign in
      with Poweur" can be clicked, not just read about
- [ ] **Publish to npm** (E17-T7). The site shows SDK code, and developers will run
      `npm i` in the first hour:
  - [ ] `@poweur/client` (SDK + `poweur` CLI) from a GitHub Actions release workflow with
        npm provenance, on a version tag; changelog in the release notes
  - [ ] Unscoped `poweur`: a tiny package that depends on `@poweur/client` and exposes
        its bin, so `npx poweur identity create alice.poweur.net --hosted` works. Also stops
        anyone else taking the name
  - [ ] Check the quickstart in `apps/docs/docs/clients/js-sdk.md` from a clean machine
        with `npm i`, not a checkout
  - [ ] Optional: Go CLI binaries on GitHub Releases (GoReleaser) and a Homebrew tap
- [ ] **Follow the self-hosting guide on a fresh VM** yourself, with a stopwatch.
      Self-hosters will do exactly this
- [ ] **Feedback channels:** turn on GitHub Discussions (the footer already links to it),
      add a `feedback.poweur.net` ID (dogfooding: people message you *with* the product),
      and put a "Send feedback" item in Settings
- [ ] **Launch-week load check:** a rough load test of claim + send + upload; know the limits
      of the box, how to scale it vertically, and what the registration rate limits and PoW
      are set to
- [ ] **Status page** (Better Stack uptime is wired in server-side): link it from the footer

### P2 — Phase 1 (the two-week feedback window), in this order

1. Whatever the pre-launch users hit most. Triage daily and keep a public "known issues" list
2. **E15-T12** onboarding failure states and **E15-T11** desktop layout. Launch traffic is
   mostly desktop, and the app's desktop layout is still open
3. **E05-T7 Send** web UI: the most shareable feature ("send a file with a verified sender").
   Only if it fits; it's the best growth loop on the roadmap
4. **E05-T6** remaining guest-conversion UX
5. **Mobile beta:** now in Part 1b (pre-launch), not here. It used to read: TestFlight + Android internal testing (E19-T5), as a sign-up link for
   the launch post, not a store release
6. **Supporter plan** (see Part 4)

### Not before launch

EPIC-020 storage v2, 023 email bridge, 024 Spaces, 025 realtime, 026 full billing, 027–030,
and the INT-* integrations. They belong in the blog's "where this leads" section, not in the
launch scope.

---

## Part 1b — Mobile apps: public beta before launch

Goal for pre-launch: anyone can install the iOS and Android apps from a public link
(self-invite), and push notifications work. What exists: the Capacitor shell
(`apps/mobile`, bundle ID `net.poweur.app`) builds and runs on the iOS simulator; Android
builds. **Push does not exist yet** (E19-T4), and there is no signing or release
pipeline (E19-T5). Allow about two weeks; the two store clocks below are the long poles.

### Accounts and store setup (start day one)

- [ ] **Apple Developer Program** (99 USD/year). As a sole trader you enrol as an
      individual: the seller name on the store is "Roman Mandryk". Enrolling as an
      organisation needs a D-U-N-S number and a legal entity (switch when the Lda exists;
      Apple can migrate the account)
- [ ] **Google Play Console** (25 USD once). **New personal accounts must run a closed test
      with at least 12 testers for 14 days in a row before they can publish to production
      or open testing**, so start the closed test as early as possible. An organisation
      account (D-U-N-S) is exempt
- [ ] **EU trader status** in App Store Connect and Play Console. Under the Digital
      Services Act, traders' address, phone and email are shown on the store page in the
      EU: use R. Prudêncio Franco da Trindade 4, 2655-344 Ericeira, `info@poweur.org`, and
      a business phone number, not a personal one
- [ ] Register `net.poweur.app` in both consoles; app name "Poweur" (check it's not taken
      in either store)

### Store listings and compliance

- [ ] Privacy policy URL (`https://poweur.org/legal/privacy/`), support URL, contact email
- [ ] **Apple privacy "nutrition labels" and the Google Play Data safety form.** Answer
      them from the privacy policy: diagnostics are anonymous by default, the ID is linked
      only with the in-app opt-in, message contents are end-to-end encrypted, files are
      stored on the server
- [ ] **Encryption export compliance:** the app uses standard encryption (X25519,
      ChaCha20-Poly1305, Ed25519). Set `ITSAppUsesNonExemptEncryption` in `Info.plist`
      and check whether the annual US self-classification report applies
- [ ] Screenshots (`apps/site/assets/shots/` has phone shots), icon (already generated),
      short and long descriptions, beta "what to test" text
- [ ] Review notes for Apple: how a reviewer creates an ID (claim any free name; the
      keystore needs Face ID or a passcode on the device), and a note that sign-in uses
      the device keystore, not a password

### Signing and builds (E19-T5)

- [ ] iOS: automatic signing in Xcode for the App ID, a distribution certificate, and
      archive → upload to App Store Connect. Local builds first; add CI later (macOS
      runners are the expensive ones)
- [ ] Android: Play App Signing, plus an upload keystore that you **back up** in your
      password manager (losing it means a support ticket to replace it)
- [ ] Versions: the shell version tracks the web app it embeds (`versionName` and
      `MARKETING_VERSION` = `apps/web` version; `versionCode` / build number increment
      on every upload)
- [ ] Decide and document over-the-air web updates (Capgo-style): yes or no, and why. The
      default is **no** for a key-holding app; every change ships as a store build

### Push notifications (E19-T4) — the real work

- [ ] **App:** add `@capacitor/push-notifications` (APNs on iOS, FCM on Android). Ask for
      permission after onboarding, not at launch. Register the device token, and refresh it
      when it changes
- [ ] **Credentials:** an APNs auth key (`.p8`) from the Apple developer account, and a
      Firebase project with a service account for FCM (Android; FCM can also send to iOS
      if you'd rather use one API)
- [ ] **Relay → push gateway.** Only whoever holds the APNs and FCM credentials can push to
      the official app, and users on self-hosted relays use the same app. So build a small
      **push gateway** that you run (the design Matrix uses with Sygnal):
      the app registers `{token, platform}` with its relay as part of its signed device
      record; when a message arrives for that identity, the relay posts a **content-free
      wake-up** to the gateway, and the gateway sends it on to APNs or FCM. A self-hosted
      relay can point at your gateway or run its own. For the beta, poweur.net's relay
      calling the gateway is enough
- [ ] **Payload:** v1 is a generic visible notification ("New message"), with no sender
      and no content; the app decrypts when opened. v2 is a Notification Service Extension
      (iOS) or a data message (Android) that decrypts locally and shows the sender
- [ ] **Privacy:** the relay stores push tokens; add them to the privacy policy's table,
      delete them when a device is removed, and let the user switch notifications off
- [ ] Test on real devices: a message from another ID wakes a locked phone on both
      platforms; with notifications denied, opening the app still delivers everything

### Distribution: self-invite links

- [ ] **iOS: TestFlight public link.** An external testing group with a public link lets
      anyone join (up to 10,000 testers), with no invite needed. The first build of each
      version goes through Beta App Review (usually about a day), so upload the beta build
      early
- [ ] **Android: closed testing with a Google Group as the tester list.** Anyone who joins
      the group (a public link) gets the opt-in link: that's self-invite, and it counts
      towards the 12-testers-for-14-days rule. Switch to open testing (a public Play
      listing) once the account qualifies
- [ ] Optional fallback for Android: a signed APK on poweur.org, or Firebase App
      Distribution, for people who won't join a Google Group
- [ ] A "Get the beta" section on poweur.org and in the web app's Settings, with both
      links; mention it in every pre-launch post

---

## Part 2 — The story

- [ ] Finish [`blog-why-poweur.md`](blog-why-poweur.md): fill in the personal parts marked
      `[YOU: …]`, and have two people outside the project read it
- [ ] Publish on poweur.org (add `/blog/`), with an OG image and a canonical URL. Every
      cross-post (dev.to, Hashnode, Medium, LinkedIn article) points its canonical URL back
      to it
- [ ] A 60–90 s screen recording: claim a name → message hello.poweur.net → share a folder →
      sign in to the guestbook. Post it natively on X, LinkedIn, Mastodon and Bluesky, and use
      it for Product Hunt
- [ ] Press kit page: logo files (`design/claude/lockups/`), screenshots
      (`apps/site/assets/shots/`), the product shot, a one-paragraph description, a founder
      bio and photo, contact
- [ ] Four ready-to-paste texts, written once and adapted per channel:
      one line (≤ 120 chars), short (≤ 300 chars, social), medium (a Reddit/forum post:
      problem → what it is → what's honest → the ask), and Show HN (plain, technical, no
      marketing words; the first comment is the story)
- [ ] An FAQ, answered before anyone asks: *Why not Matrix / Nostr / Bluesky (AT Protocol) /
      Solid / email?* *What happens if poweur.net disappears?* *Who holds the keys?* *How do
      you make money?* *Why do I need PRF passkeys?* *Is it audited?* This will be half of
      every thread

---

## Part 3 — Channels

Read each community's self-promotion rules before posting. Post as a person, not a brand,
and stay in the thread to answer. Never ask for upvotes; HN and Reddit penalise it.

### Phase 1a — private and personal (Oct 1–4)

- [ ] 20–30 people directly: friends, ex-colleagues, people who've complained to you about
      Big Tech lock-in. Ask each to claim a name and message you *on Poweur*
- [ ] Two or three developers you trust: "try to self-host this and break it"
- [ ] Anyone you know in identity, security, or at a relay-shaped company (hosting, email,
      password managers): ask for 20 minutes of critique, not promotion

### Phase 1b — expert communities, framed as "feedback wanted" (Oct 5–12)

**Identity / auth**
- [ ] IndieWeb chat (`#indieweb-dev`, chat.indieweb.org): the IndieAuth bridge is directly
      relevant to them. Add a page to the indieweb.org wiki once it works for their sites
- [ ] Decentralized Identity Foundation (DIF) Slack and W3C Credentials Community Group
      list: frame it around `did:web` projection and web-first identity documents
- [ ] Internet Identity Workshop (IIW): check the next date (it runs twice a year, spring and
      fall). An unconference session is the best room in the world for this
- [ ] r/webdev or r/node threads on "Sign in with…", *only* with the guestbook demo live

**Open internet / decentralisation / local-first**
- [ ] Local-first community (localfirstweb.dev Discord)
- [ ] Solid community forum (forum.solidproject.org): a close neighbour, so compare honestly
- [ ] Fediverse: an account on fosstodon.org or hachyderm.io, tagged #OpenSource #Privacy
      #SelfHosted #IndieWeb
- [ ] Bluesky: developers and "own your identity" people follow there; the domain-as-handle
      overlap is a natural hook
- [ ] NLnet / NGI community. **Apply for an NGI Zero grant** (their calls close roughly every
      two months); this project fits it almost word for word
- [ ] FOSDEM: watch for the devroom calls (typically October–December) for the
      decentralised-internet, identity and Go devrooms. A talk there is a great anchor for
      February

**Self-hosters**
- [ ] r/selfhosted: post in Phase 1b with a docker-compose snippet and the self-hosting
      guide. It is the most important audience for the "run the whole thing" story, and the
      most allergic to marketing
- [ ] selfh.st: submit to Self-Host Weekly (newsletter)
- [ ] r/homelab (lighter touch, link the r/selfhosted thread)
- [ ] Prepare app-store packages to submit *after* launch: YunoHost, Umbrel, CasaOS,
      Cloudron, Coolify templates, Unraid Community Apps
- [ ] awesome-selfhosted: read its inclusion rules (it expects a released, licensed and
      maintained project) and submit when you qualify

**Privacy**
- [ ] Privacy Guides forum (discuss.privacyguides.net): expect hard questions about metadata
      and the relay operator. Prepare an honest threat-model page first
- [ ] r/degoogle, r/privacy: only once the site loads no third-party fonts or trackers

### Phase 2 — launch week (Tue Oct 13 onward)

**Day 1 (Tuesday)**
- [ ] **Show HN** at about 8–9 am US Eastern: "Show HN: Poweur – one open ID for sign-in,
      E2E messaging and file sharing". Link the GitHub repo or the site, then post the story
      as the first comment. Keep the whole day free to reply
- [ ] **Product Hunt** on the same Tuesday (it resets at 00:01 Pacific): gallery from the
      product shot, the video, maker comment = short story. Line up a hunter or self-hunt
- [ ] Blog post live; social posts (X, LinkedIn, Mastodon, Bluesky) pointing to it
- [ ] Email everyone from Phase 1: "it's public today — thank you"

**Days 2–5**
- [ ] Lobsters (invite-only: ask someone for an invite in Phase 1), tagged `show`
- [ ] Reddit, one per day, adapted to each: r/selfhosted (launch follow-up), r/opensource,
      r/golang (the Go relay and the conformance-vector approach), r/typescript or r/javascript
      (the SDK), r/degoogle, r/privacy, r/LocalLLaMA or r/AI_Agents ("agents get IDs")
- [ ] Indie Hackers (the build story and the business model), dev.to / Hashnode
      (cross-post with canonical URLs)
- [ ] Directories: AlternativeTo (list it as an alternative to "Sign in with Google",
      WeTransfer, Google Drive, WhatsApp), OpenAlternative, SaaSHub, European Alternatives
      (european-alternatives.eu, the EU-sovereignty angle), BetaList (submit in Phase 1: it
      has a queue)

**Newsletters (submit during launch week; most take reader tips)**
- [ ] Changelog News, Golang Weekly, JavaScript Weekly / Node Weekly, Console.dev,
      TLDR, Hacker Newsletter, Self-Host Weekly, It's FOSS

**Press and podcasts (pitch launch week, with a follow-up the week after)**

Use a short personal pitch: one paragraph, the angle, the demo link, and an offer of a call.
Target the writer who covered something adjacent last month, not the newsroom inbox.

- [ ] Tech press, open source / privacy beat: The Register, Ars Technica, The Verge
      (privacy/platform writers), Wired, TechCrunch (only with a funding or traction hook)
- [ ] European outlets (the sovereignty angle is a real story in the EU right now): heise /
      c't, Golem.de, Netzpolitik.org, plus outlets in your own country
- [ ] FOSS and Linux media: It's FOSS, OMG! Ubuntu, Linux Magazine, LWN (pitch a
      technical article rather than an announcement)
- [ ] Developer media: The New Stack, InfoQ (a protocol and architecture write-up)
- [ ] Podcasts: Self-Hosted, The Changelog, Linux Unplugged, privacy-focused shows. Pitch
      "why identity is the missing primitive", not the product

### Metrics to watch (the Growth dashboard, EPIC-013)

Claims per day, % of claims that send a first message, % that share something, 7-day
return, self-host installs (GitHub clones and container pulls), sign-in bridge usage. Don't
collect filenames, contents or social graphs to get them.

---

## Part 4 — Storage limits, then a first paid plan

**At launch (done, relay 0.1.19 / web 0.1.37):**

- [x] Free storage on poweur.net is **200 MiB** (`MAX_IDENTITY_BYTES` in
      `docker-compose.prod.yml`) and is **not advertised**: the site doesn't mention it, and
      the app shows only "X used", with no total.
- [x] At 90 % the Files screen warns; an upload over the limit says "Your storage is full.
      Message `<contact>` to ask for more space, and tell us what you need it for."
- [x] Support raises one ID's limit with `docker exec poweur-relay /relay quotas set
      alice.poweur.net 2GB` (`quotas` lists, `quotas unset` removes). The overrides live in the
      relay's store (`relay/storage-quotas.json` in the bucket); no restart, picked up within a
      minute, and an invalid size is refused.
- [ ] `support.poweur.net` and `QUOTA_CONTACT`: see "P0 — operational accounts and
      contacts" in Part 1. Until it's set, a full drive just says the storage is full
- [ ] Decide what happens to IDs already over 200 MiB. They keep their files but can't
      upload more. Give the early users an override with `/relay quotas set` if needed.

**Later: a real upgrade path.** Once people ask for more space, replace "message support"
with a CTA. Follow EPIC-026's invariant ("an identity is not a billing account"):

- [ ] A merchant of record for checkout (Paddle, Lemon Squeezy or Polar), so EU VAT and
      invoices aren't your problem. Collect the Poweur ID as a checkout field. The customer's
      email stays with the payment provider, not on the relay
- [ ] Fulfilment sets the override (`/relay quotas set`): a webhook to a small script, or you,
      by hand, at first
- [ ] Offer, following EPIC-026's Personal tier: e.g. €3/month or €30/year for a lot more
      storage. Say publicly what is **never** charged for: the ID, recovery, E2E messaging,
      sign-in and export
- [ ] Where it shows: the quota warning and the storage-full message link to the plan page
      instead of the support ID
- [ ] GitHub Sponsors and/or Open Collective for people who just want to help
- [ ] Terms for paid plans: refunds, what happens when a payment fails (grace period,
      files become read-only, never deleted at once, and the ID is never revoked),
      cancellation

---

## Launch-day runbook (Oct 13)

- [ ] Freeze deploys from the evening before unless something is on fire
- [ ] Scale the server up a size for the week
- [ ] Check claim → message → share → sign-in on production, from a fresh phone, in the
      morning
- [ ] Have the Show HN text, the maker comment, the social posts and the FAQ open in tabs
- [ ] Reply to every comment within the hour for the first 12 hours; keep a list of every
      bug and feature request mentioned
- [ ] End of day: post a short "thank you + what we're fixing" update on the blog / GitHub
      Discussions
