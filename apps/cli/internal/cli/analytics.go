package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"
)

const analyticsTreePath = "poweur-sys/relay/analytics.json"

func runAnalytics(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "show" && args[0] != "on" && args[0] != "off") {
		fmt.Fprintln(stderr, "usage: poweur analytics <show|on|off> [--use-identity=...] [--json]")
		return 1
	}
	fs := flag.NewFlagSet("analytics", flag.ContinueOnError)
	fs.SetOutput(stderr)
	identity := fs.String("use-identity", "", "identity")
	jsonOut := fs.Bool("json", false, "JSON output")
	if fs.Parse(normalizeArgs(args[1:], map[string]bool{"--json": true})) != nil {
		return 1
	}
	relayURL, id, token, ok := loadShareSession(*identity, stderr)
	if !ok {
		return 1
	}
	p := struct {
		Version   int    `json:"version"`
		Granted   bool   `json:"granted"`
		UpdatedAt string `json:"updated_at,omitempty"`
	}{Version: 1}
	if args[0] == "show" {
		b, status, err := davGetBytes(context.Background(), relayURL, id, token, analyticsTreePath)
		if err != nil || (status != 200 && status != http.StatusNotFound) {
			fmt.Fprintln(stderr, "could not read analytics preference")
			return 1
		}
		if status == 200 {
			if json.Unmarshal(b, &p) != nil || p.Version != 1 {
				fmt.Fprintln(stderr, "invalid analytics preference")
				return 1
			}
		}
	} else {
		p.Granted = args[0] == "on"
		p.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		b, _ := json.Marshal(p)
		if err := davPutBytes(context.Background(), relayURL, id, token, analyticsTreePath, b); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	return writeOutput(stdout, *jsonOut, p, fmt.Sprintf("Detailed relay analytics: %t (off uses hashed identity, no IP)", p.Granted))
}
