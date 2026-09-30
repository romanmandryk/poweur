## Bugfix

Fixes #<!-- issue number; open an issue first if there is none -->

## Root cause

<!-- What was actually wrong, not just what you changed. -->

## Checklist

- [ ] Linked to an issue with `Fixes #n`
- [ ] A regression test fails without the fix and passes with it (unit; `apps/integration` if it involves relay / CLI behaviour)
- [ ] If the fix exposed a spec gap, the epic or `apps/docs/docs/` is corrected in this PR (or a spec PR is linked)
- [ ] Patch version bumped for each package whose shipped behaviour changed (see `AGENTS.md`)
- [ ] Commits are signed off (`git commit -s`, [DCO](https://developercertificate.org/))
- [ ] No secrets, keys or personal data in the diff
