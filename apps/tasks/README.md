# `poweur-tasks` — reference implementation of PCP-0007

The dogfood app for [E06-T5](../../epics/EPIC-006-poweur-sys-conventions.md): a task
manager whose entire storage layer is
[PCP-0007](../../conventions/pcp-0007-tasks.md) under `/apps/net.poweur.tasks/`.

It exists to answer one question — *is the convention as written actually
implementable by someone who did not write it?* — so it is held to what an
outside author has:

- **its own Go module, depending on nothing.** Not `github.com/poweur/identity`,
  not the CLI's internals, not a third-party HTTP or WebDAV library. Only the
  standard library, the published PCP, and a scoped token;
- **no server code.** Two identities collaborate through EPIC-005 sharing and
  EPIC-004 sync alone.

Everything it learned doing that is written up in PCP-0007's
[retrospective](../../conventions/pcp-0007-tasks.md#retrospective-what-the-convention-process-missed).

## Use

```bash
go build -o poweur-tasks ./cmd/poweur-tasks

# the user mints a token scoped to exactly this app's namespace
poweur dav token --scope dav:rw:/apps/net.poweur.tasks/ --json

export POWEUR_TASKS_RELAY=https://relay.poweur.net
export POWEUR_TASKS_OWNER=alice.poweur.net
export POWEUR_TASKS_TOKEN=<token>

./poweur-tasks init
./poweur-tasks project new "Kitchen renovation"
./poweur-tasks add prj_… "Measure the alcove"
./poweur-tasks set prj_… tsk_… priority=1 due=2026-09-12 assignee=bob.poweur.net
./poweur-tasks list prj_…
./poweur-tasks done prj_… tsk_…
```

To work on a project someone shared with you, point `--owner` at *them* and use a
token whose audience is their home — a grant is read through the owner's tree, it
is not copied into yours:

```bash
poweur share add /apps/net.poweur.tasks/projects/prj_… --with bob.poweur.net --perm rw   # alice
poweur dav token --audience alice.poweur.net --json                                      # bob
POWEUR_TASKS_OWNER=alice.poweur.net ./poweur-tasks list prj_…                            # bob
```

## Layout

| File | What it is |
|------|------------|
| `pkg/tasks/model.go` | the PCP-0007 documents: parse, validate, unknown-field preservation, ids |
| `pkg/tasks/dav.go` | the smallest WebDAV client that carries the convention (GET/PUT/DELETE/MKCOL/PROPFIND) |
| `pkg/tasks/store.go` | the layout: namespace → projects → tasks |
| `pkg/tasks/cli.go` | commands; `Run()` is the single entry point the binary and the tests share |
| `cmd/poweur-tasks` | the binary |

## Tests

```bash
cd apps/tasks && go test ./...     # unit: documents, path sanitization, DAV, CLI
cd apps/integration && go test -run TestINT_TASKS -count=1 -v
```

The unit tests run against an in-memory fake of the relay's DAV endpoint that
reproduces its scope enforcement and its `manifest.json` validation. The real
endpoint, real sharing and two real identities are covered by
[`apps/integration/tasks_test.go`](../integration/tasks_test.go).
