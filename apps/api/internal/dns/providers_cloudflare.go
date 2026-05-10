package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/poweur/api/internal/config"
)

type CloudflareProvider struct {
	cfg     config.Config
	client  *http.Client
	baseURL string
}

func NewCloudflareProvider(cfg config.Config) *CloudflareProvider {
	return &CloudflareProvider{
		cfg:     cfg,
		client:  &http.Client{Timeout: 15 * time.Second},
		baseURL: "https://api.cloudflare.com/client/v4",
	}
}

func (p *CloudflareProvider) WriteIdentityRecords(ctx context.Context, token, identity, publicKey, encryptionPublicKey, relayAddress string) error {
	if token == "" {
		return errors.New("missing cloudflare api token")
	}
	zoneID, err := p.findZoneID(ctx, token, identity)
	if err != nil {
		return err
	}

	ttl := int(defaultTTL(p.cfg).Seconds())
	pubName := fmt.Sprintf("_poweur.%s", identity)
	pubValue := fmt.Sprintf("poweur-pubkey=ed25519:%s", publicKey)

	if err := p.upsertRecord(ctx, token, zoneID, "TXT", pubName, pubValue, ttl, false); err != nil {
		return err
	}

	if encryptionPublicKey != "" {
		encName := fmt.Sprintf("_poweur-enc.%s", identity)
		encValue := fmt.Sprintf("poweur-enckey=x25519:%s", encryptionPublicKey)
		if err := p.upsertRecord(ctx, token, zoneID, "TXT", encName, encValue, ttl, false); err != nil {
			return err
		}
	}

	recordType, relayHost := relayRecord(relayAddress)
	proxied := p.shouldProxyRelay(ctx, relayHost, token, zoneID)
	if err := p.upsertRecord(ctx, token, zoneID, recordType, identity, relayHost, ttl, proxied); err != nil {
		return err
	}

	return nil
}

func (p *CloudflareProvider) WriteEncryptionKey(ctx context.Context, token, identity, encryptionPublicKey string) error {
	if token == "" {
		return errors.New("missing cloudflare api token")
	}
	if encryptionPublicKey == "" {
		return errors.New("missing encryption public key")
	}
	zoneID, err := p.findZoneID(ctx, token, identity)
	if err != nil {
		return err
	}
	ttl := int(defaultTTL(p.cfg).Seconds())
	encName := fmt.Sprintf("_poweur-enc.%s", identity)
	encValue := fmt.Sprintf("poweur-enckey=x25519:%s", encryptionPublicKey)
	return p.upsertRecord(ctx, token, zoneID, "TXT", encName, encValue, ttl, false)
}

func (p *CloudflareProvider) findZoneID(ctx context.Context, token, identity string) (string, error) {
	labels := strings.Split(identity, ".")
	for i := 0; i < len(labels)-1; i++ {
		zoneName := strings.Join(labels[i:], ".")
		zoneID, err := p.lookupZone(ctx, token, zoneName)
		if err == nil && zoneID != "" {
			return zoneID, nil
		}
	}
	return "", errors.New("cloudflare zone not found for identity")
}

func (p *CloudflareProvider) lookupZone(ctx context.Context, token, name string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/zones?name=%s", p.baseURL, name), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var payload struct {
		Success bool `json:"success"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Result []struct {
			ID string `json:"id"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}

	if !payload.Success {
		if len(payload.Errors) > 0 {
			return "", errors.New(payload.Errors[0].Message)
		}
		return "", errors.New("cloudflare zone lookup failed")
	}

	if len(payload.Result) == 0 {
		return "", errors.New("cloudflare zone not found")
	}

	return payload.Result[0].ID, nil
}

func (p *CloudflareProvider) upsertRecord(ctx context.Context, token, zoneID, recordType, name, content string, ttl int, proxied bool) error {
	recordID, err := p.lookupRecord(ctx, token, zoneID, recordType, name)
	if err != nil && !errors.Is(err, errRecordNotFound) {
		return err
	}

	body, err := json.Marshal(map[string]any{
		"type":    recordType,
		"name":    name,
		"content": content,
		"ttl":     ttl,
		"proxied": proxied,
	})
	if err != nil {
		return err
	}

	var method, url string
	if recordID == "" {
		method = http.MethodPost
		url = fmt.Sprintf("%s/zones/%s/dns_records", p.baseURL, zoneID)
	} else {
		method = http.MethodPut
		url = fmt.Sprintf("%s/zones/%s/dns_records/%s", p.baseURL, zoneID, recordID)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var payload struct {
		Success bool `json:"success"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}
	if !payload.Success {
		if len(payload.Errors) > 0 {
			return errors.New(payload.Errors[0].Message)
		}
		return errors.New("cloudflare record write failed")
	}
	return nil
}

var errRecordNotFound = errors.New("record not found")

func (p *CloudflareProvider) lookupRecord(ctx context.Context, token, zoneID, recordType, name string) (string, error) {
	url := fmt.Sprintf("%s/zones/%s/dns_records?type=%s&name=%s", p.baseURL, zoneID, recordType, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var payload struct {
		Success bool `json:"success"`
		Result  []struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if !payload.Success || len(payload.Result) == 0 {
		return "", errRecordNotFound
	}
	return payload.Result[0].ID, nil
}

func (p *CloudflareProvider) shouldProxyRelay(ctx context.Context, relayHost string, token string, zoneID string) bool {
	switch p.cfg.DNSProxyMode {
	case "always":
		return true
	case "never":
		return false
	}

	// auto: proxy only if relay host points to a Cloudflare tunnel
	cname, err := p.lookupCNAME(ctx, relayHost, token, zoneID)
	if err != nil {
		return false
	}
	return strings.HasSuffix(cname, "cfargotunnel.com") || strings.Contains(cname, ".cfargotunnel.com")
}

func (p *CloudflareProvider) lookupCNAME(ctx context.Context, name string, token string, zoneID string) (string, error) {
	url := fmt.Sprintf("%s/zones/%s/dns_records?type=CNAME&name=%s", p.baseURL, zoneID, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var payload struct {
		Success bool `json:"success"`
		Result  []struct {
			Content string `json:"content"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if !payload.Success || len(payload.Result) == 0 {
		return "", errors.New("cname lookup failed")
	}
	return strings.TrimSuffix(payload.Result[0].Content, "."), nil
}
