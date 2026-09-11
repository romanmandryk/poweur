package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/poweur/cli/internal/config"
	idpkg "github.com/poweur/identity"
)

func attachmentMIME(name string, data []byte) string {
	if value := mime.TypeByExtension(filepath.Ext(name)); value != "" {
		return strings.Split(value, ";")[0]
	}
	return http.DetectContentType(data[:min(len(data), 512)])
}

func attachmentMKCOL(ctx context.Context, relayURL, owner, token, path string) error {
	req, err := http.NewRequestWithContext(ctx, "MKCOL", davFileURL(relayURL, owner, path), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusMethodNotAllowed {
		return parseErrorResponse("create attachment directory failed", resp)
	}
	return nil
}

func attachmentPut(ctx context.Context, relayURL, owner, token, path string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, davFileURL(relayURL, owner, path), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return parseErrorResponse("upload attachment failed", resp)
	}
	return nil
}

func prepareAttachment(ctx context.Context, cfg config.Config, owner string, priv ed25519.PrivateKey,
	recipient, localPath string) (idpkg.AttachmentRef, error) {
	info, err := os.Stat(localPath)
	if err != nil {
		return idpkg.AttachmentRef{}, err
	}
	if !info.Mode().IsRegular() {
		return idpkg.AttachmentRef{}, errors.New("attachment must be a regular file")
	}
	if info.Size() > idpkg.MaxAttachmentBytes {
		return idpkg.AttachmentRef{}, fmt.Errorf("attachment exceeds 20 MB")
	}
	data, err := os.ReadFile(localPath)
	if err != nil {
		return idpkg.AttachmentRef{}, err
	}
	shareID, err := newShareID()
	if err != nil {
		return idpkg.AttachmentRef{}, err
	}
	name := filepath.Base(localPath)
	attachmentID := "att_" + strings.TrimPrefix(shareID, "shr_")
	treePath := "shared/.attachments/" + attachmentID + "/" + name
	sum := sha256.Sum256(data)
	ref := idpkg.AttachmentRef{Owner: owner, Path: treePath, Name: name, Size: int64(len(data)),
		MIME: attachmentMIME(name, data), SHA256: hex.EncodeToString(sum[:]), ShareID: shareID}
	if err := ref.Validate(); err != nil {
		return idpkg.AttachmentRef{}, err
	}
	tok, err := MintDAVToken(ctx, cfg.RelayURL, owner, owner, "dav:full", priv)
	if err != nil {
		return idpkg.AttachmentRef{}, err
	}
	for _, directory := range []string{"shared/.attachments", "shared/.attachments/" + attachmentID} {
		if err := attachmentMKCOL(ctx, cfg.RelayURL, owner, tok.Token, directory); err != nil {
			return idpkg.AttachmentRef{}, err
		}
	}
	if err := attachmentPut(ctx, cfg.RelayURL, owner, tok.Token, treePath, data); err != nil {
		return idpkg.AttachmentRef{}, err
	}
	grant := idpkg.ShareGrant{ShareID: shareID, Owner: owner, Path: treePath,
		Audience: []idpkg.ShareAudience{{ID: recipient}}, Permissions: []string{idpkg.PermRead},
		CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := grant.Sign(priv); err != nil {
		return idpkg.AttachmentRef{}, err
	}
	raw, _ := json.MarshalIndent(grant, "", "  ")
	if err := davPutBytes(ctx, cfg.RelayURL, owner, tok.Token, sharesTreeDir+"/"+shareID+".json", raw); err != nil {
		_, _ = davDeleteFile(ctx, cfg.RelayURL, owner, tok.Token, treePath)
		return idpkg.AttachmentRef{}, err
	}
	return ref, nil
}

func runAttachment(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: poweur attachment <get|rm>")
		return 1
	}
	if args[0] == "rm" {
		fs := flag.NewFlagSet("attachment rm", flag.ContinueOnError)
		fs.SetOutput(stderr)
		useIdentity := fs.String("use-identity", "", "attachment owner")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 2 {
			fmt.Fprintln(stderr, "usage: poweur attachment rm <share-id> <shared-path>")
			return 1
		}
		shareID, path := fs.Arg(0), strings.TrimPrefix(fs.Arg(1), "/")
		normalized, err := idpkg.NormalizeGrantPath(path)
		if err != nil || !strings.HasPrefix(normalized, "shared/.attachments/") {
			fmt.Fprintln(stderr, "invalid attachment path")
			return 1
		}
		path = normalized
		relayURL, owner, token, ok := loadShareSession(*useIdentity, stderr)
		if !ok {
			return 1
		}
		ctx := context.Background()
		if status, err := davDeleteFile(ctx, relayURL, owner, token, sharesTreeDir+"/"+shareID+".json"); err != nil || (status >= 300 && status != http.StatusNotFound) {
			fmt.Fprintf(stderr, "revoke attachment grant failed (status %d): %v\n", status, err)
			return 1
		}
		if status, err := davDeleteFile(ctx, relayURL, owner, token, path); err != nil || (status >= 300 && status != http.StatusNotFound) {
			fmt.Fprintf(stderr, "delete attachment failed (status %d): %v\n", status, err)
			return 1
		}
		fmt.Fprintf(stdout, "removed attachment %s and revoked %s\n", path, shareID)
		return 0
	}
	if args[0] != "get" {
		fmt.Fprintln(stderr, "usage: poweur attachment <get|rm>")
		return 1
	}
	fs := flag.NewFlagSet("attachment get", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity receiving the attachment")
	wantHash := fs.String("sha256", "", "expected SHA-256 from the message")
	output := fs.String("output", "", "destination file (default attachment name)")
	force := fs.Bool("force", false, "overwrite destination")
	if err := fs.Parse(normalizeArgs(args[1:], map[string]bool{"--force": true})); err != nil {
		return 1
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(stderr, "usage: poweur attachment get <owner> <shared-path> [--sha256=<hex>] [--output=<file>]")
		return 1
	}
	owner, path := strings.ToLower(fs.Arg(0)), strings.TrimPrefix(fs.Arg(1), "/")
	if normalized, err := idpkg.NormalizeGrantPath(path); err != nil || !strings.HasPrefix(normalized, "shared/.attachments/") {
		fmt.Fprintln(stderr, "invalid attachment path")
		return 1
	}
	cfg, identityValue, priv, ok := loadIdentityForDAV(*useIdentity, stderr)
	if !ok {
		return 1
	}
	relayURL, err := resolveRecipientRelayURL(context.Background(), owner, cfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	tok, err := MintDAVToken(context.Background(), relayURL, identityValue, owner, "dav:read", priv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	raw, status, err := davGetBytes(context.Background(), relayURL, owner, tok.Token, path)
	if err != nil || status != http.StatusOK {
		fmt.Fprintf(stderr, "download failed (status %d): %v\n", status, err)
		return 1
	}
	sum := sha256.Sum256(raw)
	gotHash := hex.EncodeToString(sum[:])
	if *wantHash != "" && !strings.EqualFold(*wantHash, gotHash) {
		fmt.Fprintln(stderr, "attachment hash mismatch")
		return 1
	}
	destination := *output
	if destination == "" {
		destination = filepath.Base(path)
	}
	flags := os.O_WRONLY | os.O_CREATE
	if *force {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(destination, flags, 0o600)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if _, err := file.Write(raw); err != nil {
		file.Close()
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := file.Close(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "saved %s (%d bytes, sha256 %s)\n", destination, len(raw), gotHash)
	return 0
}
