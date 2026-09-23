package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Sign-in requests by reference (EPIC-008 E08-T6).
//
// A request carried in a QR code made a dense, hard-to-scan symbol: ~600
// characters. Instead the RP may hand out a short link, <audience>/r/<code>,
// and the signer fetches the request from it. The fetched request must name
// the link's own origin as its audience, so it is exactly as authentic as a
// request the RP handed over directly — over TLS, from its own origin.
//
// The same link opened in a browser (a phone's camera app) is the RP's to
// answer with a page that offers the signers.

// maxRequestFetch bounds a fetched request document.
const maxRequestFetch = 16 << 10

// SignInRequestURI reports whether input passes a request by reference, and
// returns the link. It accepts the link itself, the deep link
// poweur://auth?request_uri=…, and a web signer link whose `auth` (or
// `request`) parameter holds a link.
func SignInRequestURI(input string) (string, bool) {
	v := strings.TrimSpace(input)
	u, err := url.Parse(v)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "poweur":
		if ref := u.Query().Get("request_uri"); ref != "" {
			return httpLink(ref)
		}
		return "", false
	case "https", "http":
		q := u.Query()
		if q.Get("request") != "" {
			return "", false // a web signer link carrying the request itself
		}
		if inner := q.Get("auth"); inner != "" {
			return httpLink(inner) // a web signer link carrying a reference
		}
		if q.Get("request_uri") != "" {
			return httpLink(q.Get("request_uri"))
		}
		return httpLink(v)
	}
	return "", false
}

func httpLink(v string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(v))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", false
	}
	return u.String(), true
}

// CheckSignInRequestURI requires the link to live at the request's audience.
func CheckSignInRequestURI(requestURI string, req SignInRequest) error {
	origin, err := NormalizeOrigin(req.Audience)
	if err != nil {
		return err
	}
	if !SameOrigin(origin, requestURI) {
		return fmt.Errorf("%w: the request was fetched from outside its audience %s", ErrSignInMalformed, origin)
	}
	return nil
}

// FetchSignInRequest resolves a request by reference: GET the link asking for
// JSON, decode {"request": "<encoded>"}, and check the audience binding.
// It returns the encoded request as served, for signers that pass it on.
func FetchSignInRequest(ctx context.Context, client *http.Client, requestURI string) (string, SignInRequest, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURI, nil)
	if err != nil {
		return "", SignInRequest{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", SignInRequest{}, fmt.Errorf("fetch sign-in request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return "", SignInRequest{}, fmt.Errorf("%w: this sign-in code expired or was already used", ErrSignInExpired)
	}
	if resp.StatusCode != http.StatusOK {
		return "", SignInRequest{}, fmt.Errorf("fetch sign-in request: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Request string `json:"request"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRequestFetch)).Decode(&body); err != nil || body.Request == "" {
		return "", SignInRequest{}, fmt.Errorf("%w: the link did not answer with a request", ErrSignInMalformed)
	}
	decoded, err := DecodeSignInRequest(body.Request)
	if err != nil {
		return "", SignInRequest{}, err
	}
	if err := CheckSignInRequestURI(requestURI, decoded); err != nil {
		return "", SignInRequest{}, err
	}
	return body.Request, decoded, nil
}

// SignInReferenceDeepLink is the app link for a request by reference.
func SignInReferenceDeepLink(requestURI string) string {
	return "poweur://auth?request_uri=" + url.QueryEscape(requestURI)
}
