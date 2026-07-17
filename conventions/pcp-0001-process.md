# PCP-0001 — The Poweur Convention Proposal process

- **Status:** experimental
- **Owner:** poweur core

## Statuses

`draft` → `experimental` → `stable` (or `withdrawn`).

- **draft** — proposed in a PR; format may change freely.
- **experimental** — at least one shipped implementation; breaking changes need a
  migration note in the PCP.
- **stable** — **two independent implementations** interoperate; breaking changes require
  a new PCP that supersedes this one.

## How to propose

1. Copy the template below into `conventions/pcp-XXXX-<slug>.md` (next free number).
2. Claim any names (app-ids, `sys.*` message types, schema namespaces) in
   `registry.json` in the same PR — first-come, subject to review.
3. Open a PR; discussion happens on the PR (GitHub Discussions once enabled).
4. Two maintainer approvals merge a `draft`; status advances by follow-up PRs that
   link the implementations.

## Template

```markdown
# PCP-XXXX — <title>
- **Status:** draft
- **Owner:** <you / org>
- **Registry entries:** <names claimed>

## Problem
## Convention
<file paths, JSON shapes (link a JSON Schema), signing rules, size limits>
## Compatibility
<forward-compat behavior; what unknown fields/files mean>
## Implementations
<links once they exist>
```

## Ground rules for all conventions

- Plain JSON + files; no RDF/Linked-Data requirements (the Solid lesson).
- Unknown files and unknown JSON fields MUST be preserved, never deleted or rejected —
  except where a PCP explicitly schema-governs a path (then validation applies on write).
- Prefer many small files over one big mutable file (sync- and conflict-friendly).
- Formats that cross a trust boundary are signed; canonical strings live in
  `packages/identity` so every implementation agrees byte-for-byte.
