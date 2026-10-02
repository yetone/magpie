package provider

// PLUGIN-SERVED (see AGENTS.md): ZCode ("zcode") is a deprecated built-in
// subscription served by its plugin, @magpie-community/opencode-zcode-auth,
// once moved onto it (provider.Moved; the default for a new sign-in). A
// moved one's sign-ins, models, requests and usage are all the plugin's,
// never this code's (only the move, in migrate*.go, still reads its
// accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/zcode) and raise the
// mover's min in internal/provider/migrate_zcode.go.

// ZCode names the machine it runs on to zcode.z.ai: its apiClient sends
// X-Device-Mid, a UUID it keeps as deviceMid, on every request, and
// /api/v1/zcode-plan/billing/balance refuses one without it (400, code
// 3001 "parameter error", #282). The server only asks that it is there, so
// magpie keeps a UUID of its own, made once for this machine beside
// providers.json — never ZCode's.

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

var zcodeDevices = struct {
	sync.Mutex
	m map[string]string // by the file it is kept in
}{m: map[string]string{}}

var zcodeUUIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func zcodeDevicePath() string { return filepath.Join(filepath.Dir(Path()), "zcode-device-mid") }

// zcodeDeviceMid is this machine's X-Device-Mid: the one kept, or a new
// one, kept for next time (and used for now if it can't be).
func zcodeDeviceMid() string {
	p := zcodeDevicePath()
	zcodeDevices.Lock()
	defer zcodeDevices.Unlock()
	if id := zcodeDevices.m[p]; id != "" {
		return id
	}
	if b, err := os.ReadFile(p); err == nil {
		if id := strings.ToLower(strings.TrimSpace(string(b))); zcodeUUIDRe.MatchString(id) {
			zcodeDevices.m[p] = id
			return id
		}
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	id := h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
	if os.MkdirAll(filepath.Dir(p), 0o755) == nil {
		_ = os.WriteFile(p, []byte(id+"\n"), 0o600)
	}
	zcodeDevices.m[p] = id
	return id
}

// zcodeDeviceHeader names the machine on a request to zcode.z.ai, as
// ZCode does; Z.ai's and BigModel's business APIs are asked without it.
func zcodeDeviceHeader(req *http.Request) {
	if strings.HasPrefix(req.URL.String(), zcodeAPI+"/") {
		req.Header.Set("X-Device-Mid", zcodeDeviceMid())
	}
}
