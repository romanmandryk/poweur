package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	idpkg "github.com/poweur/identity"
)

// `poweur email filters`: who may email this identity. With no flags it shows
// the current filters; any flag changes them and sends the result to the
// bridge as `email.settings`. The archived copy of that message is the
// CLI's (and the web app's) record of the settings — the bridge keeps only
// hashes.
//
//	poweur email filters [--receive=everyone|contacts] [--block=a@x,@spam.example]
//	                     [--unblock=…] [--allow=…] [--disallow=…]
func runEmailFilters(args []string, stdout, stderr io.Writer) int {
	fs, useIdentity, jsonOut := emailFlags("filters", stderr)
	receive := fs.String("receive", "", "everyone (default) or contacts: only allowed senders, people you emailed and replies to your mail")
	var block, unblock, allow, disallow stringList
	fs.Var(&block, "block", "address or @domain to block (repeatable, comma-separated)")
	fs.Var(&unblock, "unblock", "remove from the block list")
	fs.Var(&allow, "allow", "address or @domain always let through in contacts mode")
	fs.Var(&disallow, "disallow", "remove from the allow list")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	mail, _, _, bridge, err := loadMail(*useIdentity, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	current := latestEmailSettings(mail)
	changed := *receive != "" || len(block)+len(unblock)+len(allow)+len(disallow) > 0
	if !changed {
		return writeOutput(stdout, *jsonOut, current, describeEmailSettings(current))
	}
	next := current
	next.Version = idpkg.EmailPayloadVersion
	if *receive != "" {
		next.Receive = *receive
	}
	next.Block = editFilterList(current.Block, block, unblock)
	next.Allow = editFilterList(current.Allow, allow, disallow)
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := next.Validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	payload, _ := json.Marshal(next)
	var out bytes.Buffer
	_, identityValue, _, ok := loadIdentityKey(*useIdentity, stderr)
	if !ok {
		return 1
	}
	if code := runSend([]string{bridge, string(payload), "--type=" + idpkg.MsgTypeEmailSettings, "--use-identity=" + identityValue, "--json"}, &out, stderr); code != 0 {
		return code
	}
	return writeOutput(stdout, *jsonOut, next, describeEmailSettings(next))
}

// latestEmailSettings is the newest email.settings this identity sent; none:
// the permissive defaults.
func latestEmailSettings(mail []mailEntry) idpkg.EmailSettings {
	best := idpkg.EmailSettings{Version: idpkg.EmailPayloadVersion, Receive: idpkg.EmailReceiveEveryone}
	for _, e := range mail {
		if e.Settings != nil && e.Settings.UpdatedAt > best.UpdatedAt {
			best = *e.Settings
		}
	}
	if best.Receive == "" {
		best.Receive = idpkg.EmailReceiveEveryone
	}
	return best
}

func editFilterList(list []string, add, remove []string) []string {
	split := func(values []string) []string {
		var out []string
		for _, v := range values {
			for _, p := range strings.Split(v, ",") {
				if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
					out = append(out, p)
				}
			}
		}
		return out
	}
	drop := map[string]bool{}
	for _, r := range split(remove) {
		drop[r] = true
	}
	var out []string
	for _, e := range append(append([]string{}, list...), split(add)...) {
		if !drop[e] {
			drop[e] = true // once
			out = append(out, e)
		}
	}
	return out
}

func describeEmailSettings(s idpkg.EmailSettings) string {
	var b strings.Builder
	if s.ContactsOnly() {
		b.WriteString("who can email you: contacts only (allowed senders, people you emailed, replies to your mail)\n")
	} else {
		b.WriteString("who can email you: everyone\n")
	}
	if len(s.Block) > 0 {
		fmt.Fprintf(&b, "blocked: %s\n", strings.Join(s.Block, ", "))
	}
	if len(s.Allow) > 0 {
		fmt.Fprintf(&b, "allowed: %s\n", strings.Join(s.Allow, ", "))
	}
	return b.String()
}
