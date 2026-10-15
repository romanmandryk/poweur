package cli

import (
	"reflect"
	"strings"
	"testing"

	idpkg "github.com/poweur/identity"
)

func TestEditFilterList(t *testing.T) {
	got := editFilterList([]string{"a@x.test", "@spam.test"}, []string{"B@y.test, c@z.test", "a@x.test"}, []string{"@spam.test,c@z.test"})
	if want := []string{"a@x.test", "b@y.test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLatestEmailSettings(t *testing.T) {
	if s := latestEmailSettings(nil); s.Receive != idpkg.EmailReceiveEveryone || len(s.Block) != 0 {
		t.Fatalf("defaults: %+v", s)
	}
	old := &idpkg.EmailSettings{Version: 1, Receive: "contacts", UpdatedAt: "2026-10-14T09:00:00Z"}
	newer := &idpkg.EmailSettings{Version: 1, Block: []string{"x@y.test"}, UpdatedAt: "2026-10-14T10:00:00Z"}
	s := latestEmailSettings([]mailEntry{{Kind: "settings", Settings: newer}, {Kind: "sent"}, {Kind: "settings", Settings: old}})
	if s.Receive != idpkg.EmailReceiveEveryone || len(s.Block) != 1 {
		t.Fatalf("latest: %+v", s)
	}
	if out := describeEmailSettings(s); !strings.Contains(out, "everyone") || !strings.Contains(out, "x@y.test") {
		t.Fatal(out)
	}
}
