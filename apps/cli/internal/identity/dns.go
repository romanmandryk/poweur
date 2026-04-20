package identity

import (
	"context"
	"encoding/base64"
	"net"
	"strings"
)

func decodeBase64(value string) ([]byte, error) {
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.URLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.StdEncoding.DecodeString(value)
}

type DNSStatus struct {
	Identity        string   `json:"identity"`
	PublicKeyTXT    string   `json:"public_key_txt"`
	EncryptionKeyTXT string  `json:"encryption_key_txt,omitempty"`
	RelayHosts      []string `json:"relay_hosts"`
	CNAME           string   `json:"cname"`
}

func LookupDNS(ctx context.Context, identity string) (DNSStatus, error) {
	status := DNSStatus{Identity: identity}
	txtRecords, _ := net.DefaultResolver.LookupTXT(ctx, "_eurything."+identity)
	for _, record := range txtRecords {
		record = strings.TrimSpace(record)
		if strings.HasPrefix(record, "eurything-pubkey=") {
			status.PublicKeyTXT = record
			break
		}
	}

	encRecords, _ := net.DefaultResolver.LookupTXT(ctx, "_eurything-enc."+identity)
	for _, record := range encRecords {
		record = strings.TrimSpace(record)
		if strings.HasPrefix(record, "eurything-enckey=") {
			status.EncryptionKeyTXT = record
			break
		}
	}

	if cname, err := net.DefaultResolver.LookupCNAME(ctx, identity); err == nil {
		status.CNAME = strings.TrimSuffix(cname, ".")
	}
	if hosts, err := net.DefaultResolver.LookupHost(ctx, identity); err == nil {
		status.RelayHosts = hosts
	}
	return status, nil
}

// LookupEncryptionKey returns the raw X25519 public key bytes for the given identity,
// or nil if the identity has no published encryption key.
func LookupEncryptionKey(ctx context.Context, identity string) ([]byte, error) {
	records, err := net.DefaultResolver.LookupTXT(ctx, "_eurything-enc."+identity)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		record = strings.TrimSpace(record)
		if !strings.HasPrefix(record, "eurything-enckey=") {
			continue
		}
		parts := strings.SplitN(record, "=", 2)
		if len(parts) != 2 {
			continue
		}
		value := strings.TrimPrefix(parts[1], "x25519:")
		decoded, err := decodeBase64(value)
		if err != nil {
			continue
		}
		if len(decoded) == 32 {
			return decoded, nil
		}
	}
	return nil, nil
}
