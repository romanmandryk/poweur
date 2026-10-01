package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// Help is layered, like git's: `poweur` lists the first-tier commands with a
// line each; `poweur <command>` lists that command's subcommands with their
// required <args> and [options]; `poweur <command> <sub> --help` shows one.
//
// The tree below is the only place command documentation lives. The
// dispatch switch in Run stays the source of truth for behaviour, and
// TestHelpTreeMatchesDispatch keeps the two from drifting.

// subHelp documents one subcommand. Usage is everything after the name:
// <required> arguments first, then [optional] flags.
type subHelp struct {
	Name    string
	Aliases []string
	Usage   string
	Summary string
}

// cmdHelp documents one first-tier command. A command with Subs is a group:
// running it with no subcommand is an error. Optional groups (outbox) have a
// default action, so the bare command is valid. A command without Subs is a
// leaf and documents itself through Usage.
type cmdHelp struct {
	Name     string
	Summary  string
	Usage    string
	Subs     []subHelp
	Optional bool
}

const (
	idFlags  = "[--use-identity=<id>] [--json]"
	relayFl  = "[--relay=<url>]"
	driveFl  = "[--drive=<identity>] [--json]"
	syncArgs = "<local-dir> [--drive <owner> --folder /<node>] [--path <p>]"
)

var helpTree = []cmdHelp{
	{Name: "identity", Summary: "Create, inspect and switch between your identities", Subs: []subHelp{
		{Name: "create", Usage: "<name> [--dns-provider=cloudflare|hetzner] [--dns-token=<t>] [--parent-domain=<d>] " + relayFl + " [--seed=<b64url>] [--operator-token=<t>] [--json]", Summary: "Create an identity and register it with a relay"},
		{Name: "show", Usage: idFlags, Summary: "Show the active identity"},
		{Name: "list", Usage: "[--json]", Summary: "List identities on this machine"},
		{Name: "use", Usage: "<identity> [--json]", Summary: "Make an identity the active one"},
		{Name: "dns", Usage: "<identity> " + idFlags, Summary: "Print the DNS records an identity needs"},
		{Name: "lookup", Usage: "<identity> [--json]", Summary: "Resolve someone else's identity"},
		{Name: "export", Usage: idFlags, Summary: "Export the identity document"},
	}},
	{Name: "key", Summary: "Recover, derive, back up and pair keys and devices", Subs: []subHelp{
		{Name: "enroll", Usage: "<identity> " + relayFl + " [--label=<text>] [--wait]", Summary: "Pair THIS machine with an existing identity (shows a QR and a code)"},
		{Name: "approve", Usage: "<pairing-link|code> [--use-identity=<id>] [--seed=<b64url|mnemonic>] [--sas=<digits>] [--no-wait] [--json]", Summary: "Approve another device that is enrolling"},
		{Name: "claim", Usage: "<identity> <code> [--json]", Summary: "Finish a pairing started without --wait"},
		{Name: "ls", Aliases: []string{"list"}, Usage: "[--use-identity=<id>] " + relayFl + " [--json]", Summary: "List the keys and enrollments the relay holds"},
		{Name: "recover", Usage: "<identity> --seed <b64url|mnemonic> " + relayFl + " [--parent-domain=<d>] [--json]", Summary: "Restore an identity on this machine from its seed"},
		{Name: "derive", Usage: "--seed <b64url|mnemonic> [--json]", Summary: "Show the public keys a seed produces"},
		{Name: "kit", Usage: "--seed <b64url|mnemonic> [--use-identity=<id>] [--json]", Summary: "Print a recovery kit"},
		{Name: "rotate", Usage: "[--use-identity=<id>] [--grace=<duration>] [--json]", Summary: "Move the identity onto a new seed (new signing and encryption keys)"},
		{Name: "protect", Usage: "[--use-identity=<id>] [--passphrase=<p>] [--json]", Summary: "Encrypt the private keys on disk with a passphrase"},
		{Name: "unprotect", Usage: "[--use-identity=<id>] [--passphrase=<p>] [--json]", Summary: "Remove the passphrase from the keys on disk"},
	}},
	{Name: "devices", Summary: "See and control the machines using your identity", Subs: []subHelp{
		{Name: "list", Usage: idFlags + " " + relayFl, Summary: "List every device the relay has seen"},
		{Name: "show", Usage: "[--json]", Summary: "Show what this machine calls itself"},
		{Name: "name", Usage: "<name>", Summary: "Rename this machine"},
		{Name: "revoke", Usage: "<dev_…> " + idFlags + " " + relayFl, Summary: "Cut a device off: ends its sessions, tokens and app passwords"},
	}},
	{Name: "session", Summary: "Manage this machine's relay session", Subs: []subHelp{
		{Name: "status", Usage: idFlags, Summary: "Show the current session"},
		{Name: "refresh", Usage: idFlags, Summary: "Get a fresh session"},
		{Name: "revoke", Usage: idFlags, Summary: "End the current session"},
	}},
	{Name: "relay", Summary: "Show or change the relay this CLI talks to", Subs: []subHelp{
		{Name: "status", Usage: "[--json]", Summary: "Show the relay and whether it is reachable"},
		{Name: "set", Usage: "<url>", Summary: "Use a different relay"},
	}},
	{Name: "send", Usage: "<to> <message> [--attach=<file>] [--sign-with=session|identity] [--via-home-relay] [--request-on-reject] [--anon] " + "[--use-identity=<id>] [--json]",
		Summary: "Send an encrypted message (or file) to an identity"},
	{Name: "inbox", Usage: "[--decrypt] " + idFlags, Summary: "Read new messages (--json --decrypt: JSON with each message's plaintext as body)"},
	{Name: "listen", Usage: "[--once] [--decrypt] " + idFlags, Summary: "Stream incoming messages as they arrive"},
	{Name: "history", Usage: "[<peer>] [--limit=N] [--before=N] [--thread=<name>] [--keep-unread] " + idFlags, Summary: "Show past conversations"},
	{Name: "outbox", Optional: true, Summary: "Messages waiting to be delivered", Subs: []subHelp{
		{Name: "list", Usage: "[--json]", Summary: "Show queued messages (default)"},
		{Name: "retry", Usage: "[--json]", Summary: "Try to deliver the queue again"},
	}},
	{Name: "attach", Summary: "Work with message attachments", Subs: []subHelp{
		{Name: "save", Usage: "<peer> --id=<message-id> --out=<file> [--json]", Summary: "Save an attachment to a file"},
	}},
	{Name: "messages", Summary: "Message counters and state", Subs: []subHelp{
		{Name: "status", Usage: idFlags, Summary: "Show unread and queued counts"},
	}},
	{Name: "anon", Usage: idFlags, Summary: "Read your anonymous (unsigned) message queue"},
	{Name: "requests", Usage: idFlags, Summary: "List pending contact requests"},
	{Name: "contacts", Summary: "Your address book and who may message you", Subs: []subHelp{
		{Name: "ls", Usage: idFlags, Summary: "List contacts"},
		{Name: "add", Usage: "<identity> [--petname=<name>] " + idFlags, Summary: "Add a contact"},
		{Name: "request", Usage: "<identity> [<intro message>] " + idFlags, Summary: "Ask someone to accept you"},
		{Name: "accept", Usage: "<identity> [--petname=<name>] " + idFlags, Summary: "Accept a contact request"},
		{Name: "block", Usage: "<identity> " + idFlags, Summary: "Block an identity"},
		{Name: "rm", Usage: "<identity> " + idFlags, Summary: "Remove a contact"},
	}},
	{Name: "policy", Summary: "Who is allowed to send to you", Subs: []subHelp{
		{Name: "show", Usage: idFlags, Summary: "Show the current policy"},
		{Name: "set", Usage: "<open|contacts_only|contacts_and_requests> [--anon-allow=true|false] [--anon-challenge=none|pow] [--anon-bits=N] [--trusted-auth=<bridge id>|none] " + idFlags, Summary: "Change the policy"},
	}},
	{Name: "blocks", Summary: "Share and import block lists", Subs: []subHelp{
		{Name: "export", Usage: "[--name=<n>] [--out=<file>] [--no-publish] " + idFlags, Summary: "Export (and publish) your block list"},
		{Name: "import", Usage: "<publisher>|--file=<path> [--path=<p>] [--force] [--dry-run] " + idFlags, Summary: "Import someone's block list"},
	}},
	{Name: "report", Usage: "<identity> [--reason=spam|harassment|phishing|malware|impersonation|other] [--note=<text>] [--message-ids=id,id]", Summary: "Report abuse from an identity"},
	{Name: "group", Summary: "Group chats", Subs: []subHelp{
		{Name: "create", Usage: "<group-id> [--admin=<id> ...] [--member=<id> ...] [--json]", Summary: "Create a group"},
		{Name: "show", Usage: "<group-id> [--json]", Summary: "Show a group and its members"},
		{Name: "add", Usage: "<group-id> [--member=<id> ...] [--admin=<id> ...] [--json]", Summary: "Add members or admins"},
		{Name: "remove", Usage: "<group-id> [--member=<id> ...] [--admin=<id> ...] [--json]", Summary: "Remove members or admins"},
		{Name: "send", Usage: "<group-id> <message> [--thread=<name>]", Summary: "Send to a group"},
		{Name: "inbox", Usage: "<group-id> [--thread=<name>]", Summary: "Read a group's messages"},
	}},
	{Name: "drive", Summary: "The encrypted drive: files, logs, sharing and links", Subs: []subHelp{
		{Name: "info", Usage: driveFl, Summary: "Show drive info"},
		{Name: "ls", Aliases: []string{"list"}, Usage: "[<remote-path>] " + driveFl, Summary: "List a folder"},
		{Name: "put", Usage: "<local-file> <remote-path> [--base=<version>] " + driveFl, Summary: "Upload a file"},
		{Name: "get", Usage: "<remote-path> <local-file> [--version=<version>] " + driveFl, Summary: "Download a file"},
		{Name: "mkdir", Usage: "<remote-path> " + driveFl, Summary: "Create a folder"},
		{Name: "mv", Usage: "<from> <to> " + driveFl, Summary: "Move or rename"},
		{Name: "rm", Usage: "<remote-path> " + driveFl, Summary: "Delete a file or folder"},
		{Name: "node", Usage: "<path|id> " + driveFl, Summary: "Show one node"},
		{Name: "changes", Usage: "[--from=<cursor>] " + driveFl, Summary: "Show the changes feed"},
		{Name: "records", Usage: "<path> " + driveFl, Summary: "Show a log's records"},
		{Name: "history", Usage: "<path> [--from=1] " + driveFl, Summary: "Show a file or log's history"},
		{Name: "append", Usage: "<path> <file> " + driveFl, Summary: "Append a record to a log"},
		{Name: "tail", Usage: "<path> [--from=1] " + driveFl, Summary: "Read a log from a position"},
		{Name: "trim", Usage: "<log> <snapshot> " + driveFl, Summary: "Trim a log up to a snapshot"},
		{Name: "watch", Usage: "[--count=N] [--timeout=<d>] " + driveFl, Summary: "Stream drive events"},
		{Name: "share", Usage: "add <path> <member> [--no-offer] | rm <id> [--no-rotate] | ls " + driveFl, Summary: "Share a folder with someone"},
		{Name: "rotate", Usage: "<path> " + driveFl, Summary: "Rotate a folder's key"},
		{Name: "accept", Usage: "<offer.json|->", Summary: "Accept a share offer"},
		{Name: "mounts", Usage: "[--json]", Summary: "List drives shared with you"},
		{Name: "shared", Usage: "--drive=<identity> [--json]", Summary: "List what a drive shares with you"},
		{Name: "link", Usage: "create <path> [--password=<p>] | rm <id> [--no-rotate] | get <url> <local-file> [--path=<file>] [--password=<p>]", Summary: "Public share links"},
		{Name: "transfer", Usage: "<path> --to=<drive> [--into=</shared-node-id/path>|--to-node=<id>] " + driveFl, Summary: "Copy into another drive"},
	}},
	{Name: "sync", Summary: "Keep a local folder in step with the drive", Subs: []subHelp{
		{Name: "run", Usage: syncArgs, Summary: "Pull, then push, once"},
		{Name: "pull", Usage: syncArgs, Summary: "Download changes only"},
		{Name: "push", Usage: syncArgs, Summary: "Upload changes only"},
		{Name: "status", Usage: syncArgs, Summary: "Show what would change"},
		{Name: "watch", Usage: syncArgs, Summary: "Keep syncing until stopped"},
		{Name: "service", Usage: "<install|uninstall|status> " + syncArgs, Summary: "Run sync in the background at login"},
	}},
	{Name: "auth", Summary: "Sign in to apps with your identity", Subs: []subHelp{
		{Name: "approve", Usage: "<request|link|file> [--code=<digits>] [--sign-with=session|identity] [--no-deliver] [--json]", Summary: "Approve a sign-in request"},
		{Name: "inspect", Usage: "<request-file-or-url> [--json]", Summary: "Show what a sign-in request asks for"},
		{Name: "sign", Usage: "<request-file-or-url> [--use-identity=<id>] [--json]", Summary: "Sign a request without delivering it"},
		{Name: "log", Usage: idFlags, Summary: "Show recent sign-ins"},
	}},
	{Name: "analytics", Summary: "Anonymous usage analytics", Subs: []subHelp{
		{Name: "show", Usage: idFlags, Summary: "Show whether analytics is on"},
		{Name: "on", Usage: idFlags, Summary: "Turn analytics on"},
		{Name: "off", Usage: idFlags, Summary: "Turn analytics off"},
	}},
	{Name: "version", Summary: "Print the CLI version"},
	{Name: "help", Summary: "Help for a command: poweur help <command>"},
}

func findHelp(name string) *cmdHelp {
	for i := range helpTree {
		if helpTree[i].Name == name {
			return &helpTree[i]
		}
	}
	return nil
}

func (c *cmdHelp) sub(name string) *subHelp {
	for i := range c.Subs {
		if c.Subs[i].Name == name {
			return &c.Subs[i]
		}
		for _, a := range c.Subs[i].Aliases {
			if a == name {
				return &c.Subs[i]
			}
		}
	}
	return nil
}

func isHelpFlag(arg string) bool {
	return arg == "-h" || arg == "--help" || arg == "-help"
}

// routeHelp answers help requests and bad subcommands before dispatch. It
// reports handled=false when args is an ordinary, valid invocation.
func routeHelp(args []string, stdout, stderr io.Writer) (code int, handled bool) {
	if len(args) == 0 {
		return 0, false
	}
	if args[0] == "help" || isHelpFlag(args[0]) {
		if len(args) > 1 && args[0] == "help" {
			if c := findHelp(args[1]); c != nil {
				printCommandHelp(stdout, c, args[2:])
				return 0, true
			}
			fmt.Fprintf(stderr, "poweur: unknown command %q\n\n", args[1])
			printHelp(stderr)
			return 1, true
		}
		printHelp(stdout)
		return 0, true
	}
	c := findHelp(args[0])
	if c == nil {
		return 0, false
	}
	rest := args[1:]
	// Asked for explicitly: show help on stdout and succeed. Only the first
	// two words count, so a message that happens to read "-h" is left alone.
	for i, a := range rest {
		if i > 1 {
			break
		}
		if isHelpFlag(a) {
			printCommandHelp(stdout, c, rest[:i])
			return 0, true
		}
	}
	if len(c.Subs) == 0 || (c.Optional && len(rest) == 0) {
		return 0, false
	}
	if len(rest) == 0 {
		fmt.Fprintf(stderr, "poweur %s: subcommand required\n\n", c.Name)
		printCommandHelp(stderr, c, nil)
		return 1, true
	}
	if looksLikeFlag(rest[0]) && c.Optional {
		return 0, false
	}
	if c.sub(rest[0]) == nil {
		fmt.Fprintf(stderr, "poweur %s: unknown subcommand %q\n\n", c.Name, rest[0])
		printCommandHelp(stderr, c, nil)
		return 1, true
	}
	return 0, false
}

// printHelp is the top level: one line per command.
func printHelp(w io.Writer) {
	fmt.Fprint(w, "poweur — encrypted messaging, identity and files\n\nUsage: poweur <command> [subcommand] [options]\n\nCommands:\n")
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	for _, c := range helpTree {
		fmt.Fprintf(tw, "  %s\t%s\n", c.Name, c.Summary)
	}
	tw.Flush()
	fmt.Fprint(w, "\nRun `poweur <command>` to list its subcommands and options,\nor `poweur help <command>`.\n")
}

// printCommandHelp shows one command, or one subcommand when path names it.
func printCommandHelp(w io.Writer, c *cmdHelp, path []string) {
	if len(path) > 0 {
		if s := c.sub(path[0]); s != nil {
			fmt.Fprintf(w, "Usage: poweur %s %s %s\n\n%s\n", c.Name, s.Name, s.Usage, s.Summary)
			return
		}
	}
	if len(c.Subs) == 0 {
		fmt.Fprintf(w, "Usage: poweur %s %s\n\n%s\n", c.Name, c.Usage, c.Summary)
		return
	}
	if c.Optional {
		fmt.Fprintf(w, "Usage: poweur %s [subcommand] [options]\n\n%s\n\nSubcommands:\n", c.Name, c.Summary)
	} else {
		fmt.Fprintf(w, "Usage: poweur %s <subcommand> [options]\n\n%s\n\nSubcommands:\n", c.Name, c.Summary)
	}
	for _, s := range c.Subs {
		name := s.Name
		if len(s.Aliases) > 0 {
			name += " (" + strings.Join(s.Aliases, ", ") + ")"
		}
		fmt.Fprintf(w, "  %s — %s\n", name, s.Summary)
		if s.Usage != "" {
			fmt.Fprintf(w, "      poweur %s %s %s\n", c.Name, s.Name, s.Usage)
		}
	}
	fmt.Fprint(w, "\n<required>  [optional]. `poweur "+c.Name+" <subcommand> --help` shows just one.\n")
}
