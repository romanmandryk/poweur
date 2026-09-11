package identity

import (
	"strings"
	"testing"
)

func TestGroupThreadID(t *testing.T) {
	const group = "crew.acme.poweur.net"
	cases := []struct {
		name   string
		group  string
		suffix string
		want   string
	}{
		{"no suffix is the group itself", group, "", group},
		{"suffix is appended", group, "design", group + ":design"},
		{"suffix is trimmed", group, "  design  ", group + ":design"},
		{"group name is lowercased", "CREW.Acme.Poweur.NET", "", group},
		{"an already-qualified suffix is not doubled", group, group + ":design", group + ":design"},
		{"the group's own id as suffix collapses", group, group, group},
		{"the group's own id, cased, collapses", group, "CREW.acme.poweur.net", group},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GroupThreadID(tc.group, tc.suffix); got != tc.want {
				t.Fatalf("GroupThreadID(%q, %q) = %q, want %q", tc.group, tc.suffix, got, tc.want)
			}
		})
	}
}

// Every thread_id GroupThreadID produces must be one ValidateGroupThreadID
// accepts, or a client could build a thread its own relay refuses.
func TestGroupThreadIDRoundTrips(t *testing.T) {
	const group = "crew.acme.poweur.net"
	for _, suffix := range []string{"", "design", "design-review", "q3", group, group + ":x"} {
		id := GroupThreadID(group, suffix)
		if err := ValidateGroupThreadID(group, id); err != nil {
			t.Fatalf("GroupThreadID(%q, %q) = %q which does not validate: %v", group, suffix, id, err)
		}
	}
}

func TestValidateGroupThreadID(t *testing.T) {
	const group = "crew.acme.poweur.net"
	cases := []struct {
		name     string
		group    string
		threadID string
		wantErr  string
	}{
		{"the group itself", group, group, ""},
		{"a sub-thread", group, group + ":design", ""},
		{"case-insensitive", group, "CREW.acme.poweur.net", ""},
		{"missing thread", group, "", "must carry a thread_id"},
		{"empty group", "", group, "group is required"},
		{"a 1:1 thread", group, "chat-42", "does not belong to group"},
		{"another group's thread", group, "other.acme.poweur.net:design", "does not belong to group"},
		{"a prefix that is not a boundary", group, group + "x", "does not belong to group"},
		{"separator with no suffix", group, group + ":", "does not belong to group"},
		{"illegal thread character", group, group + ":a/b", "invalid character"},
		{"too long", group, group + ":" + strings.Repeat("a", MaxThreadIDLen), "too long"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateGroupThreadID(tc.group, tc.threadID)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestGroupMessageMetadata(t *testing.T) {
	extra := map[string]string{"mime": "text/plain"}
	md := GroupMessageMetadata("CREW.acme.poweur.net", 3, extra)
	if md[MetaGroup] != "crew.acme.poweur.net" {
		t.Fatalf("group = %q", md[MetaGroup])
	}
	if md[MetaGroupEpoch] != "3" {
		t.Fatalf("epoch = %q", md[MetaGroupEpoch])
	}
	if md["mime"] != "text/plain" {
		t.Fatal("caller metadata was dropped")
	}
	// The caller's own map must be untouched: it is usually the flags a user
	// typed, and a failed send should not have rewritten them.
	if _, ok := extra[MetaGroup]; ok {
		t.Fatal("GroupMessageMetadata mutated the caller's map")
	}
	// The result must survive the envelope's own metadata rules, or a group
	// message could not be signed at all.
	if err := ValidateMetadata(md); err != nil {
		t.Fatalf("group metadata is not valid envelope metadata: %v", err)
	}
}

func TestGroupOfMessage(t *testing.T) {
	cases := []struct {
		name      string
		md        map[string]string
		wantGroup string
		wantOK    bool
	}{
		{"absent", nil, "", false},
		{"empty value", map[string]string{MetaGroup: "  "}, "", false},
		{"present", map[string]string{MetaGroup: "Crew.Acme.Poweur.NET"}, "crew.acme.poweur.net", true},
		{"other keys only", map[string]string{"mime": "text/plain"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := GroupOfMessage(tc.md)
			if got != tc.wantGroup || ok != tc.wantOK {
				t.Fatalf("GroupOfMessage = (%q, %v), want (%q, %v)", got, ok, tc.wantGroup, tc.wantOK)
			}
		})
	}
}

func TestGroupEpochOfMessage(t *testing.T) {
	cases := []struct {
		name   string
		value  string
		want   int
		wantOK bool
	}{
		{"decimal", "3", 3, true},
		{"zero", "0", 0, true},
		{"absent", "", 0, false},
		{"negative", "-1", 0, false},
		{"not a number", "three", 0, false},
		{"float", "3.0", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			md := map[string]string{}
			if tc.value != "" {
				md[MetaGroupEpoch] = tc.value
			}
			got, ok := GroupEpochOfMessage(md)
			if got != tc.want || ok != tc.wantOK {
				t.Fatalf("GroupEpochOfMessage(%q) = (%d, %v), want (%d, %v)", tc.value, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestValidateGroupMessageMetadata(t *testing.T) {
	const group = "crew.acme.poweur.net"
	cases := []struct {
		name    string
		md      map[string]string
		wantErr string
	}{
		{"valid", GroupMessageMetadata(group, 3, nil), ""},
		{"no group key", map[string]string{MetaGroupEpoch: "3"}, "must carry metadata"},
		{"wrong group", GroupMessageMetadata("other.acme.poweur.net", 3, nil), "names"},
		{"no epoch", map[string]string{MetaGroup: group}, "must carry a decimal"},
		{"stale epoch", GroupMessageMetadata(group, 2, nil), "does not match the batch epoch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateGroupMessageMetadata(group, 3, tc.md)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

// A direct message must not be able to claim group provenance: otherwise a
// stranger's signed envelope would be filed into a group conversation by
// every recipient's client without them ever being a member.
func TestReservedGroupMetadataError(t *testing.T) {
	if err := ReservedGroupMetadataError(nil); err != nil {
		t.Fatalf("nil metadata: %v", err)
	}
	if err := ReservedGroupMetadataError(map[string]string{"mime": "text/plain"}); err != nil {
		t.Fatalf("unrelated metadata: %v", err)
	}
	err := ReservedGroupMetadataError(map[string]string{MetaGroup: "crew.acme.poweur.net"})
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected a reserved-key error, got %v", err)
	}
	// The epoch alone is meaningless without a group, so it is not reserved:
	// refusing it would break any application that happens to use the word.
	if err := ReservedGroupMetadataError(map[string]string{MetaGroupEpoch: "3"}); err != nil {
		t.Fatalf("epoch alone should not be reserved: %v", err)
	}
}

func TestGroupFanoutRecipients(t *testing.T) {
	group := ShareGroup{
		Group:   "crew.acme.poweur.net",
		Owner:   "crew.acme.poweur.net",
		Members: []string{"Bob.example.org", "alice.poweur.net", "bob.example.org", "carol.poweur.net", " "},
		Admins:  []string{"admin.poweur.net"},
		Epoch:   3,
	}
	got := GroupFanoutRecipients(group, "ALICE.poweur.net")
	want := []string{"bob.example.org", "carol.poweur.net"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("GroupFanoutRecipients = %v, want %v", got, want)
	}

	// An admin who is not a member gets nothing: `admins` is an authority
	// list over the roster and confers no access. Unioning the two here
	// would silently widen every group's audience.
	for _, r := range GroupFanoutRecipients(group, "bob.example.org") {
		if r == "admin.poweur.net" {
			t.Fatal("a non-member admin must not receive the fan-out")
		}
	}

	// A sender who is not a member is not silently excused: the caller
	// checks membership, and the recipient list is simply everyone.
	if len(GroupFanoutRecipients(group, "stranger.example.net")) != 3 {
		t.Fatalf("a non-member sender should not shrink the roster: %v",
			GroupFanoutRecipients(group, "stranger.example.net"))
	}

	// An empty group fans out to nobody rather than to itself.
	if got := GroupFanoutRecipients(ShareGroup{Group: "g.example.net"}, "alice.poweur.net"); len(got) != 0 {
		t.Fatalf("empty group: %v", got)
	}
}

// The cap is the design's revisit threshold made enforceable; pinning it
// here means moving it is a deliberate edit rather than a drift.
func TestMaxGroupFanoutMembers(t *testing.T) {
	if MaxGroupFanoutMembers != 100 {
		t.Fatalf("MaxGroupFanoutMembers = %d; the MLS study names 100 as the switch criterion", MaxGroupFanoutMembers)
	}
	if MaxGroupFanoutMembers > MaxGroupMembers {
		t.Fatal("the fan-out cap must not exceed the membership cap")
	}
}
