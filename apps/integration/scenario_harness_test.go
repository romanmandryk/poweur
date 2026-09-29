package integration_test

// E31-T1: the scenario harness for the headless reference apps.
//
// A scenario is written once against actors (an identity, its home relay, a
// client) and runs in every topology: all actors on one relay, and the host
// drive on relay A with members on B (and C). Providers: the filesystem
// always; S3 when POWEUR_TEST_S3_ENDPOINT points at a MinIO/S3 bucket.
//
// Actors act through the `poweur` CLI in-process — the same code a user runs,
// and the Go drive SDK underneath. The CLI reads HOME, so invocations are
// serialized; a long-running `drive watch` holds the lock only until its
// stream reports ready.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	relaypkg "github.com/poweur/api/pkg/relay"
	clipkg "github.com/poweur/cli/pkg/cli"
	"github.com/poweur/integration/fakedns"
)

// topology names where each actor slot is homed.
type topology struct {
	name  string
	slots map[string]string // slot ("A", "B", "C") → relay name
}

var scenarioTopologies = []topology{
	{name: "same-relay", slots: map[string]string{"A": "A", "B": "A", "C": "A"}},
	{name: "cross-relay", slots: map[string]string{"A": "A", "B": "B", "C": "C"}},
}

type storageKind struct {
	name string
	// configure fills the provider fields of a relay's config.
	configure func(t *testing.T, cfg *relaypkg.Config, relay string)
}

// scenarioProviders is the provider matrix: fs always, S3 when configured.
func scenarioProviders() []storageKind {
	kinds := []storageKind{{name: "fs", configure: func(t *testing.T, cfg *relaypkg.Config, relay string) {
		cfg.DataDir = t.TempDir()
	}}}
	endpoint := os.Getenv("POWEUR_TEST_S3_ENDPOINT")
	if endpoint == "" {
		return kinds
	}
	return append(kinds, storageKind{name: "s3", configure: func(t *testing.T, cfg *relaypkg.Config, relay string) {
		cfg.StorageProvider = "s3"
		cfg.S3Endpoint = endpoint
		cfg.S3Bucket = envOr("POWEUR_TEST_S3_BUCKET", "poweur-test")
		cfg.S3Region = envOr("POWEUR_TEST_S3_REGION", "us-east-1")
		cfg.S3AccessKey = envOr("POWEUR_TEST_S3_ACCESS_KEY", "minioadmin")
		cfg.S3SecretKey = envOr("POWEUR_TEST_S3_SECRET_KEY", "minioadmin")
		// One prefix per relay and run: scenarios never see each other.
		cfg.S3Prefix = fmt.Sprintf("scenario/%d/%s", time.Now().UnixNano(), relay)
		// The relay keeps a data dir for nothing durable, only local temp.
		cfg.DataDir = t.TempDir()
	}})
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// scenarioRelay is one relay process of a scenario; restart replaces it with
// a new process over the same store and address.
type scenarioRelay struct {
	name string
	cfg  relaypkg.Config
	ts   *httptest.Server
}

func (r *scenarioRelay) URL() string { return "http://" + r.cfg.RelayAddress }

type scenario struct {
	t       *testing.T
	zone    *fakedns.Zone
	topo    topology
	storage storageKind
	relays  map[string]*scenarioRelay
	mu      sync.Mutex // serializes CLI invocations (HOME is process-wide)
	// secrets is every plaintext the privacy scan must not find.
	secrets []string
}

// forEachScenario runs body once per topology × provider.
func forEachScenario(t *testing.T, body func(t *testing.T, s *scenario)) {
	for _, storage := range scenarioProviders() {
		for _, topo := range scenarioTopologies {
			t.Run(topo.name+"/"+storage.name, func(t *testing.T) {
				s := &scenario{t: t, zone: newZone(t), topo: topo, storage: storage, relays: map[string]*scenarioRelay{}}
				t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })
				body(t, s)
			})
		}
	}
}

// relay returns the relay a slot is homed on in this topology, starting it.
func (s *scenario) relay(slot string) *scenarioRelay {
	name := s.topo.slots[slot]
	if name == "" {
		s.t.Fatalf("unknown slot %q", slot)
	}
	if r := s.relays[name]; r != nil {
		return r
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		s.t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	cfg := relaypkg.Config{
		ListenAddr: addr, RelayAddress: addr, RelayScheme: "http",
		DNSTTL: time.Minute, ChallengeTTL: time.Minute, Version: "integration-test",
		HostedDomains: []string{"poweur.net"}, ResolverAllowPrivate: true,
		RateLimits: relaypkg.RateLimits{PerMinute: 1000, PerHour: 10000, PerDay: 100000},
	}
	s.storage.configure(s.t, &cfg, name)
	r := &scenarioRelay{name: name, cfg: cfg}
	r.ts = s.startRelay(cfg)
	s.relays[name] = r
	return r
}

func (s *scenario) startRelay(cfg relaypkg.Config) *httptest.Server {
	var listener net.Listener
	var err error
	for i := 0; i < 50; i++ {
		if listener, err = net.Listen("tcp", cfg.ListenAddr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		s.t.Fatalf("listen %s: %v", cfg.ListenAddr, err)
	}
	providers := relaypkg.NewProviderFactory(cfg)
	relaypkg.RegisterProvider(providers, "mock", s.zone.Provider())
	ts := httptest.NewUnstartedServer(relaypkg.Router(relaypkg.NewServer(cfg, s.zone, providers)))
	ts.Listener.Close()
	ts.Listener = listener
	ts.Start()
	s.t.Cleanup(ts.Close)
	return ts
}

// restart stops a relay and starts a new process over the same store: every
// in-memory cache is gone (E31 statelessness assertion).
func (s *scenario) restart(name string) {
	s.t.Helper()
	r := s.relays[name]
	if r == nil {
		s.t.Fatalf("relay %s not started", name)
	}
	r.ts.CloseClientConnections()
	r.ts.Close()
	r.ts = s.startRelay(r.cfg)
}

// actor is an identity homed on the relay of a slot, acting through the CLI.
type actor struct {
	s        *scenario
	Name     string
	Identity string
	Home     string
	Relay    *scenarioRelay
}

// newActor creates a hosted identity <handle>.poweur.net on the slot's relay.
// Handles get a per-run suffix so parallel topologies never collide.
func (s *scenario) newActor(name, slot string) *actor {
	s.t.Helper()
	r := s.relay(slot)
	identity := fmt.Sprintf("%s%d.poweur.net", strings.ToLower(name), time.Now().UnixNano()%1_000_000)
	s.zone.SetHost(identity, r.cfg.RelayAddress)
	// The resolver dials each identity's own relay through the fake zone.
	zone := s.zone
	clipkg.ConfigureIdentityResolverHosts("http", true, func(host string) string {
		if hosts, err := zone.LookupHost(context.Background(), host); err == nil && len(hosts) > 0 {
			return hosts[0]
		}
		return ""
	})
	a := &actor{s: s, Name: name, Identity: identity, Home: s.t.TempDir(), Relay: r}
	a.Run("identity", "create", identity, "--hosted", "--relay", r.URL(), "--json")
	return a
}

// anonymous is someone without a Poweur ID: an empty HOME.
func (s *scenario) anonymous(name string) *actor {
	return &actor{s: s, Name: name, Home: s.t.TempDir()}
}

// Try runs one CLI command as this actor and returns its exit code and output.
func (a *actor) Try(args ...string) (int, string, string) {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	a.s.t.Setenv("HOME", a.Home)
	var stdout, stderr bytes.Buffer
	code := clipkg.Run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// Run runs a command that must succeed.
func (a *actor) Run(args ...string) string {
	a.s.t.Helper()
	code, stdout, stderr := a.Try(args...)
	if code != 0 {
		a.s.t.Fatalf("%s: poweur %s exited %d\nstdout: %s\nstderr: %s", a.Name, strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout
}

// JSON runs a command with --json and decodes its output into out.
func (a *actor) JSON(out any, args ...string) {
	a.s.t.Helper()
	raw := a.Run(append(args, "--json")...)
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		a.s.t.Fatalf("%s: poweur %s: %v\n%s", a.Name, strings.Join(args, " "), err, raw)
	}
}

// File writes content to a new local file in the actor's workspace.
func (a *actor) File(name, content string) string {
	a.s.t.Helper()
	path := filepath.Join(a.Home, "work", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		a.s.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		a.s.t.Fatal(err)
	}
	return path
}

// Read returns a local file's content.
func (a *actor) Read(path string) string {
	a.s.t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		a.s.t.Fatal(err)
	}
	return string(raw)
}

// Out is a fresh local path for a download.
func (a *actor) Out(name string) string {
	dir := filepath.Join(a.Home, "downloads")
	_ = os.MkdirAll(dir, 0o700)
	return filepath.Join(dir, fmt.Sprintf("%d-%s", time.Now().UnixNano(), name))
}

// watch starts `drive watch` on drive and returns a channel that yields the
// change events (the ready event is waited for, not delivered). The stream
// ends after count events or the timeout; done reports the exit code.
func (a *actor) watch(drive string, count int, timeout time.Duration) (<-chan map[string]any, <-chan int) {
	a.s.t.Helper()
	a.s.mu.Lock()
	a.s.t.Setenv("HOME", a.Home)
	reader, writer := io.Pipe()
	events, done := make(chan map[string]any, 16), make(chan int, 1)
	ready := make(chan struct{})
	args := []string{"drive", "watch", "--json", "--count", fmt.Sprint(count), "--timeout", timeout.String()}
	if drive != "" {
		args = append(args, "--drive", drive)
	}
	var stderr bytes.Buffer
	go func() {
		code := clipkg.Run(args, writer, &stderr)
		writer.Close()
		done <- code
	}()
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(reader)
		signalled := false
		for scanner.Scan() {
			var event map[string]any
			if json.Unmarshal(scanner.Bytes(), &event) != nil {
				continue
			}
			if event["type"] == "ready" {
				if !signalled {
					signalled = true
					close(ready)
				}
				continue
			}
			events <- event
		}
		if !signalled {
			close(ready)
		}
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		a.s.mu.Unlock()
		a.s.t.Fatalf("%s: watch never became ready: %s", a.Name, stderr.String())
	}
	a.s.mu.Unlock()
	return events, done
}

// secret records plaintext the privacy scan must never find in any store.
func (s *scenario) secret(values ...string) {
	s.secrets = append(s.secrets, values...)
}

// scanFindings lists "<relay>:<key> contains <secret>" for every stored
// object holding a recorded secret.
func (s *scenario) scanFindings() []string {
	s.t.Helper()
	var findings []string
	for _, r := range s.relays {
		objects := 0
		err := relaypkg.WalkStore(context.Background(), r.cfg, func(key string, data []byte) error {
			objects++
			for _, secret := range s.secrets {
				if bytes.Contains(data, []byte(secret)) {
					findings = append(findings, fmt.Sprintf("%s:%s contains %q", r.name, key, secret))
				}
			}
			return nil
		})
		if err != nil {
			s.t.Fatalf("scan relay %s: %v", r.name, err)
		}
		// A scan that saw nothing proves nothing (a wrong prefix or bucket).
		if objects == 0 {
			s.t.Fatalf("scan relay %s (%s): the store is empty", r.name, s.storage.name)
		}
	}
	return findings
}

// privacyScan fails the test if any store holds a recorded secret.
func (s *scenario) privacyScan() {
	s.t.Helper()
	if findings := s.scanFindings(); len(findings) > 0 {
		s.t.Fatalf("plaintext in the store:\n%s", strings.Join(findings, "\n"))
	}
}

// acceptOffers picks up a's inbox and accepts every share offer from peer,
// as `poweur drive accept` does for a user, and returns how many.
func (a *actor) acceptOffers(peer string) int {
	a.s.t.Helper()
	a.Run("inbox")
	raw := a.Run("history", peer, "--json")
	var out struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		a.s.t.Fatalf("history: %v\n%s", err, raw)
	}
	accepted := 0
	for _, m := range out.Messages {
		if body, ok := m["body"].(string); ok && m["type"] == "sys.share.offer" {
			a.Run("drive", "accept", a.File(fmt.Sprintf("offer-%d.json", time.Now().UnixNano()), body), "--json")
			accepted++
		}
	}
	if accepted == 0 {
		a.s.t.Fatalf("%s: no share offer from %s:\n%s", a.Name, peer, raw)
	}
	return accepted
}
