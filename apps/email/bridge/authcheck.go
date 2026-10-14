package bridge

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"blitiri.com.ar/go/spf"
	"github.com/emersion/go-msgauth/dkim"
	"github.com/emersion/go-msgauth/dmarc"
	"golang.org/x/net/publicsuffix"

	"github.com/poweur/identity"
)

// Authenticator checks SPF, DKIM and DMARC itself — what a mail server in
// front would otherwise do — for the direct inbound mode, where the bridge
// is the MX. Its result replaces any Authentication-Results in the message.
type Authenticator struct {
	// LookupTXT resolves TXT records (tests inject one); nil = the system resolver.
	LookupTXT func(ctx context.Context, name string) ([]string, error)
}

type txtResolver struct {
	lookup func(ctx context.Context, name string) ([]string, error)
}

func (r txtResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return r.lookup(ctx, name)
}

func (r txtResolver) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	return net.DefaultResolver.LookupMX(ctx, name)
}

func (r txtResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

func (r txtResolver) LookupAddr(ctx context.Context, addr string) ([]string, error) {
	return net.DefaultResolver.LookupAddr(ctx, addr)
}

func (a Authenticator) txt(ctx context.Context) func(string) ([]string, error) {
	return func(name string) ([]string, error) {
		if a.LookupTXT != nil {
			return a.LookupTXT(ctx, name)
		}
		return net.DefaultResolver.LookupTXT(ctx, name)
	}
}

// Check authenticates one message from ip (said HELO helo, MAIL FROM
// mailFrom) whose From header domain is fromDomain.
func (a Authenticator) Check(ctx context.Context, ip net.IP, helo, mailFrom string, raw []byte, fromDomain string) identity.EmailAuth {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	auth := identity.EmailAuth{Verdict: identity.EmailUnverified}

	// SPF: the envelope sender's domain (or HELO for bounces) authorises ip.
	spfDomain := domainOf(mailFrom)
	if spfDomain == "" {
		spfDomain = strings.ToLower(helo)
	}
	opts := []spf.Option{spf.WithContext(ctx)}
	if a.LookupTXT != nil {
		opts = append(opts, spf.WithResolver(txtResolver{lookup: a.LookupTXT}))
	}
	sender := mailFrom
	if sender == "" {
		sender = "postmaster@" + spfDomain
	}
	if ip != nil && spfDomain != "" {
		result, _ := spf.CheckHostWithSender(ip, helo, sender, opts...)
		auth.SPF = string(result)
	} else {
		auth.SPF = "none"
	}

	// DKIM: every valid signature's domain.
	var dkimPassed []string
	verifications, err := dkim.VerifyWithOptions(bytes.NewReader(raw), &dkim.VerifyOptions{LookupTXT: a.txt(ctx), MaxVerifications: 5})
	switch {
	case len(verifications) == 0 && err == nil:
		auth.DKIM = "none"
	default:
		auth.DKIM = "fail"
		for _, v := range verifications {
			if v.Err == nil {
				auth.DKIM = "pass"
				dkimPassed = append(dkimPassed, strings.ToLower(v.Domain))
			} else if dkim.IsTempFail(v.Err) && auth.DKIM != "pass" {
				auth.DKIM = "temperror"
			}
		}
		if err != nil && len(verifications) == 0 {
			auth.DKIM = "permerror"
		}
	}

	// DMARC: an authenticated domain aligned with From.
	fromDomain = strings.ToLower(strings.TrimSpace(fromDomain))
	if fromDomain == "" {
		auth.DMARC = "none"
		return finishVerdict(auth)
	}
	record, policyDomain, err := lookupDMARC(fromDomain, a.txt(ctx))
	if err != nil {
		if errors.Is(err, dmarc.ErrNoPolicy) {
			auth.DMARC = "none"
		} else {
			auth.DMARC = "temperror"
		}
		return finishVerdict(auth)
	}
	_ = policyDomain
	aligned := false
	if auth.SPF == string(spf.Pass) && alignedDomains(spfDomain, fromDomain, record.SPFAlignment) {
		aligned = true
	}
	for _, d := range dkimPassed {
		if alignedDomains(d, fromDomain, record.DKIMAlignment) {
			aligned = true
		}
	}
	if aligned {
		auth.DMARC = "pass"
	} else {
		auth.DMARC = "fail"
	}
	return finishVerdict(auth)
}

// DMARCReject reports a failed message whose domain asks for rejection.
func (a Authenticator) DMARCReject(ctx context.Context, auth identity.EmailAuth, fromDomain string) bool {
	if auth.DMARC != "fail" {
		return false
	}
	record, _, err := lookupDMARC(strings.ToLower(fromDomain), a.txt(ctx))
	return err == nil && record.Policy == dmarc.PolicyReject
}

func finishVerdict(auth identity.EmailAuth) identity.EmailAuth {
	switch auth.DMARC {
	case "pass":
		auth.Verdict = identity.EmailVerified
	case "fail":
		auth.Verdict = identity.EmailFailed
	default:
		auth.Verdict = identity.EmailUnverified
	}
	return auth
}

// lookupDMARC finds the policy at the From domain, else at its
// organizational domain (RFC 7489 §6.6.3).
func lookupDMARC(domain string, txt func(string) ([]string, error)) (*dmarc.Record, string, error) {
	opts := &dmarc.LookupOptions{LookupTXT: txt}
	record, err := dmarc.LookupWithOptions(domain, opts)
	if err == nil {
		return record, domain, nil
	}
	if !errors.Is(err, dmarc.ErrNoPolicy) {
		return nil, "", err
	}
	org := orgDomain(domain)
	if org == domain {
		return nil, "", err
	}
	record, err = dmarc.LookupWithOptions(org, opts)
	return record, org, err
}

func orgDomain(domain string) string {
	if org, err := publicsuffix.EffectiveTLDPlusOne(domain); err == nil {
		return org
	}
	return domain
}

func alignedDomains(authenticated, from string, mode dmarc.AlignmentMode) bool {
	authenticated, from = strings.ToLower(authenticated), strings.ToLower(from)
	if mode == dmarc.AlignmentStrict {
		return authenticated == from
	}
	return orgDomain(authenticated) == orgDomain(from)
}
