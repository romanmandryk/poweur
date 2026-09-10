package cli

import (
	"bytes"
	"strings"
	"testing"

	idpkg "github.com/poweur/identity"
)

func TestGroupMessageDispatcherRejectsMissingArguments(t *testing.T) {
	for _, args := range [][]string{{"send"}, {"inbox"}, {"inbox", "one", "two"}} {
		var stdout, stderr bytes.Buffer
		if code := runGroup(args, &stdout, &stderr); code == 0 {
			t.Fatalf("args %v: expected failure", args)
		}
		if stderr.Len() == 0 {
			t.Fatalf("args %v: expected an explanation", args)
		}
		if stdout.Len() != 0 {
			t.Fatalf("args %v: unexpected stdout %q", args, stdout.String())
		}
	}
}

func TestFilterGroupHistoryUsesThreadNamespace(t *testing.T) {
	records := []idpkg.HistoryRecord{
		{Sender: "alice.example", Recipient: "bob.example", ThreadID: "team.example"},
		{Sender: "bob.example", Recipient: "alice.example", ThreadID: "team.example:design"},
		{Sender: "mallory.example", Recipient: "alice.example", ThreadID: "other.example"},
		{Sender: "mallory.example", Recipient: "alice.example", ThreadID: "team.examplex"},
	}
	got := filterGroupHistory(records, "TEAM.EXAMPLE", "")
	if len(got) != 2 {
		t.Fatalf("group history length = %d, want 2: %#v", len(got), got)
	}
	got = filterGroupHistory(records, "team.example", "team.example:design")
	if len(got) != 1 || got[0].Sender != "bob.example" {
		t.Fatalf("sub-thread history = %#v", got)
	}
}

func TestGroupThreadSuffix(t *testing.T) {
	for _, tc := range []struct{ thread, want string }{
		{"", ""},
		{"team.example", ""},
		{"team.example:design", " [design]"},
	} {
		if got := groupThreadSuffix("team.example", tc.thread); got != tc.want {
			t.Fatalf("groupThreadSuffix(%q) = %q, want %q", tc.thread, got, tc.want)
		}
	}
}

func TestGroupMemberSummarySortsMembers(t *testing.T) {
	got := groupMemberSummary(idpkg.ShareGroup{
		Group: "team.example", Epoch: 3,
		Members: []string{"zoe.example", "alice.example"},
		Admins:  []string{"zoe.example"},
	})
	if !strings.Contains(got, "members: alice.example, zoe.example") ||
		!strings.Contains(got, "epoch 3") {
		t.Fatalf("summary = %q", got)
	}
}
