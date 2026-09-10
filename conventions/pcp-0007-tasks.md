# PCP-0007 — Tasks & projects (`net.poweur.tasks`)

- **Status:** draft
- **Owner:** poweur core
- **Registry entries:** `net.poweur.tasks` (app-id), `net.poweur.tasks` (schema namespace)

## Problem

Every personal-data platform grows a task list, and every one of them grows its *own*
task list. The user ends up with three apps that each hold a third of their work and
cannot see the other two thirds, because the format is a private implementation detail
of whoever shipped first.

Poweur's thesis (EPIC-006) is that this is a **convention problem, not a protocol
problem**: the home directory, the identity, the sharing model and the sync journal
already exist. What is missing for the tasks domain is an agreement on *which files,
with which fields*, so that a second implementation can read the first one's data.

This PCP is also the **first exercise of the PCP process itself** (E06-T5). It was written
before the reference app and revised by what building the app found;
§[Retrospective](#retrospective-what-the-convention-process-missed) is that report, and is
the point of the exercise.

## Prior art

Deliberately derivative. Three formats have survived long enough to be worth copying:

| Format | Model | What we take | What we leave |
|--------|-------|--------------|---------------|
| [todo.txt](https://github.com/todotxt/todo.txt) | one line per task in one file | flat tasks, tags, `A`–`Z` priority, "done" is a state not a deletion | line format (not merge-friendly, no stable id) |
| iCalendar `VTODO` / [jsCalendar](https://www.rfc-editor.org/rfc/rfc8984) `Task` | one component per task, UID-keyed | stable `uid`, `title`/`description` split, `due`/`start`, `RELATED-TO` parent links, `status` vocabulary, `percentComplete` | recurrence, alarms, timezone machinery, the whole `jscalendar` object envelope |
| [Taskwarrior](https://taskwarrior.org/docs/) | JSON records in an append log | UUID identity, `project` as first-class, `tags`, `annotations`, explicit `entry`/`modified` timestamps | urgency scoring, virtual tags, the log format |

**Convertibility is a requirement, not a nice-to-have.** Every field below maps to a
`VTODO` property or is explicitly Poweur-native; §[Mapping](#mapping-to-existing-formats)
is normative for exporters.

## Convention

### Layout

Inside the app namespace (`/apps/net.poweur.tasks/`, rules per
[app-data.md](../apps/docs/docs/conventions/app-data.md)):

```
/apps/net.poweur.tasks/
  manifest.json                              app self-description (E06-T3, validated on write)
  projects/<project-id>/project.json         one project document
  projects/<project-id>/tasks/<task-id>.json one file per task
```

**One file per task** is normative, and it is the whole reason this convention is
sync-friendly: concurrent edits to different tasks are different files, so EPIC-004
resolves them with no conflict at all. A single `tasks.json` would turn every
concurrent edit into a conflicted copy. Implementations MUST NOT collapse tasks into a
shared document.

A project is a directory, so **sharing a project is an EPIC-005 share of
`apps/net.poweur.tasks/projects/<project-id>`** — there is no tasks-specific sharing
mechanism, and this PCP defines none.

Ids are opaque strings, `[A-Za-z0-9_-]{1,64}`, unique within their scope. They carry no
meaning: renaming a project's title MUST NOT change its id, and readers MUST NOT parse
ids. Reference implementation uses `prj_`/`tsk_` + 128 bits of base32.

### `project.json`

Schema: [`schemas/net.poweur.tasks/project.schema.json`](schemas/net.poweur.tasks/project.schema.json).

```json
{
  "schema_version": "1",
  "id": "prj_7k2m9q4xw1c0",
  "title": "Kitchen renovation",
  "description": "everything before the tiles arrive",
  "created": "2026-09-01T09:00:00Z",
  "updated": "2026-09-09T11:20:00Z",
  "archived": false,
  "tags": ["home"]
}
```

`schema_version`, `id`, `title` and `created` are required; everything else is optional.
`id` MUST equal the containing directory name.

There is deliberately **no member/ACL field**. Who can see a project is decided by the
share grant (PCP-0003) and enforced by the relay; a second copy of that list inside the
document would drift and imply authority it does not have. Implementations that want to
show "who is on this project" read the grant.

### `<task-id>.json`

Schema: [`schemas/net.poweur.tasks/task.schema.json`](schemas/net.poweur.tasks/task.schema.json).

```json
{
  "schema_version": "1",
  "id": "tsk_0f83be2a5d17",
  "title": "Measure the alcove",
  "status": "todo",
  "created": "2026-09-01T09:04:00Z",
  "updated": "2026-09-01T09:04:00Z",
  "notes": "tape measure is in the garage",
  "due": "2026-09-12",
  "priority": 5,
  "tags": ["measuring"],
  "parent": "tsk_9aa1c4d0",
  "assignee": "bob.example.org",
  "completed": null
}
```

| Field | Required | Type | Notes |
|-------|----------|------|-------|
| `schema_version` | yes | `"1"` | bumped only for breaking changes |
| `id` | yes | id string | MUST equal the filename without `.json` |
| `title` | yes | string, 1–1000 chars | one line; newlines belong in `notes` |
| `status` | yes | `todo` \| `doing` \| `done` \| `cancelled` | closed set; see compatibility |
| `created` | yes | RFC 3339 timestamp (UTC) | |
| `updated` | yes | RFC 3339 timestamp (UTC) | writers MUST bump on every write |
| `notes` | no | string ≤ 16 KiB | CommonMark by convention, plain text is valid |
| `due` | no | `YYYY-MM-DD` **or** RFC 3339 | date = all-day, floating (no timezone) |
| `start` | no | `YYYY-MM-DD` **or** RFC 3339 | earliest sensible action date |
| `priority` | no | integer 0–9 | jsCalendar semantics: 0 = undefined, 1 = highest, 9 = lowest |
| `tags` | no | array of ≤ 32 strings, each ≤ 64 chars | free-form labels; no leading `+`/`@` sigils |
| `parent` | no | task id | subtasks within the same project only; cycles are invalid |
| `assignee` | no | Poweur identity | the Poweur-native field — a name the platform can actually resolve |
| `completed` | no | RFC 3339 timestamp, or absent/null | MUST be set when `status` becomes `done`, cleared otherwise |

Document size cap: **64 KiB** per task file, matching the `poweur-sys` document cap.
Writers that need more are storing an attachment, not a task; attachments go elsewhere
in the namespace and are referenced from `notes`.

### Writing rules

- Writes are whole-document `PUT`s of a single task or project file. Readers list a
  project with a DAV `PROPFIND` of `tasks/`.
- `updated` is advisory metadata for humans and merge UIs, **not** a lock. Conflict
  resolution is the platform's (EPIC-004 conflicted copies), and one file per task keeps
  the blast radius to one task.
- The app namespace is private by default; nothing in this convention is signed, because
  nothing in it crosses a trust boundary on its own — it is the owner's data in the
  owner's home, and the relay authenticates every reader and writer. A task document
  found *outside* a home (mailed around, published) carries no authority; if a future
  PCP wants transferable tasks it must add a signature envelope, and that is a new PCP.

### Mapping to existing formats

Normative for exporters/importers:

| This PCP | iCalendar `VTODO` | jsCalendar `Task` | todo.txt | Taskwarrior |
|----------|-------------------|-------------------|----------|-------------|
| `id` | `UID` | `uid` | (none — synthesize) | `uuid` |
| `title` | `SUMMARY` | `title` | description text | `description` |
| `notes` | `DESCRIPTION` | `description` | — | annotations |
| `status: todo` | `STATUS:NEEDS-ACTION` | `"needs-action"` | (no `x` prefix) | `pending` |
| `status: doing` | `STATUS:IN-PROCESS` | `"in-process"` | (no `x` prefix) | `pending` + `+active` |
| `status: done` | `STATUS:COMPLETED` | `"completed"` | leading `x ` | `completed` |
| `status: cancelled` | `STATUS:CANCELLED` | `"cancelled"` | (drop or `x`) | `deleted` |
| `due` | `DUE` | `due` | `due:YYYY-MM-DD` | `due` |
| `start` | `DTSTART` | `start` | `t:YYYY-MM-DD` | `scheduled` |
| `priority` | `PRIORITY` (0–9) | `priority` | `(A)`≈1, `(C)`≈5, `(E)`≈9 | `H`≈1, `M`≈5, `L`≈9 |
| `tags` | `CATEGORIES` | `keywords` | `@context` / `+project` | `tags` |
| `parent` | `RELATED-TO;RELTYPE=PARENT` | `relatedTo` (`parent`) | — | `depends`-ish |
| `assignee` | `ATTENDEE` (partly) | `participants` | — | — |
| `completed` | `COMPLETED` | `progress`/`"completed"` timestamp | completion date | `end` |
| project directory | `VTODO` in a per-project calendar | — | `+project` tag | `project` |

Round-tripping through todo.txt loses `id`, `parent` and `assignee`; that is a property
of todo.txt, and importers MUST mint fresh ids rather than pretend.

## Compatibility

- **Unknown top-level fields MUST be preserved** on read-modify-write. This is the
  single most important rule in this document: the second implementation of a convention
  is always the one that silently deletes the first one's data. Implementations that
  cannot preserve unknown fields MUST refuse to write the document rather than drop them.
- **Unknown `status` values** MUST be treated as `todo` for display and MUST be preserved
  on write — never rewritten to a known value. (Contrast PCP-0004, where an unknown
  contact state is *invalid*: that document is schema-governed and relay-enforced;
  this one is app data the relay does not read.)
- **Unknown files** inside the namespace (an app's own index, a cache, a future
  `views/`) MUST be left alone.
- Directories under `projects/` that are missing or have an unparseable `project.json`
  are skipped, not deleted. A project directory with tasks and no `project.json` is a
  partially-synced state, not a corrupt one.
- `schema_version` is a string. A reader seeing a major version it does not know MUST
  present the document read-only rather than guess.

## Implementations

1. **`poweur-tasks`** — reference CLI, `apps/tasks/` in this repo. Talks only HTTP/DAV
   against a relay with a scoped token (`dav:rw:/apps/net.poweur.tasks/`); imports no
   Poweur internals, exactly as a third-party app would. Two-identity collaboration over
   a shared project is covered by `apps/integration/tasks_test.go`.
2. _(needed for `stable`: one independent implementation, ideally not in this repo.)_

## Retrospective: what the convention process missed

E06-T5's real deliverable. PCP-0007 was written first and implemented second, on purpose,
to find out what [PCP-0001](pcp-0001-process.md) does not ask for. What follows is what
the reference app actually hit, in the order it hurt.

### 1. The prose and the schema were allowed to contradict each other

The worst one, and it survived review.

`task.schema.json` had `"status": {"enum": ["todo","doing","done","cancelled"]}`. The prose
two sections above it says an unknown `status` MUST be treated as `todo` **and preserved
verbatim on write**. Both statements were in the same commit. They cannot both hold: a
validator built from the schema rejects exactly the documents the prose orders readers to
accept, and the reference implementation, following the prose, emitted documents that fail
its own convention's schema.

Nobody noticed because **nothing in the process compares the two**. PCP-0001's template
says "JSON shapes (link a JSON Schema)" and stops there. It never says whether the schema
or the prose is normative when they differ, and CI validated only that the schema file
*parses* — a check that a schema saying the opposite of its PCP passes comfortably.

*Fixed here:* `status` is `type: string` with the vocabulary as `examples` and the
preserve rule in its `description`; the `done` → `completed` coupling that only lived in
prose is now `if`/`then` in the schema; and `apps/tasks/pkg/tasks/schema_test.go` fails
if the published schema and the implementation drift on field sets, required lists, or
that enum coming back.

*Process change PCP-0001 needs:* say which artifact is normative — recommend **the prose
is normative and the schema is a conformance test of it** — and require every PCP with a
schema to ship a document that exercises the compatibility rules and validates.

### 2. "Unknown fields MUST be preserved" is much harder than it reads

It is one line in PCP-0001's ground rules and it is the single largest piece of the
reference implementation. A struct-based reader in any statically typed language decodes
into known fields and drops the rest — silently, correctly by the language's lights, and
undetectably until another implementation's data goes missing. Getting it right needs a
parallel `map[string]json.RawMessage`, a merge on write, and a decision about what happens
when a known and an unknown field collide (here: the known field wins).

A rule that every mainstream JSON binding violates by default cannot be left as one
sentence in a "ground rules" list. **PCP-0001 should carry a short implementer's note
naming the trap and the shape of the fix**, and every PCP that inherits the rule should
be required to state what its implementation does on collision.

### 3. Nothing said who creates the app's namespace directory

`app-data.md` tells an app to request `dav:rw:/apps/<app-id>/`, and separately that
`manifest.json` must be written first. It never says who creates `/apps/<app-id>` itself,
or that `/apps` already exists.

The reference app did the obvious thing — `MKCOL /apps`, then `MKCOL /apps/<app-id>` — and
got a flat 403 on the first call, because a token scoped to the app's namespace covers
that namespace and everything under it and *nothing above it*. The app cannot create its
own parent with the only credential the convention tells it to ask for. (`/apps` turns out
to be one of the five roots the relay provisions with the home, so it never needed
creating — but that is knowable only by reading the relay's source, which is precisely
what a convention exists to avoid.)

**A convention that describes a namespace must describe its bootstrap**: which directories
exist already, which the app creates, and with what credential.

### 4. The recipient's view of a shared namespace was never specified

PCP-0007 says sharing a project is an EPIC-005 share of `projects/<project-id>`, and stops.
What the *recipient's* app then sees was left to the imagination — including ours: the
integration test for this was written expecting the recipient to need a project id handed
over out of band, and it was wrong. EPIC-005 makes a grant's ancestor directories readable,
so an unmodified `poweur-tasks projects` against the owner's home enumerates exactly the
shared projects and no others. Discovery works, and no one had written that down.

What does not work is one level up: `manifest.json` is a **sibling** of `projects/`, not an
ancestor of the grant, so it stays 403 for the recipient. Every shared namespace therefore
presents to its recipient as a namespace with no readable manifest — which `app-data.md`
defines as "abandoned by tooling (cleanup UIs may flag it)". A convention-conformant app
looking at a legitimately shared project sees a namespace its own rules call abandoned.

Both halves are pinned by `TestINT_TASKS_04_RecipientsViewOfASharedNamespace`, which fails
loudly if the platform behaviour moves under this text.

**PCP-0001 should require a "what the other side sees" section** for any convention whose
data can be shared: which paths the recipient can read, what is missing from their view,
and what their app should do about it.

### 5. `stable` requires two implementations; the process never budgets for the second

PCP-0001 gates `stable` on "two independent implementations [that] interoperate" and then
provides nothing to interoperate *against*: no conformance vectors, no sample documents, no
"here is a document exercising every compatibility rule — your reader must round-trip it
unchanged". The repo already knows how to do this — `packages/identity/testdata/vectors/`
does exactly this for the protocol, and AGENTS.md treats regenerating those vectors as
mandatory — but the PCP process was written without borrowing it.

The second implementer's hardest problems are the compatibility rules, and those are
exactly what prose cannot pin down. **A PCP should be required to ship a `testdata/`
directory of conformance documents before it can leave `draft`.** PCP-0007 has not done
this yet; it is the largest thing still missing here, and it is deliberately called out
rather than quietly skipped.

### 6. Reserving a name is not proposing a convention

`net.poweur.tasks` sat in `registry.json` citing `"pcp": "pcp-0007-tasks"` for as long as
this task was deferred — pointing at a file nobody had written. A reservation that cites a
non-existent document looks, to anyone reading the registry, exactly like a documented
claim. CI now fails on that (`TestConventionsRegistryPCPReferencesResolve`), but the
process should also say plainly that a registry entry may cite a PCP only once the PCP
exists, and that an unattributed reservation is the honest way to hold a name.

### 7. What the process got right

Worth recording, because the failures above are cheaper than they look next to it:

- **No new platform mechanism was needed.** Two identities collaborate on a shared task
  list with zero server code, no tasks-specific endpoint and no relay change. The
  "conventions, not protocol" thesis survived contact with a real domain.
- **Relay-enforced namespace claims are the right call.** `manifest.json` validated on
  write, with `app_id` matched against its directory, means an app cannot squat another
  app's namespace even by accident (`TestINT_TASKS_03`).
- **One file per task paid for itself immediately.** Two writers, no coordination, no
  conflicts — the granularity guidance in `app-data.md` is doing real work.
- **Writing the PCP before the implementation is what surfaced all of the above.** A PCP
  written after the code would have documented whatever the code happened to do, and every
  one of these gaps would have been invisible.
