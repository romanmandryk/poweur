package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	driveclient "github.com/poweur/cli/internal/drive"
	idpkg "github.com/poweur/identity"
	protocol "github.com/poweur/identity/drive"
)

// Share offers (PCP-0008). Sharing sends the member an encrypted
// sys.share.offer; accepting records a mount in the member's own drive
// (.poweur/private/mounts.json) and answers sys.share.accept; revoking sends
// sys.share.revoked. The messages carry no authority: access is always the
// share on the owner's relay.

const mountsName = "mounts.json"

// sendDriveMessage sends one typed, end-to-end encrypted message.
func sendDriveMessage(use, recipient, msgType, shareID string, body any, stderr io.Writer) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	args := []string{recipient, string(raw), "--type", msgType, "--meta", "share_id=" + shareID,
		"--expires", time.Now().UTC().Add(7 * 24 * time.Hour).Format(time.RFC3339), "--request-on-reject"}
	if use != "" {
		args = append(args, "--use-identity", use)
	}
	if code := runSend(args, io.Discard, stderr); code != 0 {
		return fmt.Errorf("could not send %s to %s", msgType, recipient)
	}
	return nil
}

// offerShare tells the member about a share just made on file.
func offerShare(use string, files *driveclient.Files, file *driveclient.File, share protocol.Share, stderr io.Writer) error {
	relay := files.Client.Relay
	if u, err := url.Parse(relay); err == nil && u.Host != "" {
		relay = u.Host
	}
	offer := protocol.ShareOffer{Format: protocol.OfferFormat, Share: share, Relay: relay, Name: file.Name, Kind: file.Manifest.Kind,
		OfferedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := offer.Validate(); err != nil {
		return err
	}
	return sendDriveMessage(use, share.Member, idpkg.MsgTypeShareOffer, share.ID, offer, stderr)
}

// privateFolder opens (creating as needed) .poweur/private in the caller's drive.
func privateFolder(ctx context.Context, files *driveclient.Files) (*driveclient.File, error) {
	current, err := files.Root(ctx)
	if err != nil {
		return nil, err
	}
	for _, name := range []string{".poweur", "private"} {
		children, err := files.List(ctx, current)
		if err != nil {
			return nil, err
		}
		var next *driveclient.File
		for _, child := range children {
			if child.Name == name && child.Manifest.Kind == protocol.KindFolder {
				next = child
			}
		}
		if next == nil {
			if next, err = files.Create(ctx, current, name, protocol.KindFolder, nil); err != nil {
				return nil, err
			}
		}
		current = next
	}
	return current, nil
}

// readMounts loads the caller's mounts file (and its node, if it exists).
func readMounts(ctx context.Context, files *driveclient.Files) (protocol.Mounts, *driveclient.File, *driveclient.File, error) {
	var mounts protocol.Mounts
	folder, err := privateFolder(ctx, files)
	if err != nil {
		return mounts, nil, nil, err
	}
	children, err := files.List(ctx, folder)
	if err != nil {
		return mounts, folder, nil, err
	}
	for _, child := range children {
		if child.Name != mountsName {
			continue
		}
		var buf strings.Builder
		if err := files.Read(ctx, child, &buf); err != nil {
			return mounts, folder, nil, err
		}
		if err := json.Unmarshal([]byte(buf.String()), &mounts); err != nil {
			return mounts, folder, nil, fmt.Errorf("mounts file: %w", err)
		}
		return mounts, folder, child, nil
	}
	return protocol.Mounts{Format: 1, Mounts: []protocol.Mount{}}, folder, nil, nil
}

func writeMounts(ctx context.Context, files *driveclient.Files, folder, existing *driveclient.File, mounts protocol.Mounts) error {
	raw, err := json.MarshalIndent(mounts, "", "  ")
	if err != nil {
		return err
	}
	if existing != nil {
		return files.Replace(ctx, existing, strings.NewReader(string(raw)))
	}
	_, err = files.Create(ctx, folder, mountsName, protocol.KindFile, strings.NewReader(string(raw)))
	return err
}

// acceptOffer verifies an offer by opening the shared node through it, then
// mounts it and tells the owner.
func acceptOffer(use string, raw []byte, stderr io.Writer) (protocol.Mount, error) {
	ctx := context.Background()
	var offer protocol.ShareOffer
	if err := json.Unmarshal(raw, &offer); err != nil {
		return protocol.Mount{}, fmt.Errorf("offer: %w", err)
	}
	if err := offer.Validate(); err != nil {
		return protocol.Mount{}, err
	}
	own, ok := openDriveFiles(use, "", stderr)
	if !ok {
		return protocol.Mount{}, errors.New("cannot open your drive")
	}
	if offer.Share.Member != own.Client.Identity {
		return protocol.Mount{}, fmt.Errorf("this offer is for %s", offer.Share.Member)
	}
	shared, ok := openDriveFiles(use, offer.Share.Drive, stderr)
	if !ok {
		return protocol.Mount{}, errors.New("cannot reach the shared drive")
	}
	// The offer proves nothing by itself: it must be exactly the share the
	// relay holds (same signed hash), and opening the node checks that
	// share's signature, the issuer's authority and that its key fits.
	offered, err := offer.Share.Hash()
	if err != nil {
		return protocol.Mount{}, err
	}
	held, err := shared.Client.Shares(ctx)
	if err != nil {
		return protocol.Mount{}, err
	}
	matched := false
	for _, s := range held {
		if s.ID == offer.Share.ID {
			hash, err := s.Hash()
			matched = err == nil && hash == offered
		}
	}
	if !matched {
		return protocol.Mount{}, errors.New("the offer does not match any share the drive holds for you")
	}
	if _, err := shared.Open(ctx, offer.Share.Node); err != nil {
		return protocol.Mount{}, fmt.Errorf("the share does not open: %w", err)
	}
	mounts, folder, existing, err := readMounts(ctx, own)
	if err != nil {
		return protocol.Mount{}, err
	}
	accept, err := mounts.Accept(offer, time.Now())
	if err != nil {
		return protocol.Mount{}, err
	}
	if err := writeMounts(ctx, own, folder, existing, mounts); err != nil {
		return protocol.Mount{}, err
	}
	if err := sendDriveMessage(use, offer.Share.Issuer, idpkg.MsgTypeShareAccept, offer.Share.ID, accept, stderr); err != nil {
		fmt.Fprintln(stderr, "mounted, but the owner was not told:", err)
	}
	return mounts.Mounts[len(mounts.Mounts)-1], nil
}

// runDriveOffers handles `poweur drive accept <offer.json|->` and `mounts`.
func runDriveOffers(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("drive", flag.ContinueOnError)
	fs.SetOutput(stderr)
	use := fs.String("use-identity", "", "identity")
	jsonOut := fs.Bool("json", false, "JSON output")
	if fs.Parse(normalizeArgs(args[1:], map[string]bool{"--json": true})) != nil {
		return 1
	}
	switch {
	case args[0] == "accept" && fs.NArg() == 1:
		var raw []byte
		var err error
		if fs.Arg(0) == "-" {
			raw, err = io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
		} else {
			raw, err = os.ReadFile(fs.Arg(0))
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		mount, err := acceptOffer(*use, raw, stderr)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return writeOutput(stdout, *jsonOut, mount, fmt.Sprintf("Shared/%s/%s\t/%s\n", mount.Drive, mount.Name, mount.Node))
	case args[0] == "mounts" && fs.NArg() == 0:
		own, ok := openDriveFiles(*use, "", stderr)
		if !ok {
			return 1
		}
		mounts, _, _, err := readMounts(context.Background(), own)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		var text strings.Builder
		for _, m := range mounts.Mounts {
			fmt.Fprintf(&text, "Shared/%s/%s\t%s\t--drive %s /%s\n", m.Drive, m.Name, m.Role, m.Drive, m.Node)
		}
		return writeOutput(stdout, *jsonOut, mounts, text.String())
	}
	fmt.Fprintln(stderr, "usage: poweur drive accept <offer.json|->; mounts [--json]")
	return 1
}
