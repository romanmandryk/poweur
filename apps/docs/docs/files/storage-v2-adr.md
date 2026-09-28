---
id: storage-v2-adr
title: Storage v2 architecture decision
---

# ADR: one encrypted drive, no WebDAV relay

Accepted for implementation in EPIC-020. This decision supersedes the v1 storage,
sync and path-grant designs. The normative target and current implementation
boundary are in [Storage v2](storage-v2.md).

WebDAV and fixed roots made encrypted node identities and atomic appends awkward.
Path grants changed meaning under moves. Whole-file writes lost concurrent edits.
A separate relay database would create two sources of truth. Replace them with
node grants, signed immutable versions and a durable journal in the same provider
as chunks. Memory caches are reconstructible; snapshots are optional accelerators.

Use JSON for inspectability and Go/TypeScript interoperability, explicit canonical
signing bytes for authentication, 1024-entry immutable chunk pages, 30-day version
retention and 4 KiB size-padding buckets. Fixed-size chunks avoid CDC complexity
and content-dependent boundary leakage. Clients perform merges; append files are
ordered event logs, not a relay CRDT interpreter.

Reuse the existing ephemeral-X25519 message sealing construction with independent
HKDF domains and context binding. Keep message bytes unchanged. Introducing age
or HPKE would add a second envelope dependency without replacing any missing
primitive. Adopt `golang.org/x/crypto` (BSD-3-Clause) and the existing noble crypto
libraries (MIT) for primitives. Use XChaCha20-Poly1305 for chunks and argon2id for
password links. S3 uses minio-go (Apache-2.0); browsers use fetch and presigned URLs.
Dependency versions and their actual installed licences must be checked when the
provider/link implementation lands.

No relay-generated private key is a migration shortcut: that would let the relay
read content. Because v1 user files are not in production use, delete them and
migrate only the explicitly enumerated plaintext system documents. Deploy only
after the restored baseline and an operator migration rehearsal pass.

Consequences: mount/sync adapters and the Files UI must be rebuilt; old DAV clients
stop working. Storage remains bounded by provider durability and single-process
ownership until leases ship. Encryption does not solve rollback, forked views,
metadata exposure or compromise of an unlocked device. These limits are documented
in the threat model and must remain visible in product/privacy claims.
