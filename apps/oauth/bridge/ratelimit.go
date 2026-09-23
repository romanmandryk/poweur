package bridge

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Rate limits guard what a stranger can make the bridge do: identify fetches
// documents from hosts they choose, callbacks run signature checks, the token
// endpoint compares secrets, and the console writes to the registry.
type RateLimits struct {
	// Per client IP, per minute. Zero uses the default; negative disables.
	Authorize int // /authorize and /login — default 60
	Identify  int // POST /t/{id}/identify — default 20
	Callback  int // POST /poweur/callback — default 60
	Token     int // /token, /revoke, /introspect, /userinfo — default 120
	Console   int // POST /developers/... and /account/... — default 30
}

func (r RateLimits) withDefaults() RateLimits {
	def := func(v, d int) int {
		if v == 0 {
			return d
		}
		return v
	}
	return RateLimits{
		Authorize: def(r.Authorize, 60),
		Identify:  def(r.Identify, 20),
		Callback:  def(r.Callback, 60),
		Token:     def(r.Token, 120),
		Console:   def(r.Console, 30),
	}
}

// limiter is a fixed-window counter per key. The bridge runs as one process
// in v1; a shared store replaces this when it does not.
type limiter struct {
	mu     sync.Mutex
	perMin int
	window time.Time
	counts map[string]int
	now    func() time.Time
}

func newLimiter(perMin int, now func() time.Time) *limiter {
	return &limiter{perMin: perMin, counts: map[string]int{}, now: now}
}

// allow counts one request for key and reports whether it is within the
// limit, and if not, how many seconds until the window resets.
func (l *limiter) allow(key string) (bool, int) {
	if l == nil || l.perMin < 0 {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	window := now.Truncate(time.Minute)
	if !window.Equal(l.window) {
		l.window = window
		l.counts = map[string]int{}
	}
	l.counts[key]++
	if l.counts[key] > l.perMin {
		return false, int(window.Add(time.Minute).Sub(now).Seconds()) + 1
	}
	return true, 0
}

type limiters struct {
	authorize, identify, callback, token, console *limiter
}

func newLimiters(r RateLimits, now func() time.Time) limiters {
	r = r.withDefaults()
	return limiters{
		authorize: newLimiter(r.Authorize, now),
		identify:  newLimiter(r.Identify, now),
		callback:  newLimiter(r.Callback, now),
		token:     newLimiter(r.Token, now),
		console:   newLimiter(r.Console, now),
	}
}

// pick chooses the limiter for a request, or nil.
func (l limiters) pick(r *http.Request) *limiter {
	path := r.URL.Path
	switch {
	case path == "/authorize" || path == "/login" || strings.HasPrefix(path, "/r/"):
		return l.authorize
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/t/") && strings.HasSuffix(path, "/identify"):
		return l.identify
	case r.Method == http.MethodPost && path == "/poweur/callback":
		return l.callback
	case path == "/token" || path == "/revoke" || path == "/introspect" || path == "/userinfo":
		return l.token
	case r.Method == http.MethodPost && (strings.HasPrefix(path, "/developers") || strings.HasPrefix(path, "/account")):
		return l.console
	}
	return nil
}

// clientIP is the address a limit applies to. X-Forwarded-For is believed
// only when the operator says a proxy sets it, and then only its last hop —
// the one the proxy itself appended.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxyHeaders {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); ip != nil {
				return ip.String()
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// rateLimited answers 429 when the request is over its limit.
func (s *Server) rateLimited(w http.ResponseWriter, r *http.Request) bool {
	l := s.limits.pick(r)
	if l == nil {
		return false
	}
	ok, retry := l.allow(s.clientIP(r))
	if ok {
		return false
	}
	s.metrics.limited(rateRoute(r))
	w.Header().Set("Retry-After", strconv.Itoa(retry))
	if strings.HasPrefix(r.URL.Path, "/t/") || r.URL.Path == "/authorize" && r.Method == http.MethodGet ||
		r.URL.Path == "/login" || strings.HasPrefix(r.URL.Path, "/developers") || strings.HasPrefix(r.URL.Path, "/account") {
		s.renderError(w, r, http.StatusTooManyRequests, "Too many attempts from your network. Wait a minute and try again.")
		return true
	}
	writeOAuthError(w, http.StatusTooManyRequests, "slow_down", "too many requests; retry later")
	return true
}
