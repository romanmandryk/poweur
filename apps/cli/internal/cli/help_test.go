package cli

import (
	"bytes"
	"strings"
	"testing"
)

func runHelp(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestHelpTopLevelIsOneLinePerCommand(t *testing.T) {
	for _, args := range [][]string{{}, {"help"}, {"--help"}, {"-h"}} {
		code, out, _ := runHelp(args...)
		if code != 0 {
			t.Fatalf("%v exited %d", args, code)
		}
		if strings.Contains(out, "poweur identity create") {
			t.Fatalf("%v dumps subcommands:\n%s", args, out)
		}
		for _, c := range helpTree {
			if !strings.Contains(out, "  "+c.Name+" ") || !strings.Contains(out, c.Summary) {
				t.Fatalf("%v missing %q:\n%s", args, c.Name, out)
			}
		}
	}
}

func TestHelpGroupWithoutSubcommandListsThem(t *testing.T) {
	code, out, errOut := runHelp("identity")
	if code == 0 || out != "" {
		t.Fatalf("code=%d stdout=%q", code, out)
	}
	for _, want := range []string{"poweur identity: subcommand required", "create", "<name>", "[--json]", "add-encryption-key"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("missing %q:\n%s", want, errOut)
		}
	}
}

func TestHelpUnknownSubcommand(t *testing.T) {
	code, _, errOut := runHelp("key", "bogus")
	if code == 0 || !strings.Contains(errOut, `unknown subcommand "bogus"`) || !strings.Contains(errOut, "enroll") {
		t.Fatalf("code=%d\n%s", code, errOut)
	}
}

func TestHelpFlagsAreSuccess(t *testing.T) {
	cases := map[string][]string{
		"Subcommands:":              {"key", "--help"},
		"poweur key enroll <ident":  {"key", "enroll", "--help"},
		"Usage: poweur send <to>":   {"send", "-h"},
		"Usage: poweur devices <su": {"help", "devices"},
	}
	for want, args := range cases {
		code, out, errOut := runHelp(args...)
		if code != 0 || !strings.Contains(out, want) || errOut != "" {
			t.Fatalf("%v: code=%d want %q\n%s%s", args, code, want, out, errOut)
		}
	}
}

func TestHelpLeavesValidInvocationsAlone(t *testing.T) {
	for _, args := range [][]string{{"outbox"}, {"outbox", "list"}, {"key", "list"}, {"drive", "list"}} {
		if _, handled := routeHelp(args, &bytes.Buffer{}, &bytes.Buffer{}); handled {
			t.Fatalf("%v was intercepted", args)
		}
	}
}

// Every first-tier command in the dispatch switch must be documented, and
// every documented subcommand must be one the command really accepts.
func TestHelpTreeCoversDispatch(t *testing.T) {
	for _, name := range []string{"identity", "send", "inbox", "listen", "outbox", "drive", "sync", "devices",
		"history", "attach", "messages", "relay", "key", "group", "contacts", "requests", "analytics",
		"policy", "blocks", "report", "anon", "session", "auth", "version"} {
		if findHelp(name) == nil {
			t.Errorf("%s is dispatched but not in the help tree", name)
		}
	}
	for _, c := range helpTree {
		if c.Summary == "" {
			t.Errorf("%s has no summary", c.Name)
		}
		if len(c.Subs) == 0 && c.Name != "version" && c.Name != "help" && c.Usage == "" && c.Name != "" {
			t.Errorf("leaf %s has no usage", c.Name)
		}
	}
}
