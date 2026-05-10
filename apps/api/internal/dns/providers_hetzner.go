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

type HetznerProvider struct {
	cfg     config.Config
	client  *http.Client
	baseURL string
}

func NewHetznerProvider(cfg config.Config) *HetznerProvider {
	return &HetznerProvider{
		cfg:     cfg,
		client:  &http.Client{Timeout: 15 * time.Second},
		baseURL: "https://dns.hetzner.com/api/v1",
	}
}

func (p *HetznerProvider) WriteIdentityRecords(ctx context.Context, token, identity, publicKey, encryptionPublicKey, relayAddress string) error {
	if token == "" {
		return errors.New("missing hetzner api token")
	}
	zoneID, err := p.findZoneID(ctx, token, identity)
	if err != nil {
		return err
	}

	ttl := int(defaultTTL(p.cfg).Seconds())
	pubName := fmt.Sprintf("_poweur.%s", identity)
	pubValue := fmt.Sprintf("poweur-pubkey=ed25519:%s", publicKey)

	if err := p.upsertRecord(ctx, token, zoneID, "TXT", pubName, pubValue, ttl); err != nil {
		return err
	}

	if encryptionPublicKey != "" {
		encName := fmt.Sprintf("_poweur-enc.%s", identity)
		encValue := fmt.Sprintf("poweur-enckey=x25519:%s", encryptionPublicKey)
		if err := p.upsertRecord(ctx, token, zoneID, "TXT", encName, encValue, ttl); err != nil {
			return err
		}
	}

	recordType, relayHost := relayRecord(relayAddress)
	if err := p.upsertRecord(ctx, token, zoneID, recordType, identity, relayHost, ttl); err != nil {
		return err
	}
	return nil
}

func (p *HetznerProvider) WriteEncryptionKey(ctx context.Context, token, identity, encryptionPublicKey string) error {
	if token == "" {
		return errors.New("missing hetzner api token")
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
	return p.upsertRecord(ctx, token, zoneID, "TXT", encName, encValue, ttl)
}

func (p *HetznerProvider) findZoneID(ctx context.Context, token, identity string) (string, error) {
	labels := strings.Split(identity, ".")
	for i := 0; i < len(labels)-1; i++ {
		zoneName := strings.Join(labels[i:], ".")
		zoneID, err := p.lookupZone(ctx, token, zoneName)
		if err == nil && zoneID != "" {
			return zoneID, nil
		}
	}
	return "", errors.New("hetzner zone not found for identity")
}

func (p *HetznerProvider) lookupZone(ctx context.Context, token, name string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/zones", p.baseURL), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Auth-API-Token", token)

	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var payload struct {
		Zones []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"zones"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	for _, zone := range payload.Zones {
		if strings.EqualFold(zone.Name, name) {
			return zone.ID, nil
		}
	}
	return "", errors.New("hetzner zone not found")
}

func (p *HetznerProvider) upsertRecord(ctx context.Context, token, zoneID, recordType, name, value string, ttl int) error {
	recordID, err := p.lookupRecord(ctx, token, zoneID, recordType, name)
	if err != nil && !errors.Is(err, errRecordNotFound) {
		return err
	}

	body, err := json.Marshal(map[string]any{
		"zone_id": zoneID,
		"type":    recordType,
		"name":    name,
		"value":   value,
		"ttl":     ttl,
	})
	if err != nil {
		return err
	}

	var method, url string
	if recordID == "" {
		method = http.MethodPost
		url = fmt.Sprintf("%s/records", p.baseURL)
	} else {
		method = http.MethodPut
		url = fmt.Sprintf("%s/records/%s", p.baseURL, recordID)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Auth-API-Token", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("hetzner record write failed with status %d", resp.StatusCode)
	}
	return nil
}

func (p *HetznerProvider) lookupRecord(ctx context.Context, token, zoneID, recordType, name string) (string, error) {
	url := fmt.Sprintf("%s/records?zone_id=%s", p.baseURL, zoneID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Auth-API-Token", token)

	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var payload struct {
		Records []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"records"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	for _, record := range payload.Records {
		if strings.EqualFold(record.Type, recordType) && strings.EqualFold(record.Name, name) {
			return record.ID, nil
		}
	}
	return "", errRecordNotFound
}
