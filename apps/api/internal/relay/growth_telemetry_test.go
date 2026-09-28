package relay

import (
	"bytes"
	"reflect"
	"sync"
	"testing"
)

func TestMessageKindIsBounded(t *testing.T) {
	for in, want := range map[string]string{
		"":                          "chat",
		"chat.text":                 "chat",
		"sys.contact.request":       "sys.contact.request",
		"sys.share.offer":           "sys.share.offer",
		"sys.made.up":               "sys.other",
		"net.poweur.tasks.assigned": "app",
		"com.example.anything.goes": "app",
	} {
		if got := messageKind(in); got != want {
			t.Errorf("messageKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSettingsChanges(t *testing.T) {
	profile := ".poweur/public/profile.json"
	for _, tt := range []struct {
		name, path, old, next string
		want                  []string
	}{
		{"first profile counts set fields", profile, "", `{"version":1,"display_name":"Alice","bio":""}`, []string{"profile.display_name"}},
		{"unchanged rewrite", profile, `{"version":1,"display_name":"Alice"}`, `{"display_name":"Alice","version":1}`, nil},
		{"empty equals absent", profile, `{"version":1,"links":[]}`, `{"version":1,"locale":""}`, nil},
		{"several fields", profile, `{"display_name":"A","avatar":"public/a.png"}`, `{"display_name":"B","bio":"hi"}`, []string{"profile.display_name", "profile.avatar", "profile.bio"}},
		{"unknown fields ignored", profile, `{"x":1}`, `{"x":2,"secret_field":"y"}`, nil},
		{"inbox policy", ".poweur/relay/inbox-policy.json", `{"version":1,"mode":"open"}`, `{"version":1,"mode":"open","anonymous":{"challenge":"pow"}}`, []string{"inbox.anonymous"}},
		{"analytics", analyticsPath, `{"version":1,"granted":false,"updated_at":"2026-09-10T12:00:00Z"}`, `{"version":1,"granted":true,"updated_at":"2026-09-11T12:00:00Z"}`, []string{"analytics.granted"}},
		{"untracked document", ".poweur/relay/contacts.json", "", `{"version":1,"contacts":[]}`, nil},
		{"malformed new document", profile, "", `not json`, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := settingsChanges(tt.path, []byte(tt.old), []byte(tt.next)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAdoptionCounts(t *testing.T) {
	docs := map[string]string{
		"a/.poweur/public/profile.json":     `{"version":1,"display_name":"Alice","avatar":"me.png","links":[{"label":"x","url":"https://x"}]}`,
		"a/.poweur/relay/inbox-policy.json": `{"version":1,"mode":"contacts_and_requests","anonymous":{"challenge":"pow"},"read_receipts":{"enabled":false}}`,
		"a/" + analyticsPath:                `{"version":1,"granted":true,"updated_at":"2026-09-10T12:00:00Z"}`,
		"a/.poweur/relay/contacts.json":     `{"version":1,"contacts":[{"identity":"b.poweur.net","state":"accepted"}]}`,
		"b/.poweur/relay/contacts.json":     `{"version":1,"contacts":[{"identity":"spam.example","state":"blocked"}]}`,
		"b/.poweur/public/profile.json":     `{"version":1,"display_name":"  ","bio":"hello"}`,
		"b/.poweur/relay/inbox-policy.json": `{"version":1,"mode":"bogus"}`,
		"c/.poweur/public/profile.json":     `not json`,
	}
	got := adoptionCounts([]string{"a", "b", "c"}, func(id, path string) []byte {
		if d, ok := docs[id+"/"+path]; ok {
			return []byte(d)
		}
		return nil
	})
	want := map[string]int64{
		"adopt_profile_display_name": 1, "adopt_profile_bio": 1, "adopt_profile_avatar": 1, "adopt_profile_links": 1, "adopt_profile_locale": 0,
		"adopt_inbox_default": 2, "adopt_inbox_open": 0, "adopt_inbox_contacts_only": 0, "adopt_inbox_contacts_and_requests": 1,
		"adopt_inbox_anonymous": 1, "adopt_read_receipts_off": 1, "adopt_analytics_granted": 1, "adopt_contacts": 1,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if zero := adoptionCounts(nil, nil); len(zero) != len(adoptionStates) {
		t.Fatalf("every adoption state must be reported, got %v", zero)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
