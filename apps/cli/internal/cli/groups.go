package cli

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/poweur/cli/internal/config"
	"github.com/poweur/cli/internal/identity"
	idpkg "github.com/poweur/identity"
)

// Group identities (EPIC-005 E05-T5).
//
// A group is a *hosted identity* — an ordinary Poweur ID with its own key —
// and one document in its own tree makes it a group:
// poweur-sys/relay/groups/self.json, a signed member list carrying an admin
// list and a monotonic epoch. Because the group is an identity, any owner
// can name it in a grant (`--with-group team.acme.poweur.net`) and, once
// EPIC-009 lands, message it.
//
// Authority in v1 is possession of the group's identity key: the membership
// document lives in the group's own tree, so writing it needs a DAV token
// only that key can mint, and it is signed with that key so the relay
// verifies it like any other identity's document. The `admins` list records
// who legitimately holds it — which is what audit and E09-T5 need — and
// this CLI refuses to act for a non-admin. Per-admin signed updates, so
// that authority is cryptographic rather than recorded, are the follow-up
// named in the design doc.

func runGroup(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: poweur group <create|show|add|remove>")
		return 1
	}
	switch args[0] {
	case "create":
		return runGroupCreate(args[1:], stdout, stderr)
	case "show":
		return runGroupShow(args[1:], stdout, stderr)
	case "add":
		return runGroupUpdate(args[1:], stdout, stderr, true)
	case "remove":
		return runGroupUpdate(args[1:], stdout, stderr, false)
	default:
		fmt.Fprintln(stderr, "unknown group subcommand (want create, show, add, remove)")
		return 1
	}
}

// groupSession is everything needed to read and write one group's
// membership document: the group speaks for itself, so the token is minted
// with the group's own key.
type groupSession struct {
	relayURL string
	groupID  string
	priv     ed25519.PrivateKey
	token    string
	// actor is the identity running the command, checked against the
	// group's admin list.
	actor string
}

func openGroupSession(groupID, useIdentity string, stderr io.Writer) (groupSession, bool) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return groupSession{}, false
	}
	if cfg.RelayURL == "" {
		fmt.Fprintln(stderr, "relay url not configured")
		return groupSession{}, false
	}
	priv, err := identity.LoadPrivateKey(identity.KeyPath(cfg.KeysDir, groupID))
	if err != nil {
		fmt.Fprintf(stderr, "no key for group %s on this device: %v\n", groupID, err)
		return groupSession{}, false
	}
	tok, err := MintDAVToken(context.Background(), cfg.RelayURL, groupID, "", "dav:full", priv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return groupSession{}, false
	}
	return groupSession{
		relayURL: cfg.RelayURL,
		groupID:  groupID,
		priv:     priv,
		token:    tok.Token,
		actor:    resolveIdentity(useIdentity, cfg.Identity),
	}, true
}

// load reads and verifies the group's current membership document.
func (gs groupSession) load(stderr io.Writer) (idpkg.ShareGroup, bool) {
	raw, status, err := davGetBytes(context.Background(), gs.relayURL, gs.groupID, gs.token, idpkg.GroupSelfDoc)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return idpkg.ShareGroup{}, false
	}
	if status == http.StatusNotFound {
		fmt.Fprintf(stderr, "group %s has no membership document yet (run `poweur group create`)\n", gs.groupID)
		return idpkg.ShareGroup{}, false
	}
	if status != http.StatusOK {
		fmt.Fprintf(stderr, "read group membership failed: HTTP %d\n", status)
		return idpkg.ShareGroup{}, false
	}
	group, err := idpkg.ParseShareGroup(raw)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return idpkg.ShareGroup{}, false
	}
	if !group.IsGroupIdentity() {
		fmt.Fprintf(stderr, "%s is not a group identity\n", gs.groupID)
		return idpkg.ShareGroup{}, false
	}
	return group, true
}

// store signs the membership document with the group's key and writes it.
func (gs groupSession) store(group idpkg.ShareGroup, stderr io.Writer) bool {
	group.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := group.Sign(gs.priv); err != nil {
		fmt.Fprintln(stderr, err)
		return false
	}
	raw, err := json.MarshalIndent(group, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return false
	}
	if err := davPutBytes(context.Background(), gs.relayURL, gs.groupID, gs.token, idpkg.GroupSelfDoc, raw); err != nil {
		fmt.Fprintln(stderr, err)
		return false
	}
	return true
}

func runGroupCreate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("group create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var admins, members stringList
	fs.Var(&admins, "admin", "group admin Poweur ID (repeatable; default: the active identity)")
	fs.Var(&members, "member", "group member Poweur ID (repeatable)")
	relayURL := fs.String("relay", "", "relay base url (default: the configured relay)")
	useIdentity := fs.String("use-identity", "", "identity creating the group")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "usage: poweur group create <group-id> [--admin <id>] [--member <id>]")
		return 1
	}
	groupID := strings.ToLower(strings.TrimSpace(rest[0]))
	if !idpkg.IsGroupIdentityName(groupID) {
		fmt.Fprintln(stderr, "a group identity needs a full Poweur ID (e.g. team.acme.poweur.net); for an owner-local group use `poweur share group set`")
		return 1
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	actor := resolveIdentity(*useIdentity, cfg.Identity)
	if actor == "" {
		fmt.Fprintln(stderr, "identity not configured (run `poweur identity create` or pass --use-identity)")
		return 1
	}
	if *relayURL == "" {
		*relayURL = cfg.RelayURL
	}
	if len(admins) == 0 {
		// A group with no admin is a group nobody can ever change.
		admins = stringList{actor}
	}
	// An admin who is not a member is legitimate, but the common case is
	// that whoever creates the group is in it.
	if len(members) == 0 {
		members = stringList{actor}
	}

	// Register the group as an ordinary hosted identity, then put the
	// active identity back: creating a group must not quietly switch which
	// identity the next command speaks as.
	createArgs := []string{groupID, "--hosted", "--json"}
	if *relayURL != "" {
		createArgs = append(createArgs, "--relay", *relayURL)
	}
	var registerOut strings.Builder
	if code := runIdentityCreate(createArgs, &registerOut, stderr); code != 0 {
		return code
	}
	if restored, err := config.Load(); err == nil {
		restored.Identity = actor
		if err := config.Save(restored); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	gs, ok := openGroupSession(groupID, *useIdentity, stderr)
	if !ok {
		return 1
	}
	group := idpkg.ShareGroup{
		Group:   groupID,
		Owner:   groupID, // a group identity is its own owner
		Members: normalizeIDs(members),
		Admins:  normalizeIDs(admins),
		Epoch:   1,
	}
	if err := group.Validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !gs.store(group, stderr) {
		return 1
	}
	return writeOutput(stdout, *jsonOut, group, fmt.Sprintf(
		"group identity %s created (epoch %d)\n  admins: %s\n  members: %s\n\n"+
			"Share with it using: poweur share add <path> --with-group %s\n",
		groupID, group.Epoch,
		strings.Join(group.Admins, ", "), strings.Join(group.Members, ", "), groupID))
}

func runGroupShow(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("group show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "usage: poweur group show <group-id>")
		return 1
	}
	gs, ok := openGroupSession(strings.ToLower(strings.TrimSpace(rest[0])), *useIdentity, stderr)
	if !ok {
		return 1
	}
	group, ok := gs.load(stderr)
	if !ok {
		return 1
	}
	if *jsonOut {
		return writeOutput(stdout, true, group, "")
	}
	fmt.Fprintf(stdout, "%s (epoch %d, updated %s)\n", group.Group, group.Epoch, group.UpdatedAt)
	fmt.Fprintf(stdout, "  admins:  %s\n", strings.Join(group.Admins, ", "))
	if len(group.Members) == 0 {
		fmt.Fprintln(stdout, "  members: (none)")
	} else {
		fmt.Fprintf(stdout, "  members: %s\n", strings.Join(group.Members, ", "))
	}
	return 0
}

// runGroupUpdate applies one signed membership update. add == false removes.
func runGroupUpdate(args []string, stdout, stderr io.Writer, add bool) int {
	verb := "remove"
	if add {
		verb = "add"
	}
	fs := flag.NewFlagSet("group "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var admins, members stringList
	fs.Var(&admins, "admin", "admin Poweur ID (repeatable)")
	fs.Var(&members, "member", "member Poweur ID (repeatable)")
	useIdentity := fs.String("use-identity", "", "identity performing the update")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintf(stderr, "usage: poweur group %s <group-id> [--member <id>] [--admin <id>]\n", verb)
		return 1
	}
	if len(admins) == 0 && len(members) == 0 {
		fmt.Fprintln(stderr, "at least one --member or --admin is required")
		return 1
	}
	gs, ok := openGroupSession(strings.ToLower(strings.TrimSpace(rest[0])), *useIdentity, stderr)
	if !ok {
		return 1
	}
	group, ok := gs.load(stderr)
	if !ok {
		return 1
	}
	// The real gate is the group key, which this device evidently holds.
	// This check is what turns "you can" into "you are supposed to", and
	// catches the honest mistake of administering a group you left.
	if gs.actor != "" && !group.HasAdmin(gs.actor) {
		fmt.Fprintf(stderr, "%s is not an admin of %s\n", gs.actor, group.Group)
		return 1
	}

	before := len(group.Members) + len(group.Admins)
	if add {
		group.Members = normalizeIDs(append(group.Members, members...))
		group.Admins = normalizeIDs(append(group.Admins, admins...))
	} else {
		group.Members = removeIDs(group.Members, members)
		group.Admins = removeIDs(group.Admins, admins)
	}
	if len(group.Admins) == 0 {
		fmt.Fprintln(stderr, "refusing to remove the last admin: nobody could change the group again")
		return 1
	}
	if before == len(group.Members)+len(group.Admins) {
		// Nothing changed, so there is nothing to sign. Bumping the epoch
		// anyway would churn EPIC-009's key agreement for no reason.
		fmt.Fprintln(stdout, "no change")
		return 0
	}
	group.Epoch++
	if err := group.Validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !gs.store(group, stderr) {
		return 1
	}
	return writeOutput(stdout, *jsonOut, group, fmt.Sprintf(
		"group %s updated (epoch %d)\n  admins:  %s\n  members: %s\n",
		group.Group, group.Epoch,
		strings.Join(group.Admins, ", "), strings.Join(group.Members, ", ")))
}

// normalizeIDs lowercases, trims, de-duplicates and sorts a list of Poweur
// IDs, so a membership document has one representation regardless of how it
// was typed and the canonical string is stable.
func normalizeIDs(ids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func removeIDs(from, drop []string) []string {
	gone := map[string]bool{}
	for _, d := range drop {
		gone[strings.ToLower(strings.TrimSpace(d))] = true
	}
	out := make([]string, 0, len(from))
	for _, id := range from {
		if !gone[strings.ToLower(strings.TrimSpace(id))] {
			out = append(out, id)
		}
	}
	return normalizeIDs(out)
}
