package qoder

// PLUGIN-SERVED (see AGENTS.md): Qoder ("qoder") and Qoder CN ("qoder-cn")
// are deprecated built-in subscriptions served by their plugin,
// @magpie-community/opencode-qoder-auth, each once moved onto it
// (provider.Moved; the default for a new sign-in). A moved one's
// sign-ins, models, requests and usage are all the plugin's, never this
// code's (only the move, in migrate*.go, still reads its accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/qoder) and raise the
// movers' min in internal/provider/migrate_qoder.go.

import (
	"crypto/rand"
	"encoding/hex"
)

// NewMachineID generates the stable identity kept with one account.
func NewMachineID() string { return newUUID() }

// newUUID returns a random v4 UUID in the dashed 8-4-4-4-12 form Qoder's
// machine_id and nonce carry. A UUID source is kept local so magpie's Qoder
// support needs no new dependency.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// NewID is a UUID without dashes, the shape Qoder's request ids and tool-call
// ids use.
func NewID() string {
	h := hex.EncodeToString(uuidBytes())
	return h
}

func uuidBytes() []byte {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return b[:]
}
