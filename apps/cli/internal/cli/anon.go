package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/poweur/cli/internal/config"
	cryptoe2e "github.com/poweur/cli/internal/crypto"
	"github.com/poweur/cli/internal/identity"
	idpkg "github.com/poweur/identity"
)

// Anonymous sending & receiving (EPIC-014 E14-T4). Sending needs no Poweur
// identity at all: the message is E2E-encrypted to the recipient with an
// ephemeral key, unsigned, and gated by whatever challenge the recipient's
// policy demands (v1: proof-of-work, solved locally).

// challengeEnvelope mirrors the relay's 428 response.
type anonChallengeEnvelope struct {
	Error     string `json:"error"`
	Challenge struct {
		Type      string `json:"type"`
		Algo      string `json:"algo"`
		Token     string `json:"token"`
		Bits      int    `json:"bits"`
		ExpiresAt string `json:"expires_at"`
		Detail    string `json:"detail"`
	} `json:"challenge"`
}

// runSendAnon posts an unsigned message, solving a PoW challenge when the
// recipient requires one.
func runSendAnon(cfg config.Config, recipient, plaintext string, jsonOut bool, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	recipientEncPub, err := identity.LookupEncryptionKey(ctx, recipient)
	cancel()
	if err != nil {
		fmt.Fprintf(stderr, "cannot look up recipient encryption key for %s: %v\n", recipient, err)
		return 1
	}
	if len(recipientEncPub) != 32 {
		fmt.Fprintf(stderr, "recipient %s has no published encryption key; anonymous messages must still be encrypted\n", recipient)
		return 1
	}
	sealed, err := cryptoe2e.Encrypt(recipientEncPub, []byte(plaintext))
	if err != nil {
		fmt.Fprintln(stderr, "encrypt:", err)
		return 1
	}
	targetURL, err := resolveRecipientRelayURL(context.Background(), recipient, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "cannot resolve recipient relay for %s: %v\n", recipient, err)
		return 1
	}

	messageID := identity.NewMessageID()
	msg := map[string]any{
		"id":        messageID,
		"recipient": recipient,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"payload":   sealed.Ciphertext,
		"encryption": map[string]string{
			"alg":                  cryptoe2e.AlgName,
			"ephemeral_public_key": sealed.EphemeralPublicKey,
			"nonce":                sealed.Nonce,
		},
	}

	post := func() (*http.Response, error) {
		body, err := json.Marshal(msg)
		if err != nil {
			return nil, err
		}
		return http.Post(targetURL+"/messages", "application/json", bytes.NewReader(body))
	}

	resp, err := post()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if resp.StatusCode == http.StatusPreconditionRequired {
		var env anonChallengeEnvelope
		decodeErr := json.NewDecoder(resp.Body).Decode(&env)
		resp.Body.Close()
		if decodeErr != nil {
			fmt.Fprintln(stderr, "malformed challenge from relay:", decodeErr)
			return 1
		}
		switch env.Challenge.Type {
		case idpkg.AnonChallengePow:
			fmt.Fprintf(stderr, "recipient requires proof-of-work (%d bits, ~2^%d hashes) — solving…\n",
				env.Challenge.Bits, env.Challenge.Bits)
			start := time.Now()
			solveCtx, cancelSolve := context.WithTimeout(context.Background(), 10*time.Minute)
			solution, err := idpkg.SolvePow(solveCtx, env.Challenge.Token, env.Challenge.Bits)
			cancelSolve()
			if err != nil {
				fmt.Fprintln(stderr, "solving challenge:", err)
				return 1
			}
			fmt.Fprintf(stderr, "solved in %s\n", time.Since(start).Round(time.Millisecond))
			msg["challenge_token"] = env.Challenge.Token
			msg["challenge_solution"] = solution
			resp, err = post()
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
		default:
			fmt.Fprintf(stderr, "recipient requires a %q challenge, which this client cannot satisfy yet (%s)\n",
				env.Challenge.Type, env.Challenge.Detail)
			return 1
		}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		fmt.Fprintf(stderr, "relay rejected anonymous message (%d): %s\n", resp.StatusCode, bytes.TrimSpace(raw))
		return 1
	}
	return writeOutput(stdout, jsonOut, map[string]any{
		"id": messageID, "status": resp.StatusCode, "anonymous": true, "target_relay": targetURL,
	}, fmt.Sprintf("sent anonymous encrypted message to %s (id=%s)\n", recipient, messageID))
}

// runAnon drains the identity's anonymous queue and decrypts the payloads.
func runAnon(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("anon", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	cfg, identityValue, priv, ok := loadIdentityForDAV(*useIdentity, stderr)
	if !ok {
		return 1
	}
	raw, err := challengeSignedGet(context.Background(), cfg, identityValue, priv, "/anon/")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var out struct {
		Messages []anonQueueMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	messages := out.Messages
	if *jsonOut {
		return writeOutput(stdout, true, map[string]any{"messages": messages}, "")
	}
	if len(messages) == 0 {
		fmt.Fprintln(stdout, "no anonymous messages")
		return 0
	}
	encPriv, _ := identity.LoadEncryptionPrivateKey(identity.EncryptionKeyPath(cfg.KeysDir, identityValue))
	for _, m := range messages {
		display := "[encrypted: no local encryption key]"
		if encPriv != nil && m.Encryption != nil {
			plaintext, err := cryptoe2e.Decrypt(encPriv, cryptoe2e.EncryptedPayload{
				Ciphertext:         m.Payload,
				EphemeralPublicKey: m.Encryption.EphemeralPublicKey,
				Nonce:              m.Encryption.Nonce,
			})
			if err != nil {
				display = "[decrypt failed: " + err.Error() + "]"
			} else {
				display = string(plaintext)
			}
		}
		fmt.Fprintf(stdout, "ANONYMOUS\t%s\t%s\n", m.Timestamp, display)
	}
	fmt.Fprintln(stdout, "\nnote: anonymous messages are unauthenticated — treat content accordingly")
	return 0
}

// solveRegistrationPow fetches and solves the relay's registration
// challenge (REGISTRATION_GATE=pow, E14-T5).
func solveRegistrationPow(ctx context.Context, relayURL string, stderr io.Writer) (token, solution string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, relayURL+"/auth/pow?purpose=registration", nil)
	if err != nil {
		return "", "", err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("registration challenge request failed: HTTP %d", resp.StatusCode)
	}
	var ch struct {
		Token string `json:"token"`
		Bits  int    `json:"bits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil || ch.Token == "" {
		return "", "", fmt.Errorf("malformed registration challenge")
	}
	fmt.Fprintf(stderr, "relay requires proof-of-work for registration (%d bits) — solving…\n", ch.Bits)
	start := time.Now()
	solveCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	solution, err = idpkg.SolvePow(solveCtx, ch.Token, ch.Bits)
	if err != nil {
		return "", "", err
	}
	fmt.Fprintf(stderr, "solved in %s\n", time.Since(start).Round(time.Millisecond))
	return ch.Token, solution, nil
}

// anonQueueMessage mirrors the relay's stored anon envelope.
type anonQueueMessage struct {
	ID         string `json:"id"`
	Timestamp  string `json:"timestamp"`
	Payload    string `json:"payload"`
	Encryption *struct {
		Alg                string `json:"alg"`
		EphemeralPublicKey string `json:"ephemeral_public_key"`
		Nonce              string `json:"nonce"`
	} `json:"encryption"`
}
