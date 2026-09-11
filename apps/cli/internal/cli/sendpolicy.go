package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Recovering from a policy rejection at send time (EPIC-007 E07-T3).
//
// Under `contacts_and_requests` the relay refuses a stranger's message with
// `policy_rejected` and a hint that a contact request would be accepted.
// Until now the hint was all `poweur send` did: the user read a sentence
// telling them to run a *different* command, retyped their message as the
// intro, and hoped they got the flag order right. The relay had already told
// us exactly what to do; not doing it was the CLI being lazy on the user's
// behalf.
//
// So a rejected send offers to convert itself into a contact request, and
// carries the message across as the intro — the intro is an ordinary short
// E2E-encrypted message, which is exactly what they typed.
//
// Two rules keep the offer honest:
//
//   - It never fires for `sys.*` envelopes. A rejected contact request must
//     not answer itself with another contact request, and `contacts_only`
//     rejects requests by design.
//   - The exit code still tells the truth. Converting the whole message into
//     the intro means what the user wrote went out, so the command succeeds;
//     a message too long to be an intro did *not* go out, so it fails even
//     though the request was sent.

// maxIntroPlaintext bounds the plaintext that may be reused as a contact
// request intro. The relay caps the *encrypted* intro at 4 KB
// (maxContactRequestPayload); base64 of a sealed box runs ~4/3 of the
// plaintext plus envelope overhead, so this leaves generous headroom and
// keeps "your message became the intro" a promise rather than a gamble.
const maxIntroPlaintext = 2048

// defaultIntro is what a request carries when the message is too long to be
// one.
const defaultIntro = "contact request"

// promptInput is the CLI's answer channel. The CLI is otherwise entirely
// non-interactive (passphrases come from env or flags), so this is the one
// place stdin is read — and it is a var so tests can answer without a TTY.
var promptInput io.Reader = os.Stdin

// contactRequestOffer is everything the offer needs from the send that was
// just refused.
type contactRequestOffer struct {
	recipient   string
	plaintext   string
	msgType     string
	useIdentity string
	status      int
	body        []byte
	// auto skips the question (--request-on-reject), for scripts and for
	// anyone who already knows what they want.
	auto bool
	// jsonOut suppresses the offer: a machine reading our stdout did not
	// ask a question and cannot answer one.
	jsonOut bool
}

// isPolicyRejection reports whether a relay response is the inbox policy's
// uniform refusal. The relay answers 403 with `{"error":"policy_rejected"}`;
// the body is matched on the error code rather than the prose so a reworded
// hint does not silently disable this path.
func isPolicyRejection(status int, body []byte) bool {
	if status != 403 {
		return false
	}
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	return payload.Error == "policy_rejected"
}

// contactRequestIntro decides what the request says. Returns the intro and
// whether it carries the user's whole message.
func contactRequestIntro(plaintext string) (intro string, carriesMessage bool) {
	trimmed := strings.TrimSpace(plaintext)
	if trimmed == "" || len(trimmed) > maxIntroPlaintext {
		return defaultIntro, false
	}
	return trimmed, true
}

// offersRequest reports whether a refusal should turn into an offer at all.
// System envelopes are excluded: they are the machinery of the contact
// handshake, and a request that answers a rejected request is a loop.
func offersRequest(o contactRequestOffer) bool {
	if o.jsonOut {
		return false
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(o.msgType)), "sys.") {
		return false
	}
	return isPolicyRejection(o.status, o.body)
}

// stdinIsInteractive reports whether there is a human to ask. A pipe, a
// closed stdin or a CI runner all answer "no", so the offer degrades to the
// printed hint instead of hanging a script forever.
func stdinIsInteractive() bool {
	f, ok := promptInput.(*os.File)
	if !ok {
		return true // a test (or an embedder) injected an answer
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// confirm asks a yes/no question on stderr (stdout stays the command's
// output) and reads one line. Anything but y/yes is no.
func confirm(stderr io.Writer, question string) bool {
	fmt.Fprintf(stderr, "%s [y/N]: ", question)
	line, err := bufio.NewReader(promptInput).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		fmt.Fprintln(stderr)
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// offerContactRequest turns a policy rejection into a contact request when
// the user (or --request-on-reject) says so. Returns true only when the
// request was sent AND it carried the whole message the user typed — the
// caller uses that to decide the exit code.
func offerContactRequest(o contactRequestOffer, stdout, stderr io.Writer) bool {
	if !offersRequest(o) {
		return false
	}
	intro, carriesMessage := contactRequestIntro(o.plaintext)
	if !o.auto {
		if !stdinIsInteractive() {
			fmt.Fprintf(stderr,
				"%s does not accept messages from you yet. Re-run with --request-on-reject "+
					"to send a contact request instead (or `poweur contacts request %s \"...\"`).\n",
				o.recipient, o.recipient)
			return false
		}
		question := fmt.Sprintf("Send %s a contact request instead?", o.recipient)
		if carriesMessage {
			question = fmt.Sprintf("Send %s a contact request with this message as the intro?", o.recipient)
		}
		if !confirm(stderr, question) {
			return false
		}
	}
	// `contacts request` is the whole handshake, not just the send: it pins
	// the key and records the target as `requested` before the
	// sys.contact.request envelope goes out. Re-implementing the send here
	// would skip both.
	requestArgs := []string{o.recipient, intro}
	if o.useIdentity != "" {
		requestArgs = append(requestArgs, "--use-identity", o.useIdentity)
	}
	if code := runContactsRequest(requestArgs, stdout, stderr); code != 0 {
		return false
	}
	if !carriesMessage {
		fmt.Fprintf(stderr,
			"note: your message was too long to travel as the request intro, so it was NOT sent — "+
				"send it again once %s accepts.\n", o.recipient)
		return false
	}
	return true
}
