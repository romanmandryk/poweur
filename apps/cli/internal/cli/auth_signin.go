package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/poweur/cli/internal/config"
	cliidentity "github.com/poweur/cli/internal/identity"
	"github.com/poweur/cli/internal/session"
	idpkg "github.com/poweur/identity"
	signinpkg "github.com/poweur/identity/signin"
)

// runAuthApprove is the CLI signer for Sign in with Poweur ID. The command
// itself is the explicit approval gesture: before signing it verifies the RP
// metadata at the signed audience and prints the same human-readable scope
// descriptions the web consent screen uses.
func runAuthApprove(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("auth approve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity approving the request")
	signWith := fs.String("sign-with", "session", "sign with session (default) or identity key")
	noDeliver := fs.Bool("no-deliver", false, "print the response code instead of POSTing it to response_uri")
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true, "--no-deliver": true})); err != nil {
		return 1
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: poweur auth approve <request|link|file> [--sign-with=session|identity] [--no-deliver]")
		return 1
	}
	identityValue := resolveIdentity(*useIdentity, cfg.Identity)
	if identityValue == "" || cfg.KeysDir == "" || cfg.RelayURL == "" {
		fmt.Fprintln(stderr, "identity and relay must be configured")
		return 1
	}
	req, err := readSignInRequest(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	metadata, err := signinpkg.FetchMetadata(ctx, req.Audience, signinpkg.FetchOptions{})
	if err != nil {
		fmt.Fprintf(stderr, "cannot verify relying party metadata at %s: %v\n", req.Audience, err)
		return 1
	}
	if !metadata.AllowsResponseURI(req.ResponseURI) {
		fmt.Fprintln(stderr, "the request response_uri is not published by the relying party")
		return 1
	}
	fmt.Fprintln(stderr, signinpkg.SummarizeRequest(req, metadata.Name))
	for _, line := range signinpkg.DescribeScopes(req.Scopes, metadata.Name) {
		fmt.Fprintln(stderr, "- "+line)
	}

	identityPriv, err := cliidentity.LoadPrivateKey(cliidentity.KeyPath(cfg.KeysDir, identityValue))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	opts := signinpkg.SignOptions{Identity: identityValue}
	switch strings.ToLower(strings.TrimSpace(*signWith)) {
	case "identity":
		opts.PrivateKey = identityPriv
	case "session", "":
		sess, err := ensureSession(ctx, cfg.RelayURL, identityValue, identityPriv)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		sessionPriv, err := sess.PrivateKey()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		opts.PrivateKey = sessionPriv
		opts.SessionID = sess.SessionID
		opts.SessionProof = signInSessionProof(sess)
	default:
		fmt.Fprintln(stderr, "--sign-with must be session or identity")
		return 1
	}
	approval, err := signinpkg.Sign(req, opts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	encoded, err := idpkg.EncodeSignInResponse(approval)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	record := idpkg.NewAuthLogRecord(approval, metadata.Name, "cli", true, time.Now())
	if err := appendConsentRecord(ctx, cfg, identityValue, identityPriv, record); err != nil {
		// The approval remains valid; losing the user's audit record is visible
		// and makes the command fail instead of silently claiming completion.
		fmt.Fprintf(stderr, "approval was signed but consent log could not be saved: %v\nresponse: %s\n", err, encoded)
		return 1
	}

	delivered := false
	if req.ResponseURI != "" && !*noDeliver {
		if err := deliverSignInApproval(ctx, req.ResponseURI, encoded); err != nil {
			fmt.Fprintf(stderr, "approval was signed but delivery failed: %v\nresponse: %s\n", err, encoded)
			return 1
		}
		delivered = true
	}
	payload := map[string]any{
		"identity": identityValue, "request_id": approval.RequestID,
		"response": encoded, "delivered": delivered,
	}
	message := encoded + "\n"
	if delivered {
		message = fmt.Sprintf("approved sign-in for %s and delivered it to %s\n", metadata.Name, req.ResponseURI)
	}
	return writeOutput(stdout, *jsonOut, payload, message)
}

func signInSessionProof(sess session.Session) *idpkg.SignInSessionProof {
	if sess.SessionID == "" || sess.SessionPublicKey == "" || sess.IssuedAtRaw == "" ||
		sess.ExpiresAtRaw == "" || sess.Nonce == "" || sess.IdentitySignature == "" {
		return nil
	}
	return &idpkg.SignInSessionProof{
		SessionPublicKey: sess.SessionPublicKey, IssuedAt: sess.IssuedAtRaw,
		ExpiresAt: sess.ExpiresAtRaw, Nonce: sess.Nonce,
		IdentitySignature: sess.IdentitySignature,
	}
}

// readSignInRequest accepts the wire code, raw JSON, a poweur:// deep link,
// a web-signer URL, a local file, or an HTTP endpoint returning any of those.
func readSignInRequest(source string) (idpkg.SignInRequest, error) {
	value := strings.TrimSpace(source)
	if u, err := url.Parse(value); err == nil && u.Scheme != "" {
		for _, key := range []string{"request", "auth"} {
			if encoded := strings.TrimSpace(u.Query().Get(key)); encoded != "" {
				return idpkg.DecodeSignInRequest(encoded)
			}
		}
	}
	if req, err := idpkg.DecodeSignInRequest(value); err == nil {
		return req, nil
	}
	var raw []byte
	var err error
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		resp, getErr := (&http.Client{Timeout: 10 * time.Second}).Get(value)
		if getErr != nil {
			return idpkg.SignInRequest{}, getErr
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return idpkg.SignInRequest{}, fmt.Errorf("request fetch failed with status %d", resp.StatusCode)
		}
		raw, err = io.ReadAll(io.LimitReader(resp.Body, idpkg.MaxDocumentBytes+1))
	} else {
		raw, err = os.ReadFile(value)
	}
	if err != nil {
		return idpkg.SignInRequest{}, err
	}
	if len(raw) > idpkg.MaxDocumentBytes {
		return idpkg.SignInRequest{}, fmt.Errorf("sign-in request is too large")
	}
	return idpkg.DecodeSignInRequest(string(raw))
}

func deliverSignInApproval(ctx context.Context, responseURI, encoded string) error {
	body, err := json.Marshal(map[string]string{"response": encoded})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, responseURI, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return parseErrorResponse("sign-in callback failed", resp)
	}
	return nil
}

func appendConsentRecord(ctx context.Context, cfg config.Config, identityValue string, priv ed25519.PrivateKey, record idpkg.AuthLogRecord) error {
	tok, err := MintDAVToken(ctx, cfg.RelayURL, identityValue, "", "dav:full", priv)
	if err != nil {
		return err
	}
	existing, status, err := davGetBytes(ctx, cfg.RelayURL, identityValue, tok.Token, idpkg.AuthLogPath)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		existing = nil
	} else if status != http.StatusOK {
		return fmt.Errorf("read consent log: HTTP %d", status)
	}
	next, err := idpkg.AppendAuthLog(existing, record)
	if err != nil {
		return err
	}
	if err := ensureDAVPath(ctx, cfg.RelayURL, identityValue, tok.Token, "poweur-sys/private/logs"); err != nil {
		return err
	}
	return davPutBytes(ctx, cfg.RelayURL, identityValue, tok.Token, idpkg.AuthLogPath, next)
}

func ensureDAVPath(ctx context.Context, relayURL, identityValue, token, treePath string) error {
	var current string
	for _, part := range strings.Split(strings.Trim(treePath, "/"), "/") {
		if current == "" {
			current = part
		} else {
			current += "/" + part
		}
		req, err := http.NewRequestWithContext(ctx, "MKCOL", davFileURL(relayURL, identityValue, current), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 && resp.StatusCode != http.StatusMethodNotAllowed {
			return fmt.Errorf("create /%s: HTTP %d", current, resp.StatusCode)
		}
	}
	return nil
}
