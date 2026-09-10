package cli

import (
	"bytes"
	"testing"
)

func TestAnalyticsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"invalid"}, {"on", "--invalid"}, {"on", "extra"}} {
		var out, err bytes.Buffer
		if runAnalytics(args, &out, &err) == 0 {
			t.Fatalf("accepted invalid arguments: %v", args)
		}
	}
}
