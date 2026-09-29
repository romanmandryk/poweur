package cli

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/poweur/cli/internal/config"
	driveclient "github.com/poweur/cli/internal/drive"
	"github.com/poweur/cli/internal/identity"
	idpkg "github.com/poweur/identity"
	protocol "github.com/poweur/identity/drive"
)

func attachmentMIME(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".md":
		return "text/plain"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".pdf":
		return "application/pdf"
	case ".json":
		return "application/json"
	default:
		return "application/octet-stream"
	}
}

func ownerFiles(relay, owner string, priv ed25519.PrivateKey, enc []byte) *driveclient.Files {
	home, _ := os.UserHomeDir()
	return &driveclient.Files{Client: &driveclient.Client{
		Relay: relay, Identity: owner, Key: priv,
		Cache: driveclient.FileCache{Dir: filepath.Join(home, ".poweur", "cache", "drive-chunks")},
	}, EncryptionKey: enc}
}

func mkdirDrive(ctx context.Context, files *driveclient.Files, parent *driveclient.File, name string) (*driveclient.File, error) {
	children, err := files.List(ctx, parent)
	if err != nil {
		return nil, err
	}
	for _, child := range children {
		if child.Name == name && child.Manifest.Kind == protocol.KindFolder {
			return child, nil
		}
	}
	created, err := files.Create(ctx, parent, name, protocol.KindFolder, nil)
	if err == nil {
		return created, nil
	}
	var status *driveclient.Error
	if !errors.As(err, &status) || status.Status != 409 {
		return nil, err
	}
	children, err = files.List(ctx, parent)
	if err != nil {
		return nil, err
	}
	for _, child := range children {
		if child.Name == name {
			return child, nil
		}
	}
	return nil, err
}

// prepareAttachment stores the file on the sender's drive, shares it read-only
// with the recipient, and returns the encrypted-message payload plus the
// plaintext metadata.
func prepareAttachment(ctx context.Context, relay, owner string, priv ed25519.PrivateKey, encPriv, recipientPub []byte, recipient, path, caption string) (string, map[string]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", nil, err
	}
	if info.Size() > idpkg.MaxAttachmentBytes {
		return "", nil, fmt.Errorf("attachment exceeds %d bytes", idpkg.MaxAttachmentBytes)
	}
	input, err := os.Open(path)
	if err != nil {
		return "", nil, err
	}
	defer input.Close()
	files := ownerFiles(relay, owner, priv, encPriv)
	root, err := files.Root(ctx)
	if err != nil {
		return "", nil, err
	}
	poweur, err := mkdirDrive(ctx, files, root, ".poweur")
	if err != nil {
		return "", nil, err
	}
	private, err := mkdirDrive(ctx, files, poweur, "private")
	if err != nil {
		return "", nil, err
	}
	dir, err := mkdirDrive(ctx, files, private, "attachments")
	if err != nil {
		return "", nil, err
	}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		return "", nil, err
	}
	file, err := files.Create(ctx, dir, hex.EncodeToString(id[:]), protocol.KindFile, input)
	if err != nil {
		return "", nil, err
	}
	if _, err = files.ShareWith(ctx, file, recipient, recipientPub, protocol.RoleRead, ""); err != nil {
		return "", nil, err
	}
	sum, size, err := files.CiphertextHash(ctx, file)
	if err != nil {
		return "", nil, err
	}
	payload := idpkg.Attachment{
		Format: idpkg.AttachmentFormat, Name: filepath.Base(path), MIME: attachmentMIME(path),
		ContentKey: base64.RawURLEncoding.EncodeToString(file.ContentKey), Caption: caption,
	}
	raw, err := payload.Marshal()
	if err != nil {
		return "", nil, err
	}
	return string(raw), idpkg.AttachmentMetadata(file.Manifest.Node, size, sum), nil
}

func runAttach(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "save" {
		fmt.Fprintln(stderr, "usage: poweur attach save <peer> --id=<message-id> --out=<file> [--json]")
		return 1
	}
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	fs.SetOutput(stderr)
	use := fs.String("use-identity", "", "identity")
	id := fs.String("id", "", "history message id")
	out := fs.String("out", "", "destination file")
	jsonOut := fs.Bool("json", false, "JSON output")
	if fs.Parse(normalizeArgs(args[1:], map[string]bool{"--json": true})) != nil {
		return 1
	}
	peer := ""
	if fs.NArg() == 1 {
		peer = strings.ToLower(fs.Arg(0))
	}
	if peer == "" || *id == "" || *out == "" {
		fmt.Fprintln(stderr, "usage: poweur attach save <peer> --id=<message-id> --out=<file>")
		return 1
	}
	store, err := openHistoryStore(*use)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx := context.Background()
	rows, err := store.conversationRecords(ctx, peer)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var record idpkg.HistoryRecord
	found := false
	for _, row := range rows {
		if row.Record.ID == *id {
			record = row.Record
			found = true
		}
	}
	if !found {
		fmt.Fprintln(stderr, "no attachment with that id")
		return 1
	}
	payload, err := idpkg.ParseAttachment([]byte(record.Body))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	key, err := payload.ContentKeyBytes()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	node := record.Metadata[idpkg.AttachmentMetaNode]
	wantSize, err := strconv.ParseUint(record.Metadata[idpkg.AttachmentMetaSize], 10, 64)
	if node == "" || err != nil || record.Metadata[idpkg.AttachmentMetaHash] == "" {
		fmt.Fprintln(stderr, "attachment metadata is incomplete")
		return 1
	}
	host := record.Sender
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	relay := cfg.RelayURL
	if !strings.EqualFold(host, store.identity) {
		relay, err = resolveRecipientRelayURL(ctx, host, cfg)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	resolved, err := identity.ResolveIdentity(ctx, host)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	author, err := idpkg.ParseEd25519PublicKey(resolved.Document.PublicKey)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	reader := ownerFiles(relay, store.identity, store.priv, store.encPriv)
	reader.Client.Drive = host
	plain, sum, size, err := reader.ReadContent(ctx, node, key, author)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if sum != record.Metadata[idpkg.AttachmentMetaHash] || size != wantSize {
		fmt.Fprintln(stderr, "attachment ciphertext does not match the message")
		return 1
	}
	tmp, err := os.CreateTemp(filepath.Dir(*out), ".poweur-attach-*")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(plain); err != nil {
		tmp.Close()
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err = tmp.Close(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err = os.Rename(tmp.Name(), *out); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeOutput(stdout, *jsonOut, map[string]string{"name": payload.Name, "path": *out, "bytes": strconv.Itoa(len(plain))}, payload.Name+"\n")
}
