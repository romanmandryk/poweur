---
id: mls-adoption
sidebar_position: 2
title: MLS adoption study (group messaging v2)
---

# MLS adoption study — group messaging v2

Group messaging v1 is [server fan-out with per-member encryption](../protocol/group-messaging.md):
the sender seals the payload once per member and the group's relay delivers each envelope. It
is O(N) per message and it has no post-compromise security beyond what 1:1 already has.

This page is the study of the thing that fixes both — **MLS, the Messaging Layer Security
protocol, RFC 9420** — and the honest account of what adopting it would cost. It is a decision
record, not a plan. Nothing here is scheduled.

## What MLS is, in one paragraph

MLS gives a group a **shared, continuously-updated secret**. Members are leaves of a binary
tree of key pairs; each member knows the private keys on the path from their leaf to the root,
and the root secret keys the group. Sending is one ciphertext for the whole group, whatever N
is. Any member can *update* their leaf, which re-keys every node on their path and therefore
the root — so the group's key moves forward continuously and independently of message traffic.
Adds and removes are the same operation applied to the tree, so membership changes are key
changes by construction. The result is a group protocol that is O(log N) to change and O(1) to
send, with formally analysed security properties.

## What we would inherit

### 1. Post-compromise security

This is the property v1 does not have and cannot get. Today, an attacker who steals a member's
long-term X25519 encryption key can read every group message sealed to it from then on, and
nothing that member does short of publishing a new identity key ends that. Under MLS, the
compromised member performs an Update — one commit — and the attacker is locked out of every
subsequent epoch, because the root secret has moved through a path they do not know.

Poweur has no story for "my laptop was stolen last month" in group messaging beyond identity
rotation, which is a heavier, more visible, more disruptive act. PCS is the single strongest
argument for MLS and the one worth adopting for even if scale never becomes a problem.

### 2. Forward secrecy that is real rather than incidental

v1 calls late-join forward secrecy "by construction", and it is: a new member cannot read old
messages because no envelope exists that they can open. But that is a property of *fan-out*,
not of key evolution. A member who was present the whole time can still decrypt every message
they ever received if their key is later compromised. MLS deletes key material as epochs
advance, so past traffic stops being decryptable even by a legitimate member's own future key.

### 3. Scale

O(1) send instead of O(N). The 100-member cap in v1 exists because sender-side cost, uplink
bandwidth and the group relay's fan-out loop all scale linearly. MLS makes a 5-member group and
a 5,000-member group the same shape of work for the sender, and reduces the relay to what it is
best at: moving one opaque blob to many recipients.

### 4. Interoperability and review

MLS is an IETF standard with independent implementations (OpenMLS, mlspp) and published formal
analysis. Adopting it means the group crypto in Poweur is reviewed by people who are not us —
the same reason the protocol uses X25519 and ChaCha20-Poly1305 rather than something homegrown.

## What it demands

### 1. Group state that has to converge

This is the real cost, and it is not a crypto cost. MLS group state is a **replicated data
structure**: every member holds a copy of the ratchet tree, and every Commit moves every copy
forward by exactly one epoch. That requires:

- **A total order on commits.** Two admins adding a member at the same moment produce two
  commits for epoch N. Exactly one may win, and every member must agree on which. Someone has
  to serialize them. In practice that someone is the **Delivery Service**, and in Poweur that
  means the group's relay acquires a job it does not currently have: ordering, not just
  delivering.
- **State that survives everywhere the identity does.** Poweur identities are multi-device by
  design (E09-T1 archives, keystore enrollment). Under MLS every device is either its own leaf
  — multiplying N — or devices share leaf secrets, which means group state has to sync across
  a user's devices with the same durability guarantees as their key material. Neither is free,
  and the second interacts with enrollment in ways that need their own design.
- **Recovery when a client falls behind.** A device offline for a month wakes up many epochs
  stale. MLS's answer is a Welcome message / external commit and re-joining the group at the
  current epoch — which, in Poweur terms, means the same late-join semantics v1 already has,
  arrived at by a much more expensive route.

v1's whole appeal is that it has **no** group state. The membership document is the state, it
lives in one place, it is signed, and it is re-read per request. Trading that for a replicated
tree is the decision, and it should be made for PCS, not for tidiness.

### 2. A Delivery Service and a Key Package directory

MLS assumes two services Poweur does not have:

- A **Delivery Service** that fans out and orders handshake messages. Poweur's group relay is
  most of the fan-out half already; the ordering half is new, and it makes the group's relay
  authoritative in a way it currently is not. A relay that reorders or withholds commits can
  stall a group (it cannot read it), which is a new availability-shaped power.
- An **Authentication Service** supplying **KeyPackages** — pre-published, signed, one-time-use
  init keys per device, so a member can be added while offline. Poweur has the signing half
  (identity documents, `/.well-known/poweur/id.json`) but publishes one long-lived encryption
  key, not a consumable pool. KeyPackage publication, replenishment and exhaustion is a whole
  subsystem, and "exhausted, so you cannot be added to a group right now" is a user-visible
  failure mode we do not have today.

### 3. Credentials that bind to Poweur identities

MLS leaves carry Credentials. The natural fit is a Poweur credential type whose signature key
is the identity's Ed25519 key and whose validation is the existing web-first resolution with
key pinning. That is a small, well-shaped piece of work — but it also means **identity key
rotation has to become an MLS operation**, because rotating an identity key invalidates every
leaf credential that names it. Rotation currently touches an identity document; under MLS it
touches every group the identity is in.

### 4. Size and cost per operation

MLS handshake messages are larger than a membership diff, and a Commit is O(log N) ciphertexts
of path secrets. For a 5-member group, MLS is *more* bytes on the wire than v1, not fewer.
The crossover is somewhere in the tens of members, which is the same region as the v1 cap —
which is a comforting sign that the cap was set in roughly the right place.

## Explicit criteria for switching

Adopt MLS when **any one** of these is true. Until then, the v1 fan-out stays, and this page
gets revisited rather than the code.

1. **Scale.** A real group needs more than the v1 cap of **100 members**. This one is enforced:
   `413 group_too_large` is the code path that says "read this page".
2. **Volume.** A real group sustains more than roughly **10 messages per minute**, where O(N)
   per message becomes O(N × rate) on the sender's uplink and the relay's fan-out loop.
3. **Post-compromise security becomes a requirement.** A user, an audit, or a compliance
   commitment asks for "a stolen device stops being able to read group messages after the
   member re-keys". v1 cannot answer this at any scale, so **this criterion alone justifies
   adoption in a five-member group** — it is not a scale argument and must not be filed as one.
4. **Interop.** Poweur needs to exchange group messages with an MLS deployment, at which point
   implementing MLS is cheaper than bridging it.

And two conditions that must hold *before* adoption, whichever criterion triggers it:

- **Multi-device group state has a design.** Devices-as-leaves or shared-leaf sync, decided and
  written down, including what happens on enrollment and on device removal. Adopting MLS without
  this converts a solved problem (multi-device 1:1) into an open one.
- **The ordering authority is written into the trust model.** The group relay's new power to
  order and withhold commits is a change to [the security model](../security/model.md), and it
  should be argued there before it is coded.

## The intermediate step we would probably skip

**Sender keys** — each member has one symmetric chain key distributed to the others over
pairwise channels; sending becomes O(1) and adding a member becomes O(N) — is the usual stop
between fan-out and MLS. It fixes the volume criterion and none of the others.

We would likely skip it. It introduces group state (the chains) and their distribution, which
is most of MLS's coordination cost, while delivering none of MLS's PCS and none of its review.
If criterion 2 fires alone, with no PCS pressure, sender keys become worth a second look —
otherwise the right move is to go from v1 to MLS directly.

## If we did it: rough shape

Not a plan, but the order the pieces would have to land in.

1. **KeyPackage publication.** A `.poweur/` document holding a pool of signed init keys per
   device, with replenishment on use. Independently useful; no group semantics yet.
2. **A Poweur MLS credential.** Leaf credentials whose signature key is the identity key,
   validated by existing resolution and pinning. Decide rotation's interaction here.
3. **The relay as Delivery Service.** Ordering and fan-out of handshake messages for a group
   identity, with the trust-model change argued first.
4. **Group state storage and multi-device sync.** The hard one, and the reason this is a study.
5. **`chat.text` over MLS as a second transport**, addressed by the same group identity, chosen
   per group so v1 groups keep working. The membership document stays what it is; the epoch it
   already carries maps onto the MLS epoch rather than competing with it.

Step 5 is worth noting: because a group is an *identity* and its membership document already
carries a monotonic `epoch`, v1's addressing survives adoption intact. MLS would change how a
group message is encrypted and delivered. It would not change what a group is, or how you
write its name.

## References

- RFC 9420 — The Messaging Layer Security (MLS) Protocol
- RFC 9750 — The MLS Architecture
- [Group messaging v1](../protocol/group-messaging.md) — what this would replace
- [Group identities](../files/group-identities.md) — the membership model that survives it
