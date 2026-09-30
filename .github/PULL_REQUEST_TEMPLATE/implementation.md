## Implementation: what and why

<!-- One task per PR where you can. Title: `E02-T2: short description`. -->

Epic task: <!-- for example E02-T2 -->
Spec PR: <!-- link to the merged PR that defined this task, or the epic file if it predates this process -->

## Checklist

- [ ] Matches the spec; any deviation is written back to the epic in this PR
- [ ] Tests added or updated (unit; `apps/integration` for relay / CLI protocol changes)
- [ ] Protocol changes are documented in `apps/docs/docs/` and the conformance vectors regenerated (`pnpm vectors`)
- [ ] Epic checkboxes and Progress table updated
- [ ] Patch version bumped for each package whose shipped behaviour changed (see `AGENTS.md`)
- [ ] Commits are signed off (`git commit -s`, [DCO](https://developercertificate.org/))
- [ ] No secrets, keys or personal data in the diff
