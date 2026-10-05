# Releasing

What ships, and what starts each release. Everything goes through a pull request first; a
release is then one command from an up-to-date `master`.

| Artifact | Where it ships | What starts it |
|----------|----------------|----------------|
| Relay (`apps/api`) | image `ghcr.io/<owner>/poweur-relay`, deployed to the relay host | merge to `master` (Deploy workflow) |
| OAuth bridge (`apps/oauth`) | image `ghcr.io/<owner>/poweur-oauth`, deployed to the bridge host | merge to `master` (Deploy OAuth workflow) |
| Web app (`apps/web`) | static site | merge to `master` (Deploy web workflow) |
| `@poweur/client` and `poweur` (npm) | npmjs.com, with provenance | push tag `sdk-v<version>` |
| Go CLI (`apps/cli`) | GitHub release: archives for Linux, macOS, Windows (amd64, arm64) and `checksums.txt` | push tag `cli-v<version>` |

## Every change

Bump the patch version of each package whose shipped code you changed, in the same pull
request ([AGENTS.md → Version bumps](AGENTS.md#version-bumps-required)):

```bash
pnpm release:bump sdk        # or cli, relay, web, oauth; add minor, major or x.y.z
```

The **Version bumps** CI check fails the pull request if shipped code changed and the version
did not. Tests, fixtures, docs and lockfiles do not count. If behaviour really is unchanged,
add the `no-version-bump` label.

## Releasing the SDK or the CLI

1. The pull request with the code and its version bump is merged.
2. From a clean checkout of `master`:

   ```bash
   git pull
   pnpm release:status          # file versions next to the latest tags
   pnpm release:tag sdk --dry-run  # checks everything, creates nothing
   pnpm release:tag sdk         # pushes sdk-v<version>; use cli for the CLI
   ```

   The command refuses unless you are on `master`, the tree is clean, `master` equals
   `origin/master`, and the tag does not exist yet.
3. Watch the run: `gh run list --workflow release-npm.yml` (or `release-cli.yml`). Each
   workflow re-checks that the tag matches the version in the files, tests, builds, publishes
   and creates the GitHub release.

A release that was not published (for example a missing token) is retried by deleting the
tag (`git push --delete origin sdk-v0.2.13 && git tag -d sdk-v0.2.13`) and tagging again.
Never reuse a version npm already accepted: bump it.

Both workflows can be tried without publishing: Actions → *Release npm packages* or
*Release CLI* → Run workflow, with **Dry run** ticked (the default).

## One-time setup

- **npm:** the `@poweur` organization on npmjs.com and a granular access token with
  read/write on `@poweur/client` and `poweur`, stored as the repository secret `NPM_TOKEN`
  (`gh secret set NPM_TOKEN`). Rotate it before it expires.
- **`poweur` alias:** npm blocks the unscoped name as too similar to `bower` until its support approves it. Until then the release publishes only `@poweur/client`. After approval, set the repository variable `PUBLISH_POWEUR_ALIAS` to `true` (`gh variable set PUBLISH_POWEUR_ALIAS --body true`) and rerun the release.
- **CLI:** nothing. The release uses the workflow's own `GITHUB_TOKEN`.
- **Label:** `no-version-bump` (created once with `gh label create no-version-bump`).

## Not done yet

- A Homebrew tap and a Scoop bucket for the CLI. They need their own repositories; the
  GoReleaser config would gain a `brews` section.
- Signing the CLI archives (cosign) and SBOMs.
