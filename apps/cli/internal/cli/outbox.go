package cli

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/poweur/cli/internal/config"
	idpkg "github.com/poweur/identity"
)

// outboxEntry is a complete, identity-signed envelope. Keeping the encrypted
// wire object means retry never needs plaintext or a live session.
type outboxEntry struct {
	Message      Message   `json:"message"`
	TargetRelay  string    `json:"target_relay"`
	ViaHomeRelay bool      `json:"via_home_relay,omitempty"`
	Attempts     int       `json:"attempts"`
	NextAttempt  time.Time `json:"next_attempt"`
	LastError    string    `json:"last_error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

func outboxPath(identityValue string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".poweur", "pending", identityValue+".outbox.json"), nil
}

func loadOutbox(identityValue string) ([]outboxEntry, error) {
	path, err := outboxPath(identityValue)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []outboxEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("read outbox: %w", err)
	}
	return entries, nil
}

func saveOutbox(identityValue string, entries []outboxEntry) error {
	path, err := outboxPath(identityValue)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".outbox-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func queueOutbox(identityValue, target string, msg Message, viaHome bool, cause error) error {
	entries, err := loadOutbox(identityValue)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	entries = append(entries, outboxEntry{
		Message: msg, TargetRelay: target, ViaHomeRelay: viaHome,
		NextAttempt: now, LastError: cause.Error(), CreatedAt: now,
	})
	return saveOutbox(identityValue, entries)
}

func queueOfflineSend(identityValue, target string, msg Message, viaHome bool, priv ed25519.PrivateKey,
	plaintext, useIdentity string, jsonOut bool, cause error, stdout, stderr io.Writer) int {
	// Sessions are short-lived and may be gone before reconnect. Persist an
	// identity-signed form of the exact encrypted envelope instead.
	msg.SessionID = ""
	msg.SessionProof = nil
	msg.Signature = signMessage(priv, msg)
	if err := queueOutbox(identityValue, target, msg, viaHome, cause); err != nil {
		recordSendFailure(identityValue, msg.ID, msg.Recipient, viaHome, "queue failed: "+err.Error())
		fmt.Fprintln(stderr, "could not queue message:", err)
		return 1
	}
	archiveRecords(useIdentity, []idpkg.HistoryRecord{historyRecordThreaded(
		identityValue, idpkg.HistoryQueueSent, msg.ID, identityValue, msg.Recipient,
		msg.Timestamp, msg.Type, msg.ThreadID, plaintext)}, stderr)
	output := map[string]any{
		"id": msg.ID, "status": "queued", "message": msg, "encrypted": true,
		"sign_with": "identity", "target_relay": target, "via_home_relay": viaHome,
	}
	return writeOutput(stdout, jsonOut, output,
		fmt.Sprintf("queued encrypted message to %s for retry (id=%s): %s\n", msg.Recipient, msg.ID, cause))
}

func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		return time.Second
	}
	delay := time.Second << min(attempt, 10)
	if delay > 15*time.Minute {
		return 15 * time.Minute
	}
	return delay
}

func retryOutboxForIdentity(ctx context.Context, identityValue string, force bool, stdout, stderr io.Writer) (sent, pending int) {
	entries, err := loadOutbox(identityValue)
	if err != nil {
		fmt.Fprintln(stderr, "outbox:", err)
		return 0, 0
	}
	now := time.Now().UTC()
	keep := make([]outboxEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Message.ExpiresAt != "" {
			if expiry, err := time.Parse(time.RFC3339, entry.Message.ExpiresAt); err == nil && !now.Before(expiry) {
				recordSendFailure(identityValue, entry.Message.ID, entry.Message.Recipient, entry.ViaHomeRelay, "expired in outbox")
				continue
			}
		}
		if !force && now.Before(entry.NextAttempt) {
			keep = append(keep, entry)
			continue
		}
		// Relay addresses can change while we are offline (deploys and hosted
		// migration). Follow DNS when it actually answered; rewrite a stored
		// bare-IP https URL even if we only have the identity name (TLS SNI).
		if cfg, err := config.Load(); err == nil {
			if entry.ViaHomeRelay && cfg.RelayURL != "" {
				entry.TargetRelay = cfg.RelayURL
			} else {
				scheme := schemeFromConfig(cfg)
				host, fromDNS, err := lookupRelayHost(ctx, scheme, entry.Message.Recipient)
				if err == nil && (fromDNS || relayURLHostIsBareIP(entry.TargetRelay)) {
					entry.TargetRelay = scheme + "://" + host
				}
			}
		}
		resp, sendErr := SendMessage(ctx, entry.TargetRelay, entry.Message)
		if sendErr == nil && resp.StatusCode < 400 {
			resp.Body.Close()
			recordTick1(identityValue, entry.Message.ID, entry.Message.Recipient, entry.ViaHomeRelay)
			fmt.Fprintf(stdout, "sent queued message to %s (id=%s, tick 1 ✓)\n", entry.Message.Recipient, entry.Message.ID)
			sent++
			continue
		}
		permanent := false
		if sendErr == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			sendErr = fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
			permanent = resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests
		}
		if permanent {
			recordSendFailure(identityValue, entry.Message.ID, entry.Message.Recipient, entry.ViaHomeRelay, sendErr.Error())
			fmt.Fprintf(stderr, "dropping queued message %s: %v\n", entry.Message.ID, sendErr)
			continue
		}
		entry.Attempts++
		entry.LastError = sendErr.Error()
		entry.NextAttempt = now.Add(retryDelay(entry.Attempts))
		keep = append(keep, entry)
	}
	if err := saveOutbox(identityValue, keep); err != nil {
		fmt.Fprintln(stderr, "outbox:", err)
	}
	return sent, len(keep)
}

func runOutbox(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	identityValue := cfg.Identity
	if identityValue == "" {
		fmt.Fprintln(stderr, "identity not configured")
		return 1
	}
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "list":
		entries, err := loadOutbox(identityValue)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].CreatedAt.Before(entries[j].CreatedAt) })
		if len(entries) == 0 {
			fmt.Fprintln(stdout, "outbox empty")
		}
		for _, entry := range entries {
			fmt.Fprintf(stdout, "%s -> %s attempts=%d next=%s last=%s\n", entry.Message.ID,
				entry.Message.Recipient, entry.Attempts, entry.NextAttempt.Format(time.RFC3339), entry.LastError)
		}
		return 0
	case "retry":
		sent, pending := retryOutboxForIdentity(context.Background(), identityValue, true, stdout, stderr)
		fmt.Fprintf(stdout, "outbox: sent=%d pending=%d\n", sent, pending)
		if pending > 0 {
			return 1
		}
		return 0
	default:
		fmt.Fprintln(stderr, "usage: poweur outbox [list|retry]")
		return 1
	}
}
