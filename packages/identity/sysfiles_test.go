package identity

import (
	"strings"
	"testing"
)

func TestContactsFileValidation(t *testing.T) {
	f := ContactsFile{Version: 1, Contacts: []Contact{
		{Identity: "bob.example.org", State: ContactAccepted},
		{Identity: "eve.example.org", State: ContactBlocked},
	}}
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		file ContactsFile
		want string
	}{
		{"bad state", ContactsFile{Contacts: []Contact{{Identity: "a", State: "friend"}}}, "invalid state"},
		{"missing identity", ContactsFile{Contacts: []Contact{{State: ContactAccepted}}}, "identity is required"},
		{"duplicate", ContactsFile{Contacts: []Contact{
			{Identity: "Bob.example.org", State: ContactAccepted},
			{Identity: "bob.example.org", State: ContactBlocked}}}, "duplicate"},
		{"bad pin", ContactsFile{Contacts: []Contact{{Identity: "a", State: ContactAccepted, PinnedKey: "not-a-key!"}}}, "pinned_key"},
		{"bad time", ContactsFile{Contacts: []Contact{{Identity: "a", State: ContactAccepted, AddedAt: "yesterday"}}}, "added_at"},
	}
	for _, tc := range cases {
		err := tc.file.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: want error containing %q, got %v", tc.name, tc.want, err)
		}
	}
}

func TestContactsFindUpsertRemove(t *testing.T) {
	var f ContactsFile
	f = f.Upsert(Contact{Identity: "bob.example.org", State: ContactRequested})
	f = f.Upsert(Contact{Identity: "Bob.example.org", State: ContactAccepted})
	if len(f.Contacts) != 1 {
		t.Fatalf("upsert must replace case-insensitively: %+v", f.Contacts)
	}
	c, ok := f.Find("BOB.EXAMPLE.ORG")
	if !ok || c.State != ContactAccepted {
		t.Fatalf("find: %+v ok=%v", c, ok)
	}
	if !f.Remove("bob.example.org") || len(f.Contacts) != 0 {
		t.Fatal("remove failed")
	}
	if f.Remove("bob.example.org") {
		t.Fatal("double remove must report false")
	}
}

func TestInboxPolicyValidation(t *testing.T) {
	for _, mode := range []string{InboxOpen, InboxContactsOnly, InboxContactsAndRequests} {
		if _, err := ParseInboxPolicy([]byte(`{"version":1,"mode":"` + mode + `"}`)); err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
	}
	if _, err := ParseInboxPolicy([]byte(`{"mode":"friends_only"}`)); err == nil {
		t.Fatal("unknown mode must fail")
	}
	if _, err := ParseInboxPolicy([]byte(`{not json`)); err == nil {
		t.Fatal("garbage must fail")
	}
	p, err := ParseInboxPolicy([]byte(`{"version":1,"mode":"open","read_receipts":{"enabled":true,"disabled_for":["bob.example.org"]}}`))
	if err != nil || p.SendsReadReceiptsTo("bob.example.org") || !p.SendsReadReceiptsTo("carol.example.org") {
		t.Fatalf("read receipt policy: %+v err=%v", p, err)
	}
	if _, err := ParseInboxPolicy([]byte(`{"version":1,"mode":"open","read_receipts":{"enabled":true,"disabled_for":["bad"]}}`)); err == nil {
		t.Fatal("invalid read-receipt identity must fail")
	}
}

func TestProfileValidation(t *testing.T) {
	good := Profile{Version: 1, DisplayName: "Alice", Avatar: "avatar.png",
		Bio: "hello", Links: []ProfileLink{{Label: "web", URL: "https://alice.example"}}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []Profile{
		{Avatar: "https://evil.example/x.png"},    // avatar must be a file name
		{Avatar: "public/avatar.png"},             // not a path
		{Bio: strings.Repeat("x", 5000)},          // too long
		{Links: []ProfileLink{{Label: "no url"}}}, // empty url
		{DisplayName: strings.Repeat("n", 300)},   // too long
	}
	for i, p := range bad {
		if err := p.Validate(); err == nil {
			t.Fatalf("bad profile %d must fail", i)
		}
	}
}

func TestCapabilitiesValidation(t *testing.T) {
	good := Capabilities{Version: 1, Features: map[string]string{"messaging": "v1", "files": "webdav"}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Capabilities{Features: map[string]string{" ": "x"}}).Validate(); err == nil {
		t.Fatal("empty feature name must fail")
	}
}

func TestAppManifestValidation(t *testing.T) {
	if _, err := ParseAppManifest([]byte(`{"app_id":"net.poweur.tasks","name":"Tasks"}`)); err != nil {
		t.Fatal(err)
	}
	bad := []string{
		`{"app_id":"tasks","name":"Tasks"}`,  // not reverse-DNS
		`{"app_id":"net.poweur.tasks"}`,      // missing name
		`{"app_id":"net/poweur","name":"x"}`, // slash
	}
	for i, raw := range bad {
		if _, err := ParseAppManifest([]byte(raw)); err == nil {
			t.Fatalf("bad manifest %d must fail", i)
		}
	}
}
