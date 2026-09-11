package cli

import (
	"net/url"
	"os"
	"testing"
	"time"

	idpkg "github.com/poweur/identity"
)

func signInRequestForCLI(t *testing.T) idpkg.SignInRequest {
	t.Helper()
	now := time.Now().UTC()
	req, err := (idpkg.SignInRequest{
		PoweurAuth: idpkg.SignInVersion, RequestID: "req_cli", Nonce: "nonce_cli",
		Audience: "https://tasks.example", Action: idpkg.SignInActionSignin,
		IssuedAt:  now.Add(-time.Minute).Format(time.RFC3339),
		ExpiresAt: now.Add(time.Minute).Format(time.RFC3339),
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestReadSignInRequestForms(t *testing.T) {
	req := signInRequestForCLI(t)
	encoded, err := idpkg.EncodeSignInRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	deep, _ := idpkg.SignInDeepLink(req)
	web := "https://signer.example/app/?auth=" + url.QueryEscape(encoded)
	file := t.TempDir() + "/request.txt"
	if err := os.WriteFile(file, []byte(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{encoded, deep, web, file} {
		got, err := readSignInRequest(source)
		if err != nil {
			t.Fatalf("%s: %v", source, err)
		}
		if got.RequestID != req.RequestID || got.Audience != req.Audience {
			t.Fatalf("decoded %#v", got)
		}
	}
}

func TestReadSignInRequestRejectsGarbage(t *testing.T) {
	if _, err := readSignInRequest("not-a-request-and-not-a-file"); err == nil {
		t.Fatal("expected malformed request to fail")
	}
}
