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
	"strings"
	"sync"
	"time"

	"github.com/poweur/cli/internal/config"
	cryptoe2e "github.com/poweur/cli/internal/crypto"
	"github.com/poweur/cli/internal/identity"
	"github.com/poweur/cli/pkg/cli"
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
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	encPriv, err := identity.LoadEncryptionPrivateKey(identity.EncryptionKeyPath(cfg.KeysDir, opt.Identity))
	if err != nil {
		return fmt.Errorf("hello: no encryption key for %s in %s: %w", opt.Identity, cfg.KeysDir, err)
	}
	b := &bot{opt: opt, encPriv: encPriv, limiter: newLimiter(opt.Burst, opt.Refill, opt.MaxPerDay)}

	args := []string{"listen", "--json", "--use-identity", opt.Identity}
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
	encPriv []byte
	limiter *limiter
}

// inboxPickup is the wire shape `poweur listen --json` prints for each pickup.
type inboxPickup struct {
	Messages []struct {
		ID         string `json:"id"`
		Sender     string `json:"sender"`
		Timestamp  string `json:"timestamp"`
		Payload    string `json:"payload"`
		Type       string `json:"type,omitempty"`
		Encryption *struct {
			Alg                string `json:"alg"`
			EphemeralPublicKey string `json:"ephemeral_public_key"`
			Nonce              string `json:"nonce"`
		} `json:"encryption,omitempty"`
	} `json:"messages"`
}

func (b *bot) pickup(line []byte) {
	var in inboxPickup
	if err := json.Unmarshal(line, &in); err != nil {
		return
	}
	for _, m := range in.Messages {
		if m.Type != "" && m.Type != idpkg.MsgTypeChatText {
			continue
		}
		sender := strings.ToLower(strings.TrimSpace(m.Sender))
		if sender == "" || sender == b.opt.Identity || idpkg.ValidateIdentityName(sender) != nil {
			continue
		}
		if m.Encryption == nil || m.Encryption.Alg == "" {
			continue // the relay carries these end-to-end encrypted; plaintext is not ours to answer
		}
		plain, err := cryptoe2e.Decrypt(b.encPriv, cryptoe2e.EncryptedPayload{
			Ciphertext:         m.Payload,
			EphemeralPublicKey: m.Encryption.EphemeralPublicKey,
			Nonce:              m.Encryption.Nonce,
		})
		if err != nil {
			fmt.Fprintf(b.opt.Log, "hello: cannot open a message from %s: %v\n", sender, err)
			continue
		}
		text := string(plain)
		if len(text) > maxText {
			text = text[:maxText]
		}
		now := b.opt.Now()
		if !b.limiter.allow(sender, now) {
			fmt.Fprintf(b.opt.Log, "hello: %s is over its limit, no reply\n", sender)
			continue
		}
		reply := Reply(Incoming{Sender: sender, Text: text, MessageID: m.ID, SentAt: m.Timestamp, Alg: m.Encryption.Alg}, now, b.opt.DemoURL)
		var stderr strings.Builder
		if code := b.opt.Run([]string{"send", sender, reply, "--use-identity", b.opt.Identity, "--request-on-reject"}, io.Discard, &stderr); code != 0 {
			fmt.Fprintf(b.opt.Log, "hello: reply to %s failed (%d): %s\n", sender, code, strings.TrimSpace(stderr.String()))
			continue
		}
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

func command(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "hello"
	}
	word := strings.Trim(strings.ToLower(fields[0]), "/!.?,:;")
	switch word {
	case "help", "ping", "whoami", "docs", "demo":
		return word
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
		"Try: help · ping · whoami · docs\nTell a friend to claim their own ID at %s, then message them the same way.", in.Sender, claimURL)
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
