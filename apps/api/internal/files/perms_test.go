package files

import "testing"

func TestPermissionsMatrix(t *testing.T) {
	pe := Permissions{}
	owner := "alice.poweur.net"
	self := Principal{Identity: owner, Owner: true, Scope: Scope{Write: true}}
	visitor := Principal{Identity: "bob.example.org"}

	cases := []struct {
		name   string
		p      Principal
		path   string
		access Access
		want   bool
	}{
		{"owner reads private", self, "private/notes.txt", AccessRead, true},
		{"owner writes private", self, "private/notes.txt", AccessWrite, true},
		{"owner writes sys relay config", self, SysRelay + "/contacts.json", AccessWrite, true},
		{"owner writes sys private", self, SysPrivate + "/storage-credentials.json", AccessWrite, true},
		{"owner cannot overwrite id.json via DAV", self, SysPublic + "/id.json", AccessWrite, false},
		{"anonymous reads sys public", Anonymous, SysPublic + "/id.json", AccessRead, true},
		{"anonymous denied /public", Anonymous, "public/readme.md", AccessRead, false},
		{"anonymous denied private", Anonymous, "private/notes.txt", AccessRead, false},
		{"visitor reads /public", visitor, "public/readme.md", AccessRead, true},
		{"visitor reads root listing", visitor, "", AccessRead, true},
		{"visitor cannot write /public", visitor, "public/readme.md", AccessWrite, false},
		{"visitor denied /private", visitor, "private/notes.txt", AccessRead, false},
		{"visitor denied sys relay", visitor, SysRelay + "/contacts.json", AccessRead, false},
		{"visitor denied sys private", visitor, SysPrivate + "/creds.json", AccessRead, false},
		{"visitor denied /shared without grant", visitor, "shared/project/file", AccessRead, false},
	}
	for _, c := range cases {
		if got := pe.Allowed(owner, c.p, c.path, c.access); got != c.want {
			t.Errorf("%s: Allowed=%v want %v", c.name, got, c.want)
		}
	}
}

func TestOwnerScopeBounds(t *testing.T) {
	pe := Permissions{}
	owner := "alice.poweur.net"
	scoped := Principal{Identity: owner, Owner: true, Scope: Scope{Write: true, Prefix: "apps/net.poweur.tasks"}}
	if !pe.Allowed(owner, scoped, "apps/net.poweur.tasks/todo.json", AccessWrite) {
		t.Fatal("scoped owner token must write inside prefix")
	}
	if pe.Allowed(owner, scoped, "private/secret.txt", AccessRead) {
		t.Fatal("scoped owner token must not escape its prefix")
	}
}

type allowAllGrants struct{}

func (allowAllGrants) Allowed(owner, visitor, path string, access Access) bool { return true }

func TestGrantCheckerHookUsedForShared(t *testing.T) {
	pe := Permissions{Grants: allowAllGrants{}}
	visitor := Principal{Identity: "bob.example.org"}
	if !pe.Allowed("alice.poweur.net", visitor, "shared/project/file", AccessWrite) {
		t.Fatal("grant checker must be consulted for /shared")
	}
}
