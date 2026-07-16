# EPIC-006 — `/poweur-sys` layout & application data conventions

- **Status:** proposed
- **Priority:** P1
- **Depends on:** EPIC-003
- **Unlocks:** EPIC-007 (contacts file), EPIC-010 (app ecosystem), every third-party app

## Goal

Specify the **system folder** (`/poweur-sys`) that makes an identity's home self-describing, and
establish the **convention process** by which applications store and share data in user homes —
starting as simple documented conventions, evolving into a protocol as multiple apps converge
per domain (tasks, calendars, social, food delivery, …). The thesis: interoperability emerges
from a shared filesystem + shared identity + published conventions, the way email emerged from
RFC 822 — apps start with their own formats, then domains standardize.

## Background

- EPIC-002/003 already *use* `poweur-sys/public/id.json` (identity document) and
  `poweur-sys/private/` (app passwords, shares, groups, devices). This epic is where those
  ad-hoc decisions get a single normative home.
- Prior art to lean on instead of inventing: **XDG base dirs** (per-app namespacing),
  **`.well-known` URI registry** (IANA-style registries), **vCard/jCard** (contacts),
  **iCalendar/jsCalendar** (events), **JSON Schema** (validation), **Solid Pods** (the closest
  philosophical neighbor — user-owned data + app access; study what made adoption hard:
  heavyweight RDF/Linked-Data requirements, which we deliberately avoid in favor of plain
  JSON + files).

## Design direction

```
/poweur-sys/
  public/
    id.json            identity document (EPIC-001)          [world]
    profile.json       display name, avatar ref, bio          [world]
    capabilities.json  supported features + endpoints         [world]
  private/
    contacts.json      contact list (EPIC-007)                [owner + relay]
    inbox-policy.json  message acceptance rules (EPIC-007)    [owner + relay]
    devices.json       device registry (EPIC-004)             [owner + relay]
    shares/…           grant documents (EPIC-005)             [owner + relay]
    groups/…           group documents (EPIC-005)             [owner + relay]
    app-passwords.json credentials (EPIC-003)                 [owner + relay]
    logs/…             access/audit logs                      [owner + relay]
/apps/<app-id>/        app data; <app-id> is reverse-DNS of the app vendor
                       (e.g. /apps/net.poweur.tasks/), default ACL private,
                       shareable per EPIC-005 like any other path
```

Conventions are versioned markdown specs + JSON Schemas in-repo (`conventions/`), published on
the docs site, with a lightweight RFC process (**PCP — Poweur Convention Proposal**).

## Tasks

### E06-T1 — Normative `/poweur-sys` specification

- [ ] Spec `apps/docs/docs/conventions/poweur-sys.md`: every file above with schema, audience,
      writer (owner / relay / both), size limits, and forward-compat rules (unknown files MUST
      be preserved by relays and sync clients, never deleted)
- [ ] JSON Schemas in `conventions/schemas/poweur-sys/*.schema.json`, wired into relay
      validation (reject malformed writes to `poweur-sys` with clear errors; other paths are
      schema-free by design)
- [ ] Bootstrap rule: what the relay creates at registration (minimal `public/id.json`,
      empty `private/`), and how existing EPIC-002–005 code migrates to the normative paths
- [ ] Protection rules: `poweur-sys/public` writable only by owner; relay-written files
      (devices.json last_seen, logs) clearly marked; sync-client behavior for relay-written
      files (pull-only)

**Acceptance:** spec + schemas merged; relay validates on write; integration test rejects a
malformed `contacts.json` PUT.

### E06-T2 — `profile.json` and `capabilities.json`

The human-facing and machine-facing "who am I" files.

- [ ] `profile.json` schema: display_name, avatar (path into `/public`), bio, links, locale;
      rendered by web client on contact views
- [ ] `capabilities.json` schema: supported protocol features + endpoint hints
      (messaging version, files/DAV, sync, sign-in) — supersedes the `_poweur-caps` TXT
      sketch in `apps/docs/docs/future/capabilities.md` for web-resolved identities; keep TXT
      record as the DNS-equivalent
- [ ] Resolver library (E01-T3) optionally fetches capabilities; CLI `poweur lookup` shows them

**Acceptance:** web app shows a contact's profile card resolved purely from their home; CLI
prints capabilities.

### E06-T3 — `/apps/<app-id>` namespace rules

- [ ] Spec `apps/docs/docs/conventions/app-data.md`: reverse-DNS app ids, layout freedom inside
      the namespace, a mandatory `manifest.json` at the root (app name, vendor, schema-version,
      docs URL), default-private ACL, how apps request scoped access (token scopes from
      E03-T3: `dav:rw:/apps/net.poweur.tasks/`)
- [ ] Data-portability guidance: prefer open formats (JSON/NDJSON/markdown/CSV); document the
      conflicted-copy behavior (EPIC-004) so app authors design merge-friendly file granularity
      (many small files > one big mutable file)
- [ ] Shared app data pattern: app data shared between users is just an EPIC-005 share of an
      `/apps/...` subtree — write the canonical worked example (a shared task-project folder)

**Acceptance:** spec merged; example manifest validated by schema; worked example doubles as an
integration test scenario.

### E06-T4 — PCP process: how conventions become standards

- [ ] `conventions/README.md` + `conventions/pcp-0001-process.md`: proposal template, statuses
      (draft → experimental → stable), numbering, where discussion happens (GitHub
      discussions/PRs), criteria for stable (≥2 independent implementations)
- [ ] Registry file `conventions/registry.json`: claimed app-ids, claimed `sys.*` message
      types, claimed schema namespaces — merged via PR, conflict = first-come + review
- [ ] Seed PCPs from this roadmap's formats: identity document, grant document, contacts,
      devices, sync journal record

**Acceptance:** process docs merged; at least the seed PCPs filed; registry validated in CI.

### E06-T5 — Reference convention: tasks domain (dogfood)

Prove the "apps converge on domain conventions" thesis with one real domain end-to-end.

- [ ] PCP draft `pcp-XXXX-tasks.md`: minimal task/project JSON format informed by existing
      open formats (todo.txt, iCalendar VTODO/jsCalendar Task — pick fields, stay convertible)
- [ ] Tiny reference app (CLI `poweur-tasks` or a static web app) that reads/writes
      `/apps/net.poweur.tasks/`, shares a project with another identity, and shows live
      collaboration through sync
- [ ] Write the retrospective into the PCP: what the convention process missed

**Acceptance:** two identities collaborate on a shared task list using only the conventions +
platform primitives (no custom server code) — this is the ecosystem's hello-world demo.
