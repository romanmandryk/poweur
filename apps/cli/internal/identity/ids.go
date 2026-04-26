package identity

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"
)

// NewMessageID returns a fresh client-assigned message identifier. The
// format is `msg_<timestamp-ms>_<rand>` — sortable by send time, globally
// unique with overwhelming probability, and short enough to print on a
// single terminal line. Keep the `msg_` prefix so logs and tooling can
// distinguish messages from acks (`ack_`) at a glance.
func NewMessageID() string {
	return newPrefixedID("msg")
}

// NewAckID returns a fresh client-assigned ack identifier with the same
// shape as NewMessageID but a `ack_` prefix.
func NewAckID() string {
	return newPrefixedID("ack")
}

func newPrefixedID(prefix string) string {
	buf := make([]byte, 9)
	if _, err := rand.Read(buf); err != nil {
		// Random source failures shouldn't happen on production hosts;
		// fall back to a timestamp-only id rather than panicking so a
		// degraded host can still send messages.
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	suffix := base64.RawURLEncoding.EncodeToString(buf)
	return fmt.Sprintf("%s_%d_%s", prefix, time.Now().UnixMilli(), suffix)
}
