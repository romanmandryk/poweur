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

This PCP is also the **first exercise of the PCP process itself** (E06-T5); a
retrospective on the process lands in this document once the reference app is built.

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
