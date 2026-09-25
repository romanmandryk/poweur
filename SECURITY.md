# Security policy

Please report vulnerabilities privately, not in a public issue: email
**info@poweur.org** with the details and, if you can, steps to reproduce.

We'll acknowledge the report within a few days, keep you updated, and credit you once it's
fixed if you'd like. Good-faith research that avoids other people's data and doesn't disrupt
the service is welcome, and we won't pursue it legally. The full policy is at
<https://poweur.org/legal/#security>.

Poweur is pre-1.0 and has **not** had an independent security audit yet (EPIC-011 E11-T7).
The design is documented under [`apps/docs/docs/security/`](apps/docs/docs/security/).

In scope: the relay (`apps/api`), the OAuth/OIDC bridge (`apps/oauth`), the web app
(`apps/web`), the mobile shell (`apps/mobile`), the CLIs, `@poweur/client`,
`packages/identity`, and the hosted service at poweur.net and oauth.poweur.org. A relay run by
someone else is theirs to fix, but tell us if the bug is in our code.
