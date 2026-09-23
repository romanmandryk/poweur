# Claude / Cursor agent entrypoint

Follow **[`AGENTS.md`](AGENTS.md)** for all implementation work in this repository.

In particular:

- Unit-test new code.
- Use `apps/integration` (real local relays) for relay/CLI protocol changes.
- Keep [`epics/`](epics/) task checkboxes and Progress tables up to date.
- When a change implements or fixes the relay, Go CLI, TS SDK, web app, mobile shell or OAuth bridge, bump that package's **patch** version in the same change set (`package.json`, `internal/buildinfo.Version`, or `bridge.Version` in `apps/oauth/bridge/doc.go` — not `go.mod`). Only the packages whose shipped behaviour changed: test-only, config-only and docs-only changes keep the current version. See **Version bumps** in [`AGENTS.md`](AGENTS.md).
