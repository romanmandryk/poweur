package cli

import (
	"bytes"
	"testing"
)

func TestDriveUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"node"}, {"info", "extra"}, {"changes", "--limit=0"}, {"ls", "node", "--limit=1001"}} {
		var out, stderr bytes.Buffer
		if code := runDrive(args, &out, &stderr); code != 1 || stderr.Len() == 0 {
			t.Fatalf("%v: code=%d, stderr=%s", args, code, stderr.String())
		}
	}
}
