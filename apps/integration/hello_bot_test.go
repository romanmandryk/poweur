package integration_test

import (
	"context"
	"strings"
	"testing"

	"github.com/poweur/demoapps/appmetrics"
	"github.com/poweur/demoapps/hello"
)

// The hello.poweur.net demo bot against a real relay: a new ID writes to it,
// the bot answers, and the answer reaches the sender: in the inbox when the
// sender accepts anyone, and as a contact request (carrying the reply) when
// the sender's policy, the app's recommended one, asks strangers to
// introduce themselves.
func TestHelloBotAnswersANewID(t *testing.T) {
	for _, tc := range []struct {
		name, policy string
		inInbox      bool
	}{
		{"default open inbox", "", true},
		{"contacts and requests", "contacts_and_requests", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			relay := newDrillRelay(t, "hello.poweur.net", "alice.poweur.net")
			botHome, aliceHome := t.TempDir(), t.TempDir()
			createSeedIdentity(t, botHome, "hello.poweur.net", relay.url)
			createSeedIdentity(t, aliceHome, "alice.poweur.net", relay.url)
			runCLI(t, botHome, "policy", "set", "open", "--use-identity", "hello.poweur.net")
			if tc.policy != "" {
				runCLI(t, aliceHome, "policy", "set", tc.policy, "--use-identity", "alice.poweur.net")
			}

			runCLI(t, aliceHome, "send", "hello.poweur.net", "ping", "--use-identity", "alice.poweur.net", "--request-on-reject")

			// Sequential on purpose: the CLI reads HOME on every call.
			t.Setenv("HOME", botHome)
			var logs strings.Builder
			metrics := appmetrics.New("poweur_hello")
			if err := hello.Serve(context.Background(), hello.Options{Identity: "hello.poweur.net", Once: true, Log: &logs, Metrics: metrics}); err != nil {
				t.Fatalf("serve: %v\n%s", err, logs.String())
			}
			if !strings.Contains(logs.String(), "replied to alice.poweur.net (ping)") {
				t.Fatalf("bot log:\n%s", logs.String())
			}
			var scraped strings.Builder
			metrics.Write(&scraped)
			for _, want := range []string{
				`poweur_hello_messages_total{result="replied"} 1`,
				`poweur_hello_replies_total{keyword="ping"} 1`,
				`poweur_hello_replies_total{keyword="other"} 0`,
				`poweur_hello_errors_total{kind="reply_failed"} 0`,
			} {
				if !strings.Contains(scraped.String(), want+"\n") {
					t.Errorf("metrics lack %q:\n%s", want, scraped.String())
				}
			}
			if strings.Contains(scraped.String(), "alice") {
				t.Errorf("metrics name the sender:\n%s", scraped.String())
			}

			inbox, _ := runCLI(t, aliceHome, "inbox", "--use-identity", "alice.poweur.net")
			requests, _ := runCLI(t, aliceHome, "requests", "--use-identity", "alice.poweur.net")
			got := strings.Contains(inbox, "pong. Your message took")
			inRequests := strings.Contains(requests, "pong. Your message took")
			if tc.inInbox && !got || !tc.inInbox && !inRequests {
				t.Fatalf("answer not where expected (inbox=%v requests=%v)\ninbox: %s\nrequests: %s", got, inRequests, inbox, requests)
			}
		})
	}
}
