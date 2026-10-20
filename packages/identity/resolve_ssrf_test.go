package identity

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSafeHTTPClientRefusesLoopbackUnlessAllowed(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer ts.Close()

	// The check runs at dial time on the resolved address, so it holds no
	// matter what a (possibly rebinding) DNS server said beforehand.
	refused := newSafeHTTPClient(ResolveOptions{Timeout: 2 * time.Second})
	_, err := refused.Get(ts.URL)
	if err == nil {
		t.Fatal("expected loopback dial to be refused with AllowPrivate=false")
	}
	if !errors.Is(err, errPrivateAddress) {
		t.Fatalf("err = %v, want errPrivateAddress", err)
	}

	allowed := newSafeHTTPClient(ResolveOptions{Timeout: 2 * time.Second, AllowPrivate: true})
	resp, err := allowed.Get(ts.URL)
	if err != nil {
		t.Fatalf("AllowPrivate=true should reach loopback: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestSafeHTTPClientRefusesRedirects(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			t.Error("redirect was followed")
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer ts.Close()

	client := newSafeHTTPClient(ResolveOptions{Timeout: 2 * time.Second, AllowPrivate: true})
	if _, err := client.Get(ts.URL + WellKnownPath); err == nil {
		t.Fatal("expected redirect to be refused")
	}
}

func TestPublicIP(t *testing.T) {
	cases := []struct {
		ip     string
		public bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"93.184.216.34", true},
		{"2606:4700:4700::1111", true},
		{"::ffff:8.8.8.8", true},

		{"127.0.0.1", false},
		{"127.10.0.1", false},
		{"::1", false},
		{"0.0.0.0", false},
		{"0.1.2.3", false},
		{"::", false},
		{"10.0.0.1", false},
		{"172.16.5.4", false},
		{"192.168.1.1", false},
		{"::ffff:192.168.1.1", false},
		{"::ffff:127.0.0.1", false},
		{"169.254.169.254", false},
		{"fe80::1", false},
		{"fc00::1", false},
		{"fd12:3456::1", false},
		{"100.64.0.1", false},
		{"100.127.255.254", false},
		{"198.18.0.1", false},
		{"198.19.255.255", false},
		{"192.0.0.8", false},
		{"192.0.2.1", false},
		{"198.51.100.1", false},
		{"203.0.113.1", false},
		{"224.0.0.1", false},
		{"239.255.255.250", false},
		{"ff02::1", false},
		{"ff01::1", false},
		{"240.0.0.1", false},
		{"255.255.255.255", false},
		{"64:ff9b::7f00:1", false},
		{"2001:db8::1", false},
	}
	for _, tc := range cases {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("bad test IP %q", tc.ip)
		}
		if got := publicIP(ip); got != tc.public {
			t.Errorf("publicIP(%s) = %v, want %v", tc.ip, got, tc.public)
		}
	}
}
