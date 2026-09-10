# Claude / Cursor agent entrypoint

Follow **[`AGENTS.md`](AGENTS.md)** for all implementation work in this repository.

In particular:

- Unit-test new code.
- Use `apps/integration` (real local relays) for relay/CLI protocol changes.
- Keep [`epics/`](epics/) task checkboxes and Progress tables up to date.
- When a change implements or fixes the relay, Go CLI, TS SDK, web app or mobile shell, bump that package's **patch** version in the same change set (`package.json` or `internal/buildinfo.Version` — not `go.mod`). Test-only and config-only changes keep the current version. See **Version bumps** in [`AGENTS.md`](AGENTS.md).
