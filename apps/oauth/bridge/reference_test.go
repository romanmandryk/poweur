package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/poweur/identity"
)

func getAccept(t *testing.T, url, accept string) (int, http.Header, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 64<<10)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, resp.Header, b.String()
}

// The QR's short link serves the request to signers, and a page to cameras.
func TestRequestByReference(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	id := b.identify(authorizeQuery("rp", rpRedirect, "openid"), h.users[alice])
	txn := h.txn(id)
	if len(txn.RequestCode) != identity.ShortCodeLen {
		t.Fatalf("request code %q", txn.RequestCode)
	}
	link := h.issuer + "/r/" + txn.RequestCode

	// A signer: JSON, open to any origin, and exactly the request.
	status, header, body := getAccept(t, link, "application/json")
	var got struct{ Request string }
	if status != 200 || header.Get("Access-Control-Allow-Origin") != "*" || json.Unmarshal([]byte(body), &got) != nil || got.Request != txn.Request {
		t.Fatalf("json = %d %v %s", status, header, body)
	}
	// Typed loosely, still found.
	if status, _, _ := getAccept(t, h.issuer+"/r/"+strings.ToLower(txn.RequestCode[:4])+"-"+txn.RequestCode[4:], "application/json"); status != 200 {
		t.Fatalf("typed code = %d", status)
	}
	// The same fetch every signer does, including the audience check.
	enc, req, err := identity.FetchSignInRequest(context.Background(), http.DefaultClient, link)
	if err != nil || enc != txn.Request || req.RequestID != txn.RequestID {
		t.Fatalf("fetch = %v %+v", err, req)
	}

	// A phone camera: a page offering the app and the signers, by reference,
	// and never the match code.
	p := b.get("/r/" + txn.RequestCode)
	var hp handoffPage
	if p.data(t, &hp); !p.is("handoff") || hp.Identity != alice || hp.Client == nil || hp.Client.Name != "Relying Party" {
		t.Fatalf("handoff = %s %+v", p.shown().Page, hp)
	}
	if hp.DeepLink != identity.SignInReferenceDeepLink(link) || len(hp.Signers) != 2 ||
		!strings.HasSuffix(hp.Signers[0].Href, "?auth="+strings.ReplaceAll(strings.ReplaceAll(link, ":", "%3A"), "/", "%2F")) {
		t.Fatalf("handoff links = %+v", hp)
	}
	if strings.Contains(p.body, `"match"`) || strings.Contains(p.body, txn.Match+`"`) && len(txn.Match) > 1 && strings.Contains(p.body, `"`+txn.Match+`"`) {
		t.Fatal("the handoff page carries the match code")
	}

	// Once approved, the link is spent.
	h.deliver(h.approve(id, h.users[alice]), txn.Match)
	if status, _, _ := getAccept(t, link, "application/json"); status != http.StatusGone {
		t.Fatalf("after approval = %d", status)
	}
	for _, bad := range []string{"ZZZZZZZZ", "not-a-code"} {
		if status, _, _ := getAccept(t, h.issuer+"/r/"+bad, "application/json"); status != http.StatusGone {
			t.Errorf("%s = %d", bad, status)
		}
	}
}

// Changing identity issues a new request, and the old code stops working.
func TestRequestCodeFollowsTheCurrentRequest(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	id := b.identify(authorizeQuery("rp", rpRedirect, "openid"), h.users[alice])
	old := h.txn(id).RequestCode
	b.post("/t/"+id+"/identify", map[string][]string{"identity": {bob}})
	if fresh := h.txn(id).RequestCode; fresh == "" || fresh == old {
		t.Fatalf("code after re-identify = %q (was %q)", fresh, old)
	}
	if status, _, _ := getAccept(t, h.issuer+"/r/"+old, "application/json"); status != http.StatusGone {
		t.Fatalf("superseded code = %d", status)
	}
}
