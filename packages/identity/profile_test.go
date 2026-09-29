package identity

import (
	"encoding/json"
	"testing"
)

func boolPtr(v bool) *bool { return &v }

func TestIdentityPageSettingsDefaults(t *testing.T) {
	var absent *IdentityPageSettings
	if !absent.EnabledOrDefault() || !absent.IndexableOrDefault() || absent.AdvertisesAnonymousMessages() {
		t.Fatal("absent settings must enable and index the page without advertising anonymous messages")
	}

	settings := &IdentityPageSettings{
		Enabled:                    boolPtr(false),
		Indexable:                  boolPtr(false),
		AdvertiseAnonymousMessages: boolPtr(true),
	}
	if settings.EnabledOrDefault() || settings.IndexableOrDefault() || !settings.AdvertisesAnonymousMessages() {
		t.Fatal("explicit identity-page settings were not honoured")
	}
}

func TestProfileIdentityPageRoundTrip(t *testing.T) {
	raw := []byte(`{"version":1,"identity_page":{"enabled":false,"indexable":true,"advertise_anonymous_messages":true}}`)
	profile, err := ParseProfile(raw)
	if err != nil {
		t.Fatal(err)
	}
	if profile.IdentityPage == nil || profile.IdentityPage.EnabledOrDefault() || !profile.IdentityPage.IndexableOrDefault() || !profile.IdentityPage.AdvertisesAnonymousMessages() {
		t.Fatalf("unexpected parsed settings: %#v", profile.IdentityPage)
	}
	encoded, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip Profile
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.IdentityPage == nil || roundTrip.IdentityPage.Enabled == nil || *roundTrip.IdentityPage.Enabled {
		t.Fatalf("explicit false was not preserved: %s", encoded)
	}
}

func TestIdentityPageRejectsNull(t *testing.T) {
	if _, err := ParseProfile([]byte(`{"version":1,"identity_page":null}`)); err == nil {
		t.Fatal("identity_page:null must not be accepted as an omitted settings block")
	}
}
