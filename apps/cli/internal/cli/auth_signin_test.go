package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
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

func TestDescribeAuthPrompt(t *testing.T) {
	req := signInRequestForCLI(t)
	enc, _ := idpkg.EncodeSignInRequest(req)
	body, _ := json.Marshal(idpkg.AuthRequestPayload{
		Version: 1, Request: enc, Client: "Team dashboard", ClientHost: "grafana.example.org", ExpiresAt: req.ExpiresAt,
	})
	got := describeAuthPrompt("bridge.poweur.org", string(body))
	for _, want := range []string{"Team dashboard (grafana.example.org)", "via bridge.poweur.org", "poweur auth approve " + enc, "--code"} {
		if !strings.Contains(got, want) {
			t.Fatalf("describeAuthPrompt = %q, lacks %q", got, want)
		}
	}
	if got := describeAuthPrompt("x.example", "{not json"); !strings.Contains(got, "could not be read") {
		t.Fatalf("garbage = %q", got)
	}
}

func TestSendMessageDoesNotFollowRedirects(t *testing.T) {
	var followed bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed = true
		_, _ = w.Write([]byte("<html>a landing page</html>"))
	}))
	defer target.Close()
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusMovedPermanently)
	}))
	defer redirecting.Close()
	resp, err := SendMessage(context.Background(), redirecting.URL, Message{ID: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if followed || resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("redirect followed=%v status=%d — a redirect must never read as delivery", followed, resp.StatusCode)
	}
}

// A request by reference (E08-T6): the RP's short link, fetched, and accepted
// only when it lives at the request's own audience.
func TestReadSignInRequestByReference(t *testing.T) {
	var audience string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := signInRequestForCLI(t)
		req.Audience = audience
		encoded, _ := idpkg.EncodeSignInRequest(req)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"request":%q}`, encoded)
	}))
	defer srv.Close()
	link := srv.URL + "/r/K7QM4XP2"

	audience = srv.URL
	for _, source := range []string{link, idpkg.SignInReferenceDeepLink(link), "https://signer.example/app/?auth=" + url.QueryEscape(link)} {
		got, err := readSignInRequest(source)
		if err != nil || got.Audience != srv.URL {
			t.Fatalf("%s: %+v %v", source, got, err)
		}
	}
	// A link serving a request for another site is refused.
	audience = "https://bank.example"
	if _, err := readSignInRequest(link); err == nil {
		t.Fatal("a request for another audience was accepted by reference")
	}
}
