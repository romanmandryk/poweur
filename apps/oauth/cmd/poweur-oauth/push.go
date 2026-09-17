package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/poweur/identity"
)

// cliPusher sends sign-in prompts with the `poweur` CLI, as the bridge's own
// identity, from a home directory that holds nothing but that identity. The
// CLI owns message encryption and signing, so the bridge carries no second
// implementation of either.
type cliPusher struct {
	bin      string
	home     string
	identity string
	run      func(ctx context.Context, name string, args []string, env []string) ([]byte, error)
}

func newCLIPusher(bin, home, id string) (*cliPusher, error) {
	if home == "" || id == "" {
		return nil, errors.New("OAUTH_PUSH_CLI needs OAUTH_PUSH_HOME and OAUTH_PUSH_IDENTITY (the bridge's own Poweur ID, created with `poweur identity create` under that HOME)")
	}
	id = strings.ToLower(strings.TrimSpace(id))
	if err := identity.ValidateIdentityName(id); err != nil {
		return nil, fmt.Errorf("OAUTH_PUSH_IDENTITY: %w", err)
	}
	if st, err := os.Stat(home); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("OAUTH_PUSH_HOME %s is not a directory", home)
	}
	return &cliPusher{bin: bin, home: home, identity: id, run: runCommand}, nil
}

func runCommand(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(errOut.String()))
	}
	return out.Bytes(), nil
}

func (p *cliPusher) Push(ctx context.Context, to string, body []byte, expiresAt time.Time) error {
	args := []string{
		"send", to, string(body),
		"--type", identity.MsgTypeAuthRequest,
		"--expires", expiresAt.UTC().Format(time.RFC3339),
		"--use-identity", p.identity,
		// Through the bridge's own relay, which delivers or forwards it: the
		// bridge host never needs to reach — or resolve — users' relays.
		"--via-home-relay",
		"--json",
	}
	env := []string{"HOME=" + p.home, "PATH=" + os.Getenv("PATH")}
	for _, k := range []string{"POWEUR_RESOLVER_SCHEME", "RESOLVER_ALLOW_PRIVATE", "POWEUR_RESOLVER_DIAL", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	out, err := p.run(ctx, p.bin, args, env)
	if err != nil {
		return err
	}
	// The CLI queues an undeliverable message for retry and still exits 0. A
	// sign-in prompt is only worth anything now, so anything but an accepted
	// send is a failure.
	var result struct {
		Status json.RawMessage `json:"status"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &result); err != nil {
		return fmt.Errorf("unexpected poweur send output: %.200s", out)
	}
	var code int
	if err := json.Unmarshal(result.Status, &code); err != nil || code < 200 || code >= 300 {
		return fmt.Errorf("prompt not delivered (status %s)", strings.TrimSpace(string(result.Status)))
	}
	return nil
}
