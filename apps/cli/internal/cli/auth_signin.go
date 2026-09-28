package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
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
	matchCode := fs.String("code", "", "the code shown by the screen that started the sign-in, when approving from another device")
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true, "--no-deliver": true})); err != nil {
		return 1
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: poweur auth approve <request|link|file> [--code=<digits>] [--sign-with=session|identity] [--no-deliver]")
		return 1
	}
	identityValue := resolveIdentity(*useIdentity, cfg.Identity)
	if identityValue == "" || cfg.KeysDir == "" || cfg.RelayURL == "" {
		fmt.Fprintln(stderr, "identity and relay must be configured")
		return 1
	}
	// A request by reference is what a QR or a copied link carries, so the
	// screen that started the sign-in is elsewhere: its code is required.
	if _, byReference := idpkg.SignInRequestURI(fs.Arg(0)); byReference && *matchCode == "" && !*noDeliver {
		fmt.Fprintln(stderr, "this sign-in was started on another screen: add --code with the digits it shows")
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
	if sc, err := signinpkg.FetchContext(ctx, metadata, req.RequestID, signinpkg.FetchOptions{}); err == nil {
		if line := signinpkg.DescribeContext(sc, time.Now()); line != "" {
			fmt.Fprintln(stderr, line)
			if *matchCode != "" {
				fmt.Fprintln(stderr, "Approve only if that is the screen in front of you.")
			}
		}
	}
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
	var receipt signinpkg.DeliveryReceipt
	if req.ResponseURI != "" && !*noDeliver {
		receipt, err = deliverSignInApproval(ctx, req.ResponseURI, signinpkg.Delivery{
			Response: encoded, Match: signinpkg.NormalizeMatchCode(*matchCode),
		})
		if err != nil {
			fmt.Fprintf(stderr, "approval was signed but delivery failed: %v\n", err)
			return 1
		}
		if err := signinpkg.CheckResumeURI(req.Audience, receipt.ResumeURI); err != nil {
			fmt.Fprintf(stderr, "the relying party answered with an unsafe resume link: %v\n", err)
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
		if receipt.ResumeURI != "" {
			// A same-device approval finishes only in the browser that started
			// it; the link is useless anywhere else, so it is safe to print.
			payload["resume_uri"] = receipt.ResumeURI
			message += "open this in the browser where you started signing in:\n" + receipt.ResumeURI + "\n"
		}
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
// a web-signer URL, a local file, or a request by reference (E08-T6): the
// RP's short link (what its QR shows), fetched and required to live at the
// request's own audience.
func readSignInRequest(source string) (idpkg.SignInRequest, error) {
	value := strings.TrimSpace(source)
	if ref, ok := idpkg.SignInRequestURI(value); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, req, err := idpkg.FetchSignInRequest(ctx, &http.Client{
			Timeout:       10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}, ref)
		return req, err
	}
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
	raw, err := os.ReadFile(value)
	if err != nil {
		return idpkg.SignInRequest{}, err
	}
	if len(raw) > idpkg.MaxDocumentBytes {
		return idpkg.SignInRequest{}, fmt.Errorf("sign-in request is too large")
	}
	return idpkg.DecodeSignInRequest(string(raw))
}

// deliverSignInApproval POSTs a delivery to response_uri and returns the
// relying party's receipt. An RP that predates receipts answers with an empty
// or non-JSON body, which reads as a receipt with no resume link.
func deliverSignInApproval(ctx context.Context, responseURI string, delivery signinpkg.Delivery) (signinpkg.DeliveryReceipt, error) {
	body, err := json.Marshal(delivery)
	if err != nil {
		return signinpkg.DeliveryReceipt{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, responseURI, bytes.NewReader(body))
	if err != nil {
		return signinpkg.DeliveryReceipt{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{
		Timeout: 10 * time.Second,
		// The approval goes to the published response_uri and nowhere else.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return signinpkg.DeliveryReceipt{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return signinpkg.DeliveryReceipt{}, parseErrorResponse("sign-in callback failed", resp)
	}
	var receipt signinpkg.DeliveryReceipt
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if err != nil {
		return signinpkg.DeliveryReceipt{}, err
	}
	_ = json.Unmarshal(raw, &receipt)
	return receipt, nil
}

func appendConsentRecord(ctx context.Context, cfg config.Config, identityValue string, priv ed25519.PrivateKey, record idpkg.AuthLogRecord) error {
	existing, status, err := readSysFile(ctx, cfg.RelayURL, identityValue, priv, idpkg.AuthLogPath)
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
	err = writeSysFile(ctx, cfg.RelayURL, identityValue, priv, idpkg.AuthLogPath, next)
	if errors.Is(err, errStorageUnavailable) {
		// Until storage v2 lands there is nowhere to keep the log; the
		// approval itself does not depend on it.
		return nil
	}
	return err
}
