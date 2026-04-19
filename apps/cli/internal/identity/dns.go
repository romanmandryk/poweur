package identity

import (
	"context"
	"net"
	"strings"
)

type DNSStatus struct {
	Identity     string   `json:"identity"`
	PublicKeyTXT string   `json:"public_key_txt"`
	RelayHosts   []string `json:"relay_hosts"`
	CNAME        string   `json:"cname"`
}

func LookupDNS(ctx context.Context, identity string) (DNSStatus, error) {
	status := DNSStatus{Identity: identity}
	txtName := "_eurything." + identity
	txtRecords, _ := net.DefaultResolver.LookupTXT(ctx, txtName)
	for _, record := range txtRecords {
		record = strings.TrimSpace(record)
		if strings.HasPrefix(record, "eurything-pubkey=") {
			status.PublicKeyTXT = record
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
