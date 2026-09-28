package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"regexp"

	driveclient "github.com/poweur/cli/internal/drive"
)

func runDrive(args []string, stdout, stderr io.Writer) int {
	const usage = "usage: poweur drive <info|node|ls|changes|records> [node-id] [--cursor=...] [--limit=100] [--drive=identity] [--use-identity=...] [--json]"
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 1
	}
	switch args[0] {
	case "put", "get", "mkdir", "mv", "rm", "list":
		return runDriveFiles(args, stdout, stderr)
	case "info", "node", "ls", "changes", "records":
	default:
		fmt.Fprintln(stderr, usage)
		return 1
	}
	fs := flag.NewFlagSet("drive", flag.ContinueOnError)
	fs.SetOutput(stderr)
	identity := fs.String("use-identity", "", "signing identity")
	target := fs.String("drive", "", "drive identity")
	cursor := fs.String("cursor", "", "pagination cursor")
	limit := fs.Int("limit", 100, "page size")
	jsonOut := fs.Bool("json", false, "JSON output")
	if fs.Parse(normalizeArgs(args[1:], map[string]bool{"--json": true})) != nil {
		return 1
	}
	if args[0] == "ls" && fs.NArg() == 1 && !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(fs.Arg(0)) && *limit == 100 {
		return runDriveFiles(append([]string{"list"}, args[1:]...), stdout, stderr)
	}
	needsNode := args[0] == "node" || args[0] == "ls" || args[0] == "records"
	want := 0
	if needsNode {
		want = 1
	}
	if fs.NArg() != want || *limit < 1 || *limit > 1000 {
		fmt.Fprintln(stderr, usage)
		return 1
	}
	suffix := ""
	if needsNode {
		suffix = "/nodes/" + url.PathEscape(fs.Arg(0))
	}
	q := url.Values{"limit": {fmt.Sprint(*limit)}}
	switch args[0] {
	case "ls":
		suffix += "/children"
		q.Set("cursor", *cursor)
	case "changes":
		suffix = "/changes"
		value := *cursor
		if value == "" {
			value = "0"
		}
		q.Set("cursor", value)
	case "records":
		suffix += "/records"
		value := *cursor
		if value == "" {
			value = "0"
		}
		q.Set("from", value)
	}
	if args[0] != "info" && args[0] != "node" {
		suffix += "?" + q.Encode()
	}
	relay, id, key, ok := loadSysSession(*identity, stderr)
	if !ok {
		return 1
	}
	client := driveclient.Client{Relay: relay, Identity: id, Drive: *target, Key: key}
	var result json.RawMessage
	if err := client.Get(context.Background(), suffix, &result); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeOutput(stdout, *jsonOut, result, string(result))
}
