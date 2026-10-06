// Package hello is the demo bot behind hello.poweur.net: an ordinary Poweur ID
// that answers a text message with a short, fixed reply that proves the round
// trip worked. It is deliberately not a chatbot: every reply is one of a few
// canned strings plus facts the bot itself checked (the sender's ID, the time
// the message took), so nobody can make it say something of their choosing,
// and it costs nothing to run.
//
// It drives the `poweur` CLI in-process (listen, send), so it needs exactly
// what the CLI needs: RELAY_URL, IDENTITY and KEYS_DIR (or ~/.poweur).
package hello

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/poweur/cli/pkg/cli"
	"github.com/poweur/demoapps/appmetrics"
	idpkg "github.com/poweur/identity"
)

// Options configures Serve. Only Identity is required.
type Options struct {
	// Identity is the bot's own ID, e.g. hello.poweur.net.
	Identity string
	// Log receives one line per handled message; nil discards.
	Log io.Writer
	// Run executes one CLI invocation. Default: the real CLI. Tests replace it.
	Run func(args []string, stdout, stderr io.Writer) int
	// Once stops after the first pickup (tests; `listen --once`).
	Once bool
	// A sender may send Burst messages at once and then one more every Refill
	// (defaults 6 and 2 seconds), so a conversation never feels throttled and
	// only a flood is dropped. MaxPerDay caps replies per sender per UTC day
	// (default 500), which also ends two bots answering each other.
	Burst     int
	Refill    time.Duration
	MaxPerDay int
	// DemoURL is what the `demo` command links to; empty says "coming soon".
	DemoURL string
	// Metrics receives the bot's counters (see newMetrics); the caller serves
	// them. Nil records nothing.
	Metrics *appmetrics.Registry
	// Now is the clock (default time.Now).
	Now func() time.Time
}

const (
	docsURL  = "https://www.poweur.org/docs/"
	claimURL = "https://poweur.net/app/"
	// maxText bounds what is even looked at; replies never contain it.
	maxText = 500
)

// Serve listens for messages to the bot's ID and answers each, until the
// process is told to stop (the CLI's listen handles SIGINT and SIGTERM) or,
// with Once, after the first pickup.
func Serve(ctx context.Context, opt Options) error {
	if opt.Identity == "" {
		return fmt.Errorf("hello: an identity is required")
	}
	if err := idpkg.ValidateIdentityName(opt.Identity); err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	if opt.Run == nil {
		opt.Run = cli.Run
	}
	if opt.Log == nil {
		opt.Log = io.Discard
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Burst == 0 {
		opt.Burst = 6
	}
	if opt.Refill == 0 {
		opt.Refill = 2 * time.Second
	}
	if opt.MaxPerDay == 0 {
		opt.MaxPerDay = 500
	}
	b := &bot{opt: opt, limiter: newLimiter(opt.Burst, opt.Refill, opt.MaxPerDay), m: newMetrics(opt.Metrics)}

	args := []string{"listen", "--json", "--decrypt", "--use-identity", opt.Identity}
	if opt.Once {
		args = append(args, "--once")
	}
	pr, pw := io.Pipe()
	done := make(chan int, 1)
	go func() {
		code := opt.Run(args, pw, opt.Log)
		pw.Close()
		done <- code
	}()
	go func() {
		<-ctx.Done()
		pr.CloseWithError(ctx.Err())
	}()

	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 1<<20), 8<<20)
	for scanner.Scan() {
		b.pickup(scanner.Bytes())
	}
	if code := <-done; code != 0 {
		return fmt.Errorf("hello: listen exited %d", code)
	}
	return ctx.Err()
}

type bot struct {
	opt     Options
	limiter *limiter
	m       *metrics
}

// What happened to each message picked up. Bounded, so they can be metric
// labels; none carries a sender or any text.
const (
	resultReplied     = "replied"
	resultRateLimited = "rate_limited"
	resultReplyFailed = "reply_failed"
	resultUnreadable  = "unreadable" // not end-to-end encrypted, or the CLI could not open it
	resultIgnored     = "ignored"    // not chat text, from itself, or not from an identity
)

const (
	errReplyFailed = "reply_failed"
	errBadPickup   = "bad_pickup"
)

// metrics are the bot's counters. keyword is the only label that comes from
// what a sender wrote, and appmetrics folds anything outside keywords into
// "other", so the series are bounded whatever people type.
type metrics struct {
	messages *appmetrics.Counter
	replies  *appmetrics.Counter
	errors   *appmetrics.Counter
}

func newMetrics(r *appmetrics.Registry) *metrics {
	return &metrics{
		messages: r.Counter("messages_total", "Messages picked up from the inbox, by what the bot did with them.", "result",
			resultReplied, resultRateLimited, resultReplyFailed, resultUnreadable, resultIgnored),
		replies: r.Counter("replies_total", "Replies sent, by the keyword the message started with (other if it was none of ours).", "keyword",
			append(slices.Clone(keywords), appmetrics.Other)...),
		errors: r.Counter("errors_total", "Failures: a reply that could not be sent, or a pickup that could not be read.", "kind",
			errReplyFailed, errBadPickup),
	}
}

// inboxPickup is the wire shape `poweur listen --json --decrypt` prints for
// each pickup: the relay's inbox response plus, per message, the plaintext
// the CLI opened with the identity's key. The bot never sees that key.
type inboxPickup struct {
	Messages []struct {
		ID         string `json:"id"`
		Sender     string `json:"sender"`
		Timestamp  string `json:"timestamp"`
		Body       string `json:"body"`
		Decrypted  bool   `json:"decrypted"`
		Type       string `json:"type,omitempty"`
		Encryption *struct {
			Alg string `json:"alg"`
		} `json:"encryption,omitempty"`
	} `json:"messages"`
}

func (b *bot) pickup(line []byte) {
	var in inboxPickup
	if err := json.Unmarshal(line, &in); err != nil {
		b.m.errors.Inc(errBadPickup)
		return
	}
	for _, m := range in.Messages {
		if m.Type != "" && m.Type != idpkg.MsgTypeChatText {
			b.m.messages.Inc(resultIgnored)
			continue
		}
		sender := strings.ToLower(strings.TrimSpace(m.Sender))
		if sender == "" || sender == b.opt.Identity || idpkg.ValidateIdentityName(sender) != nil {
			b.m.messages.Inc(resultIgnored)
			continue
		}
		if !m.Decrypted || m.Encryption == nil || m.Encryption.Alg == "" {
			b.m.messages.Inc(resultUnreadable) // the relay carries these end-to-end encrypted; what the CLI could not open is not ours to answer
			continue
		}
		text := m.Body
		if len(text) > maxText {
			text = text[:maxText]
		}
		now := b.opt.Now()
		if !b.limiter.allow(sender, now) {
			b.m.messages.Inc(resultRateLimited)
			fmt.Fprintf(b.opt.Log, "hello: %s is over its limit, no reply\n", sender)
			continue
		}
		reply := Reply(Incoming{Sender: sender, Text: text, MessageID: m.ID, SentAt: m.Timestamp, Alg: m.Encryption.Alg}, now, b.opt.DemoURL)
		var stderr strings.Builder
		if code := b.opt.Run([]string{"send", sender, reply, "--use-identity", b.opt.Identity, "--request-on-reject"}, io.Discard, &stderr); code != 0 {
			b.m.messages.Inc(resultReplyFailed)
			b.m.errors.Inc(errReplyFailed)
			fmt.Fprintf(b.opt.Log, "hello: reply to %s failed (%d): %s\n", sender, code, strings.TrimSpace(stderr.String()))
			continue
		}
		b.m.messages.Inc(resultReplied)
		b.m.replies.Inc(keyword(text))
		fmt.Fprintf(b.opt.Log, "hello: replied to %s (%s)\n", sender, command(text))
	}
}

// Incoming is one message the bot is answering.
type Incoming struct {
	Sender    string // already validated as an identity
	Text      string
	MessageID string
	SentAt    string // RFC3339, as the sender stamped it
	Alg       string // the envelope's encryption algorithm
}

// keywords are the commands the bot understands. Anything else gets the
// greeting and is counted as "other".
var keywords = []string{"help", "ping", "whoami", "docs", "demo"}

// keyword is the command a message starts with, or "other".
func keyword(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return appmetrics.Other
	}
	word := strings.Trim(strings.ToLower(fields[0]), "/!.?,:;")
	if slices.Contains(keywords, word) {
		return word
	}
	return appmetrics.Other
}

// command is which reply a message gets: its keyword, or the greeting.
func command(text string) string {
	if k := keyword(text); k != appmetrics.Other {
		return k
	}
	return "hello"
}

// Reply is the bot's whole vocabulary. It only ever includes the sender's
// validated ID, message metadata the bot verified itself and fixed text; it
// never repeats what the sender wrote.
func Reply(in Incoming, now time.Time, demoURL string) string {
	switch command(in.Text) {
	case "help":
		return "I'm a demo bot. Write anything and I'll answer. Commands:\n" +
			"ping: how long your message took to reach me\n" +
			"whoami: what I can verify about you\n" +
			"docs: where to read more\n" +
			"demo: a live \"Sign in with Poweur\" demo"
	case "ping":
		return fmt.Sprintf("pong. Your message took %s to reach me.", latency(in.SentAt, now))
	case "whoami":
		return fmt.Sprintf("You are %s. Your message (%s) reached me encrypted with %s and signed by the key published at https://%s/.well-known/poweur/id.json, so I can tell it came from you.",
			in.Sender, shortID(in.MessageID), in.Alg, in.Sender)
	case "docs":
		return "Docs: " + docsURL + "\nKnow someone who'd like an ID? They can claim one at " + claimURL + " and message you."
	case "demo":
		if demoURL == "" {
			return "The sign-in demo is coming soon. Until then: " + docsURL
		}
		return "Try \"Sign in with Poweur\": " + demoURL
	}
	return fmt.Sprintf("Hi %s 👋 That message reached me end-to-end encrypted and signed by your key. Nobody in between could read it.\n"+
		"Try: help · ping · whoami · docs · demo\nTell a friend to claim their own ID at %s, then message them the same way.", in.Sender, claimURL)
}

func latency(sentAt string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, sentAt)
	if err != nil {
		return "a moment"
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return fmt.Sprintf("%d ms", d.Milliseconds())
	}
	return d.Round(100 * time.Millisecond).String()
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// limiter is a token bucket per sender (burst tokens, one more every refill)
// plus a cap on replies per UTC day.
type limiter struct {
	mu     sync.Mutex
	burst  int
	refill time.Duration
	max    int
	state  map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
	date   string
	day    int
}

func newLimiter(burst int, refill time.Duration, max int) *limiter {
	return &limiter{burst: burst, refill: refill, max: max, state: map[string]*bucket{}}
}

func (l *limiter) allow(sender string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.state[sender]
	if !ok {
		b = &bucket{tokens: float64(l.burst), last: now}
		l.state[sender] = b
	}
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens += float64(elapsed) / float64(l.refill)
		if b.tokens > float64(l.burst) {
			b.tokens = float64(l.burst)
		}
	}
	b.last = now
	if date := now.UTC().Format("2006-01-02"); b.date != date {
		b.date, b.day = date, 0
	}
	if b.tokens < 1 || b.day >= l.max {
		return false
	}
	b.tokens--
	b.day++
	// A sender that has gone quiet is forgotten, so this map cannot grow without bound.
	if len(l.state) > 10000 {
		for k, v := range l.state {
			if now.Sub(v.last) > 24*time.Hour {
				delete(l.state, k)
			}
		}
	}
	return true
}
