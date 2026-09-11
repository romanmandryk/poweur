package identity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testOptions points the fetcher at an httptest server while keeping the
// identity FQDN as the Host (the production shape: the identity IS the host).
func testOptions(t *testing.T, handler http.Handler) (ResolveOptions, func()) {
	t.Helper()
	ts := httptest.NewServer(handler)
	client := &http.Client{
		Transport: &hostRewriteTransport{
			base: ts.Client().Transport,
			host: strings.TrimPrefix(ts.URL, "http://"),
		},
	}
	return ResolveOptions{Scheme: "http", AllowPrivate: true, HTTPClient: client}, ts.Close
}

func TestFetchProfile(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantErr    bool
		wantAbsent bool
		wantName   string
	}{
		{name: "valid profile", status: 200, body: `{"version":1,"display_name":"Alice"}`, wantName: "Alice"},
		{name: "absent is not a failure", status: 404, body: "", wantErr: true, wantAbsent: true},
		{name: "server error", status: 500, body: "boom", wantErr: true},
		{name: "malformed json", status: 200, body: `{`, wantErr: true},
		{name: "schema violation", status: 200, body: `{"version":1,"avatar":"https://evil.example/x.png"}`, wantErr: true},
		{name: "unsupported version", status: 200, body: `{"version":9}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, closeFn := testOptions(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != WellKnownProfilePath {
					t.Errorf("path = %q", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer closeFn()

			profile, err := FetchProfile(context.Background(), "alice.poweur.net", opts)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", profile)
				}
				if tc.wantAbsent && !errors.Is(err, ErrPublicFileAbsent) {
					t.Fatalf("404 must be ErrPublicFileAbsent, got %v", err)
				}
				if !tc.wantAbsent && errors.Is(err, ErrPublicFileAbsent) {
					t.Fatalf("non-404 must not be ErrPublicFileAbsent: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if profile.DisplayName != tc.wantName {
				t.Fatalf("display_name = %q, want %q", profile.DisplayName, tc.wantName)
			}
		})
	}
}

func TestFetchCapabilities(t *testing.T) {
	opts, closeFn := testOptions(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != WellKnownCapabilitiesPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"version":1,"features":{"messaging":"v1"},"endpoints":{"dav":"https://alice.poweur.net/dav"}}`))
	}))
	defer closeFn()

	caps, err := FetchCapabilities(context.Background(), "alice.poweur.net", opts)
	if err != nil {
		t.Fatal(err)
	}
	if caps.Features["messaging"] != "v1" || caps.Endpoints["dav"] == "" {
		t.Fatalf("capabilities = %+v", caps)
	}
}

// A document over the cap is refused rather than read: the fetch target is
// chosen by whoever the user typed, so the body budget is not negotiable.
func TestFetchPublicFileSizeCap(t *testing.T) {
	opts, closeFn := testOptions(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":1,"bio":"` + strings.Repeat("x", MaxPublicDocumentBytes) + `"}`))
	}))
	defer closeFn()

	if _, err := FetchPublicFile(context.Background(), "alice.poweur.net", WellKnownProfilePath, opts); err == nil {
		t.Fatal("oversize document must be refused")
	}
}

func TestFetchPublicFileRejectsForeignPaths(t *testing.T) {
	for _, path := range []string{"/dav/alice.poweur.net/poweur-sys/private/x.json", "/.well-known/other/x", "profile.json", ""} {
		if _, err := FetchPublicFile(context.Background(), "alice.poweur.net", path,
			ResolveOptions{AllowPrivate: true}); err == nil {
			t.Fatalf("path %q must be refused", path)
		}
	}
}

func TestFetchPublicFileRejectsInvalidIdentity(t *testing.T) {
	if _, err := FetchPublicFile(context.Background(), "not a host", WellKnownProfilePath,
		ResolveOptions{AllowPrivate: true}); err == nil {
		t.Fatal("invalid identity must be refused before any request")
	}
}

func TestCapabilitiesFromDocument(t *testing.T) {
	cases := []struct {
		name string
		doc  IdentityDocument
		want int
	}{
		{"none", IdentityDocument{}, 0},
		{"blank entries dropped", IdentityDocument{Capabilities: []string{" ", ""}}, 0},
		{"list becomes features", IdentityDocument{Capabilities: []string{"messaging", "files"}}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caps := CapabilitiesFromDocument(tc.doc)
			if len(caps.Features) != tc.want {
				t.Fatalf("features = %+v, want %d", caps.Features, tc.want)
			}
			if tc.want > 0 && caps.Version != 1 {
				t.Fatalf("version = %d", caps.Version)
			}
		})
	}
}

func TestAvatarURL(t *testing.T) {
	cases := []struct {
		name, path, scheme, want string
	}{
		{"public path", "public/avatar.png", "https", "https://alice.poweur.net/pub/avatar.png"},
		{"nested", "public/img/a.png", "http", "http://alice.poweur.net/pub/img/a.png"},
		{"default scheme", "public/a.png", "", "https://alice.poweur.net/pub/a.png"},
		{"external url refused", "https://evil.example/a.png", "https", ""},
		{"private tree refused", "private/a.png", "https", ""},
		{"traversal refused", "public/../private/a.png", "https", ""},
		{"empty", "", "https", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AvatarURL("alice.poweur.net", tc.path, tc.scheme); got != tc.want {
				t.Fatalf("AvatarURL(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
