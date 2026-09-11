package signin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/poweur/identity"
)

func validMetadata() Metadata {
	return Metadata{
		PoweurAuth:   identity.SignInVersion,
		Origin:       testOrigin,
		Name:         "Poweur Guestbook",
		AppID:        "net.poweur.guestbook",
		ResponseURIs: []string{testOrigin + "/auth/callback"},
		PollURI:      testOrigin + "/auth/poll",
		Scopes:       []string{testAppScope, identity.ScopeProfileRead},
		Transports:   []string{"redirect", "qr", "poll"},
	}
}

func TestMetadataValidate(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*Metadata)
		servedFrom string
		wantErr    bool
	}{
		{"valid", nil, testOrigin, false},
		{"valid without a served-from check", nil, "", false},
		{"wrong version", func(m *Metadata) { m.PoweurAuth = "0" }, testOrigin, true},
		{"origin does not match where it was served", nil, "https://evil.example", true},
		{"origin unparseable", func(m *Metadata) { m.Origin = "not a url" }, "", true},
		{"name missing", func(m *Metadata) { m.Name = "" }, testOrigin, true},
		{"response_uri off origin", func(m *Metadata) {
			m.ResponseURIs = []string{"https://evil.example/cb"}
		}, testOrigin, true},
		{"poll_uri off origin", func(m *Metadata) { m.PollURI = "https://evil.example/poll" }, testOrigin, true},
		// A third-party logo would leak the pending approval to whoever
		// serves it, so it is refused rather than merely not rendered.
		{"logo_uri off origin", func(m *Metadata) { m.LogoURI = "https://cdn.example/logo.png" }, testOrigin, true},
		{"logo_uri same origin", func(m *Metadata) { m.LogoURI = testOrigin + "/logo.png" }, testOrigin, false},
		{"app_id does not match the origin", func(m *Metadata) { m.AppID = "net.poweur.mail" }, testOrigin, true},
		{"advertised scope in another namespace", func(m *Metadata) {
			m.Scopes = []string{"dav:rw:apps/net.poweur.mail"}
		}, testOrigin, true},
		{"advertised scope unknown", func(m *Metadata) { m.Scopes = []string{"root:all"} }, testOrigin, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := validMetadata()
			if tc.mutate != nil {
				tc.mutate(&m)
			}
			err := m.Validate(tc.servedFrom)
			if tc.wantErr && err == nil {
				t.Fatal("expected an error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestMetadataAllowsResponseURI(t *testing.T) {
	m := validMetadata()
	tests := []struct {
		uri  string
		want bool
	}{
		{"", true},
		{testOrigin + "/auth/callback", true},
		{testOrigin + "/auth/callback/", true},
		{testOrigin + "/auth/other", false},
		{"https://evil.example/auth/callback", false},
	}
	for _, tc := range tests {
		if got := m.AllowsResponseURI(tc.uri); got != tc.want {
			t.Fatalf("AllowsResponseURI(%q) = %v, want %v", tc.uri, got, tc.want)
		}
	}

	// Publishing no list is the permissive demo default: any same-origin URI.
	open := validMetadata()
	open.ResponseURIs = nil
	if !open.AllowsResponseURI(testOrigin + "/anything") {
		t.Fatal("an RP with no published list should accept any same-origin URI")
	}
	if open.AllowsResponseURI("https://evil.example/cb") {
		t.Fatal("off-origin must be refused even with no published list")
	}
}

func metadataServer(t *testing.T, status int, body any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != MetadataPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(status)
		switch v := body.(type) {
		case string:
			_, _ = w.Write([]byte(v))
		default:
			_ = json.NewEncoder(w).Encode(v)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchMetadata(t *testing.T) {
	// The document must claim the origin it is actually served from, so the
	// handler fills it in from the request it answers.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != MetadataPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(Metadata{
			PoweurAuth: identity.SignInVersion,
			Origin:     "http://" + r.Host,
			Name:       "Guestbook",
		})
	}))
	defer srv.Close()

	got, err := FetchMetadata(context.Background(), srv.URL, FetchOptions{})
	if err != nil {
		t.Fatalf("FetchMetadata: %v", err)
	}
	if got.Name != "Guestbook" {
		t.Fatalf("name = %q", got.Name)
	}
}

func TestFetchMetadataFailureModes(t *testing.T) {
	t.Run("non-200", func(t *testing.T) {
		srv := metadataServer(t, http.StatusNotFound, "")
		if _, err := FetchMetadata(context.Background(), srv.URL, FetchOptions{}); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("not JSON", func(t *testing.T) {
		srv := metadataServer(t, http.StatusOK, "<html>nope</html>")
		if _, err := FetchMetadata(context.Background(), srv.URL, FetchOptions{}); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("claims another origin", func(t *testing.T) {
		srv := metadataServer(t, http.StatusOK, Metadata{
			PoweurAuth: identity.SignInVersion, Origin: "https://evil.example", Name: "Impostor",
		})
		_, err := FetchMetadata(context.Background(), srv.URL, FetchOptions{})
		if err == nil || !strings.Contains(err.Error(), "claims origin") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("oversized body", func(t *testing.T) {
		srv := metadataServer(t, http.StatusOK, `{"name":"`+strings.Repeat("x", MaxMetadataBytes)+`"}`)
		if _, err := FetchMetadata(context.Background(), srv.URL, FetchOptions{}); err == nil {
			t.Fatal("expected an error")
		}
	})
	// A redirect would let one origin answer for another, which is exactly
	// the confusion this fetch exists to prevent.
	t.Run("redirect not followed", func(t *testing.T) {
		target := metadataServer(t, http.StatusOK, Metadata{PoweurAuth: identity.SignInVersion, Name: "Elsewhere"})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+MetadataPath, http.StatusFound)
		}))
		defer srv.Close()
		if _, err := FetchMetadata(context.Background(), srv.URL, FetchOptions{}); err == nil {
			t.Fatal("expected the redirect to be refused")
		}
	})
}

func TestCheckRequestAgainstMetadata(t *testing.T) {
	meta := validMetadata()
	base := identity.SignInRequest{
		PoweurAuth:  identity.SignInVersion,
		RequestID:   "req_1",
		Audience:    testOrigin,
		Nonce:       "n",
		IssuedAt:    "2026-01-15T09:29:00Z",
		ExpiresAt:   "2026-01-15T09:31:00Z",
		Action:      identity.SignInActionSignin,
		ResponseURI: testOrigin + "/auth/callback",
	}
	if err := CheckRequestAgainstMetadata(base, meta); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Same-origin but not published: the per-RP allowlist is what stops an
	// open redirect elsewhere on the RP from becoming an approval leak.
	unlisted := base
	unlisted.ResponseURI = testOrigin + "/redirect?to=https://evil.example"
	if err := CheckRequestAgainstMetadata(unlisted, meta); err == nil {
		t.Fatal("expected an unpublished response_uri to be refused")
	}

	// Metadata fetched from a different origin than the request's audience.
	elsewhere := base
	elsewhere.Audience = "https://other.example"
	if err := CheckRequestAgainstMetadata(elsewhere, meta); err == nil {
		t.Fatal("expected the audience/metadata mismatch to be refused")
	}
}
