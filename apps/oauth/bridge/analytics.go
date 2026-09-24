package bridge

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

// Better Stack web analytics, opt-in through Config.AnalyticsToken.
//
// A script on a page can read it and press its buttons, so third-party code
// must never run where someone signs in or approves an application. Only the
// public information pages load it, and only they get the widened CSP.
var analyticsPages = map[string]bool{"home": true, "privacy": true, "security": true, "abuse": true}

const (
	analyticsScriptOrigin = "https://betterstack.net"
	analyticsDataOrigin   = "https://*.betterstackdata.com"
	analyticsTag          = `<script src="/analytics.js" async></script>`
)

var analyticsTokenRE = regexp.MustCompile(`^[A-Za-z0-9]{8,64}$`)

// analyticsFor reports whether a page loads analytics, switching its response
// to the widened CSP when it does.
func (s *Server) analyticsFor(w http.ResponseWriter, page string) bool {
	if s.cspAnalytics == "" || !analyticsPages[page] {
		return false
	}
	w.Header().Set("Content-Security-Policy", s.cspAnalytics)
	return true
}

// handleAnalytics serves Better Stack's loader from our own origin, so the
// pages keep script-src 'self' for everything but Better Stack's own script.
func (s *Server) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	if s.cspAnalytics == "" {
		http.NotFound(w, r)
		return
	}
	token, _ := json.Marshal(s.cfg.AnalyticsToken)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(strings.ReplaceAll(analyticsLoader, "TOKEN", string(token))))
}

const analyticsLoader = `!function(b,e,t,r){
  b[t]=b[t]||function(...args){(b[t].q=b[t].q||[]).push(args)};
  b[t].l=+new Date;
  var s=e.createElement('script'); s.async=1; s.crossOrigin='anonymous';
  s.src='` + analyticsScriptOrigin + `/b.js?t='+r;
  (e.head||e.getElementsByTagName('head')[0]).appendChild(s);
}(window,document,'betterstack',TOKEN);
betterstack('init', { environment: 'production' });
`
