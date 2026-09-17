package signin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/poweur/identity"
)

// SignInContext is what a relying party tells a signer about where a pending
// sign-in was started. It is disclosure, not authentication: it lets a person
// approving on one device notice that the sign-in did not begin in front of
// them. It carries nothing a holder of the request id could not already
// observe, and never a precise location.
type SignInContext struct {
	RequestID string    `json:"request_id"`
	StartedAt time.Time `json:"started_at"`
	// Browser is a coarse label such as "Chrome on macOS".
	Browser string `json:"browser,omitempty"`
	// Client names the application the sign-in is for, when the RP is a
	// broker (the OAuth bridge) rather than the application itself.
	Client     string `json:"client,omitempty"`
	ClientHost string `json:"client_host,omitempty"`
}

// MaxContextBytes caps a context document.
const MaxContextBytes = 4 * 1024

// FetchContext retrieves the context of a pending request from the RP's
// published context_uri. It returns (zero, nil) when the RP publishes none.
func FetchContext(ctx context.Context, meta Metadata, requestID string, opts FetchOptions) (SignInContext, error) {
	if meta.ContextURI == "" {
		return SignInContext{}, nil
	}
	if !identity.SameOrigin(meta.Origin, meta.ContextURI) {
		return SignInContext{}, fmt.Errorf("%w: context_uri is not same-origin", identity.ErrSignInAudience)
	}
	u, err := url.Parse(meta.ContextURI)
	if err != nil {
		return SignInContext{}, err
	}
	q := u.Query()
	q.Set("request_id", requestID)
	u.RawQuery = q.Encode()
	client := opts.HTTPClient
	if client == nil {
		timeout := opts.Timeout
		if timeout == 0 {
			timeout = identity.DefaultTimeout
		}
		client = &http.Client{
			Timeout:       timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirects are not followed") },
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return SignInContext{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return SignInContext{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return SignInContext{}, fmt.Errorf("sign-in context returned %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxContextBytes+1))
	if err != nil {
		return SignInContext{}, err
	}
	if len(raw) > MaxContextBytes {
		return SignInContext{}, errors.New("sign-in context too large")
	}
	var c SignInContext
	if err := json.Unmarshal(raw, &c); err != nil {
		return SignInContext{}, fmt.Errorf("sign-in context is not JSON: %w", err)
	}
	if c.RequestID != requestID {
		return SignInContext{}, errors.New("sign-in context is for another request")
	}
	c.Browser = displayLine(c.Browser, 60)
	c.Client = displayLine(c.Client, 80)
	c.ClientHost = displayLine(c.ClientHost, 253)
	return c, nil
}

// DescribeContext renders a context as the sentence a signer shows.
func DescribeContext(c SignInContext, now time.Time) string {
	if c.StartedAt.IsZero() {
		return ""
	}
	var b strings.Builder
	b.WriteString("Started ")
	b.WriteString(ago(now.Sub(c.StartedAt)))
	if c.Browser != "" {
		b.WriteString(" in ")
		b.WriteString(c.Browser)
	}
	if c.Client != "" {
		b.WriteString(", to sign in to ")
		b.WriteString(c.Client)
		if c.ClientHost != "" && !strings.EqualFold(c.ClientHost, c.Client) {
			b.WriteString(" (" + c.ClientHost + ")")
		}
	}
	b.WriteString(".")
	return b.String()
}

func ago(d time.Duration) string {
	switch {
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%d seconds ago", int(d.Seconds()))
	case d < 2*time.Minute:
		return "a minute ago"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	default:
		return "over an hour ago"
	}
}

// displayLine keeps an RP-supplied label to one printable line.
func displayLine(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if len(s) > max {
		s = s[:max]
	}
	return s
}
