package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	idpkg "github.com/poweur/identity"
	signinpkg "github.com/poweur/identity/signin"
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

func TestDeliverSignInApprovalSendsCodeAndReadsReceipt(t *testing.T) {
	var got signinpkg.Delivery
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d, err := signinpkg.ParseDelivery(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		got = d
		_ = json.NewEncoder(w).Encode(signinpkg.DeliveryReceipt{Status: "ok", ResumeURI: "https://rp.example/auth/resume?code=x"})
	}))
	defer srv.Close()

	receipt, err := deliverSignInApproval(context.Background(), srv.URL+"/cb", signinpkg.Delivery{Response: "enc", Match: "42"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Response != "enc" || got.Match != "42" {
		t.Fatalf("server received %+v", got)
	}
	if receipt.ResumeURI != "https://rp.example/auth/resume?code=x" {
		t.Fatalf("receipt = %+v", receipt)
	}
}

func TestDeliverSignInApprovalToleratesLegacyAndRefusesRedirects(t *testing.T) {
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer legacy.Close()
	receipt, err := deliverSignInApproval(context.Background(), legacy.URL, signinpkg.Delivery{Response: "enc"})
	if err != nil || receipt.ResumeURI != "" {
		t.Fatalf("legacy RP: receipt %+v err %v", receipt, err)
	}

	moved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.example/cb", http.StatusTemporaryRedirect)
	}))
	defer moved.Close()
	if _, err := deliverSignInApproval(context.Background(), moved.URL, signinpkg.Delivery{Response: "enc"}); err == nil {
		t.Fatal("a redirecting callback must not be treated as a delivery")
	}

	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"code mismatch"}`, http.StatusForbidden)
	}))
	defer refused.Close()
	if _, err := deliverSignInApproval(context.Background(), refused.URL, signinpkg.Delivery{Response: "enc", Match: "11"}); err == nil {
		t.Fatal("a refused delivery must be an error")
	}
}
