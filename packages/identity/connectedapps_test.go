package identity

import (
	"encoding/json"
	"testing"
	"time"
)

func TestConnectedAppsParseUpsertAndRevoke(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	app := ConnectedApp{
		AppID: "example.tasks", Audience: "https://tasks.example/",
		Scopes:    []string{"profile:read", "messages:send"},
		GrantedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(time.Hour).Format(time.RFC3339),
	}
	doc := (ConnectedApps{}).Upsert(app)
	raw, _ := json.Marshal(doc)
	parsed, err := ParseConnectedApps(raw)
	if err != nil {
		t.Fatal(err)
	}
	active, ok := parsed.Active("EXAMPLE.TASKS", now)
	if !ok || active.Audience != "https://tasks.example" || active.Scopes[0] != "messages:send" {
		t.Fatalf("active = %#v, %v", active, ok)
	}
	active.RevokedAt = now.Add(time.Minute).Format(time.RFC3339)
	parsed = parsed.Upsert(active)
	if _, ok := parsed.Active(active.AppID, now.Add(2*time.Minute)); ok {
		t.Fatal("revoked app remained active")
	}
}

func TestConnectedAppsRejectsNamespaceMismatch(t *testing.T) {
	raw := []byte(`{"version":1,"apps":[{"app_id":"example.other","audience":"https://tasks.example","scopes":[],"granted_at":"2026-09-10T12:00:00Z"}]}`)
	if _, err := ParseConnectedApps(raw); err == nil {
		t.Fatal("accepted mismatched app namespace")
	}
}
