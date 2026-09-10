package identity

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Group messaging rules (EPIC-009 E09-T5), canonical Go.
//
// v1 is server fan-out with per-member encryption: the sender seals the
// payload once per member and posts one batch of ordinary envelopes to the
// relay hosting the group identity, which expands the membership and
// delivers. See apps/docs/docs/protocol/group-messaging.md.
//
// # Why there is no new envelope field here
//
// Group provenance rides in two reserved `metadata` keys rather than in new
// envelope fields, so CanonicalMessageEnvelope is untouched: a group message
// is byte-for-byte a message every existing implementation already knows how
// to verify. `metadata` is signed, plaintext, and already documented as
// addressing rather than content (msgtypes.go) — which is exactly what a
// group name is.

const (
	// MetaGroup names the group identity a fan-out envelope belongs to.
	MetaGroup = "group"
	// MetaGroupEpoch is the membership epoch the sender built the batch
	// against, decimal. It is a precondition, not a hint: the relay refuses
	// a batch whose epoch is not the group's current one, which is what
	// makes "removed at 14:00" mean removed.
	MetaGroupEpoch = "epoch"
)

// MaxGroupFanoutMembers caps how large a group v1 will fan out to.
//
// The number is the design's own revisit threshold made enforceable: past
// it, the O(N)-per-message shape stops being a detail and becomes the
// design, and the answer is MLS rather than a bigger loop. Refusing here
// means the limit cannot quietly rot into "whatever the timeouts allow".
// See apps/docs/docs/future/mls-adoption.md.
const MaxGroupFanoutMembers = 100

// GroupThreadSeparator joins a group ID to a sub-thread suffix.
//
// `:` rather than `/` because `/` is not a legal thread_id character
// (ValidateThreadID), and the group ID's own dots keep the two halves
// unambiguous either way.
const GroupThreadSeparator = ":"

// GroupThreadID returns the thread_id for a message to group.
//
// An empty suffix yields the group's own ID: the main conversation. Every
// member derives that value from the address alone, so the group's default
// thread is the same on every client with nobody having to agree on an
// identifier.
func GroupThreadID(group, suffix string) string {
	group = strings.ToLower(strings.TrimSpace(group))
	suffix = strings.TrimSpace(suffix)
	if suffix == "" {
		return group
	}
	// A caller that already passed the full thread_id gets it back
	// unchanged, so `--thread` accepts either form.
	if strings.EqualFold(suffix, group) {
		return group
	}
	if strings.HasPrefix(strings.ToLower(suffix), group+GroupThreadSeparator) {
		return strings.ToLower(group) + GroupThreadSeparator + suffix[len(group)+1:]
	}
	return group + GroupThreadSeparator + suffix
}

// ValidateGroupThreadID checks that threadID belongs to group.
//
// The prefix rule is what makes thread_id alone answer "which group is
// this?". Without it a group thread and a 1:1 thread could collide, and a
// client would have to open a message to find out which conversation it is
// part of.
func ValidateGroupThreadID(group, threadID string) error {
	group = strings.ToLower(strings.TrimSpace(group))
	if group == "" {
		return fmt.Errorf("group is required")
	}
	if err := ValidateThreadID(threadID); err != nil {
		return err
	}
	if threadID == "" {
		return fmt.Errorf("a group message must carry a thread_id (default: the group's own ID)")
	}
	lower := strings.ToLower(threadID)
	if lower == group {
		return nil
	}
	if strings.HasPrefix(lower, group+GroupThreadSeparator) && len(lower) > len(group)+1 {
		return nil
	}
	return fmt.Errorf("thread_id %q does not belong to group %s (want %s or %s%s<suffix>)",
		threadID, group, group, group, GroupThreadSeparator)
}

// GroupMessageMetadata returns the reserved metadata for one fan-out,
// merged over the caller's own keys.
//
// The caller's map is copied rather than mutated: it is usually the flags a
// user typed, and a send that fails should not have rewritten them.
func GroupMessageMetadata(group string, epoch int, extra map[string]string) map[string]string {
	out := make(map[string]string, len(extra)+2)
	for k, v := range extra {
		out[k] = v
	}
	out[MetaGroup] = strings.ToLower(strings.TrimSpace(group))
	out[MetaGroupEpoch] = strconv.Itoa(epoch)
	return out
}

// GroupOfMessage returns the group a message's metadata claims, and whether
// it claims one at all. Recipients file an inbound message under this rather
// than under its sender, so all members' messages land in one conversation.
func GroupOfMessage(md map[string]string) (string, bool) {
	if len(md) == 0 {
		return "", false
	}
	group := strings.ToLower(strings.TrimSpace(md[MetaGroup]))
	if group == "" {
		return "", false
	}
	return group, true
}

// GroupEpochOfMessage returns the epoch a message's metadata claims.
func GroupEpochOfMessage(md map[string]string) (int, bool) {
	if len(md) == 0 {
		return 0, false
	}
	raw := strings.TrimSpace(md[MetaGroupEpoch])
	if raw == "" {
		return 0, false
	}
	epoch, err := strconv.Atoi(raw)
	if err != nil || epoch < 0 {
		return 0, false
	}
	return epoch, true
}

// ValidateGroupMessageMetadata checks the reserved keys on a fan-out
// envelope: the group must be the one being addressed and the epoch must be
// the one the batch declares.
func ValidateGroupMessageMetadata(group string, epoch int, md map[string]string) error {
	group = strings.ToLower(strings.TrimSpace(group))
	claimed, ok := GroupOfMessage(md)
	if !ok {
		return fmt.Errorf("group envelope must carry metadata %q", MetaGroup)
	}
	if claimed != group {
		return fmt.Errorf("group envelope metadata names %q, not %q", claimed, group)
	}
	claimedEpoch, ok := GroupEpochOfMessage(md)
	if !ok {
		return fmt.Errorf("group envelope must carry a decimal metadata %q", MetaGroupEpoch)
	}
	if claimedEpoch != epoch {
		return fmt.Errorf("group envelope metadata epoch %d does not match the batch epoch %d", claimedEpoch, epoch)
	}
	return nil
}

// ReservedGroupMetadataError reports whether a *direct* message is trying to
// claim group provenance.
//
// The reserved keys are refused on the ordinary POST /messages path so that
// a sender cannot forge a group message: without this, anyone could send a
// direct message carrying `group: crew.acme.poweur.net` and have every
// recipient's client file it into that group's conversation, complete with a
// valid signature, without ever being a member.
func ReservedGroupMetadataError(md map[string]string) error {
	if len(md) == 0 {
		return nil
	}
	if _, ok := md[MetaGroup]; ok {
		return fmt.Errorf("metadata key %q is reserved for group fan-out (POST /groups/{group}/messages)", MetaGroup)
	}
	return nil
}

// GroupFanoutRecipients returns the members a fan-out must cover: every
// member except the sender, lowercased, de-duplicated and sorted.
//
// The sender is excluded because the relay never hands a sender their own
// message back (E09-T1); the sender's own copy is the local history record
// they archive when they send.
//
// **`members` is the list, alone.** `admins` is an authority list over the
// roster and confers no access, so an admin who is not a member neither
// receives the fan-out nor may send one. The two lists are independent by
// design (see apps/docs/docs/files/group-identities.md) and conflating them
// here would silently widen a group's audience.
func GroupFanoutRecipients(group ShareGroup, sender string) []string {
	sender = strings.ToLower(strings.TrimSpace(sender))
	seen := map[string]bool{}
	out := make([]string, 0, len(group.Members))
	for _, m := range group.Members {
		m = strings.ToLower(strings.TrimSpace(m))
		if m == "" || m == sender || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}
