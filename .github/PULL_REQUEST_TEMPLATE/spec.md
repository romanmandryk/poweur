## Spec: what is being specified

<!-- Epic and task(s) this defines or changes, for example EPIC-020 / E20-T14. A new epic is fine. -->

Epic / task:

## Why

<!-- The problem, who it affects, and what is deliberately out of scope. -->

## Checklist

- [ ] Only markdown changes: `epics/` and, for protocol changes, `apps/docs/docs/`. No code, no version bumps
- [ ] Each task has an ID, a goal, acceptance criteria and dependencies
- [ ] Epic **Progress** table has a row for every new task (status `open`)
- [ ] `epics/README.md` index updated if an epic was added or renamed
- [ ] Deferred or open work is named, with a follow-up epic where one exists
- [ ] Protocol changes list the conformance vectors that must be regenerated (`pnpm vectors`)
- [ ] Commits are signed off (`git commit -s`, [DCO](https://developercertificate.org/))
- [ ] No secrets, keys, private infrastructure details or pricing in the diff

<!-- Merging this PR is the approval to implement. Implementation PRs link back to it. -->
