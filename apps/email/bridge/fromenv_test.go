package bridge

import (
	"testing"
	"time"
)

// Every documented variable is actually read: a gap here is a setting that
// silently does nothing in production.
func TestFromEnvReadsEverySetting(t *testing.T) {
	for k, v := range map[string]string{
		"EMAIL_INBOUND": "lmtp", "EMAIL_SMTP_LISTEN": ":2526", "EMAIL_MX_HOSTNAME": "MX.example.org",
		"EMAIL_TLS_CERT_FILE": "/c", "EMAIL_TLS_KEY_FILE": "/k", "EMAIL_DKIM_SELECTOR": "s1", "EMAIL_DKIM_KEY_FILE": "/d",
		"EMAIL_SMTP_TLS_SERVER_NAME": "mail.example.org", "EMAIL_OUT_DAILY_LIMIT": "3", "EMAIL_OUT_SPACING": "7m",
		"EMAIL_OUT_SPACING_MAX": "1h", "EMAIL_OUT_GLOBAL_DAILY_LIMIT": "200", "EMAIL_OUT_MAX_PENDING": "4",
		"EMAIL_OUT_NEW_RECIPIENTS_DAILY": "6", "EMAIL_TRANSPORT": "none",
	} {
		t.Setenv(k, v)
	}
	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.Inbound != "lmtp" || c.SMTPListen != ":2526" || c.MXHostname != "mx.example.org" || c.SMTPTLSCert != "/c" || c.SMTPTLSKey != "/k" ||
		c.DKIMSelector != "s1" || c.DKIMKeyFile != "/d" || c.SMTPTLSServerName != "mail.example.org" || c.OutDailyLimit != 3 ||
		c.OutSpacing != 7*time.Minute || c.OutSpacingMax != time.Hour || c.OutGlobalDailyLimit != 200 || c.OutMaxPending != 4 ||
		c.OutNewRecipients != 6 || c.Transport != "none" {
		t.Fatalf("FromEnv = %+v", c)
	}
	t.Setenv("EMAIL_INBOUND", "")
	t.Setenv("EMAIL_OUT_DAILY_LIMIT", "")
	t.Setenv("EMAIL_OUT_SPACING", "")
	c, _ = FromEnv()
	if c.Inbound != "smtp" || c.OutDailyLimit != DefaultOutDailyLimit || c.OutSpacing != DefaultOutSpacing {
		t.Fatalf("defaults: %+v", c)
	}
}
