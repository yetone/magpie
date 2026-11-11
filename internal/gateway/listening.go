package gateway

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/yetone/magpie/internal/appdir"
)

// ListeningFile is the file in magpie's config folder where the process
// serving the gateway writes where it listens and who it is, for a command
// run in another shell to find it (ljlwlcz on X: a systemd service had
// MAGPIE_ADDR=0.0.0.0:10100, and `magpie healthcheck` and `magpie update`
// run without it looked at 3425), and for a watchdog to read, which can't
// tell magpie.exe tray and magpie.exe mcp image apart by name.
const ListeningFile = "gateway.json"

// Listening is what that file holds.
type Listening struct {
	// Addr is the address the gateway listens on, as the listener has it
	// (0.0.0.0:10100)
	Addr string `json:"addr"`
	// Pid and Serves say which process: how it was started (app, tray, web,
	// serve or tui)
	Pid     int    `json:"pid"`
	Serves  string `json:"serves"`
	Version string `json:"version"`
	Started string `json:"started"`
}

// ListeningPath is where the file is.
func ListeningPath() string { return filepath.Join(appdir.Config(), ListeningFile) }

var processStart = time.Now()

// recordListening writes where this process listens, through a temp file
// renamed over the old one: a command reading it never sees half of it.
// Best effort; without the file a command looks at the address it knows.
func recordListening(addr net.Addr) {
	if addr == nil {
		return
	}
	b, err := json.Marshal(Listening{Addr: addr.String(), Pid: os.Getpid(), Serves: Serves, Version: Version, Started: processStart.UTC().Format(time.RFC3339)})
	if err != nil {
		return
	}
	p := ListeningPath()
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	tmp := p + "." + strconv.Itoa(os.Getpid()) + ".tmp"
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	if os.Rename(tmp, p) != nil {
		os.Remove(tmp)
	}
}

// forgetListening removes the file when it is this process's: one that took
// the gateway over (handover.go) has written its own by then.
func forgetListening() {
	if l, ok := ReadListening(); ok && l.Pid == os.Getpid() {
		os.Remove(ListeningPath())
	}
}

// ReadListening reads the file; ok is false when there is none or it
// doesn't parse, which says nothing about whether a gateway runs.
func ReadListening() (Listening, bool) {
	var l Listening
	b, err := os.ReadFile(ListeningPath())
	if err != nil || json.Unmarshal(b, &l) != nil || l.Addr == "" || l.Pid <= 0 {
		return Listening{}, false
	}
	if _, _, err := net.SplitHostPort(l.Addr); err != nil {
		return Listening{}, false
	}
	return l, true
}

// loopbackURL is the URL a program on this computer reaches addr at: a
// gateway on every interface is reached on loopback.
func loopbackURL(addr string) string {
	if h, p, err := net.SplitHostPort(addr); err == nil && (h == "" || net.ParseIP(h) != nil && net.ParseIP(h).IsUnspecified()) {
		addr = net.JoinHostPort("127.0.0.1", p)
	}
	return "http://" + addr
}

// ClientURL is where a command run on this computer reaches the running
// gateway. MAGPIE_ADDR, when the command has it, is the user's word.
// Without it, the address the running gateway wrote down (ListeningFile)
// is used when that process answers there as itself; else URL, the port in
// Settings. Only the gateway itself listens by URL; every client asks here.
func ClientURL() string {
	if os.Getenv("MAGPIE_ADDR") != "" {
		return URL()
	}
	if l, ok := ReadListening(); ok {
		u := loopbackURL(l.Addr)
		// the same place: nothing to ask (the statusline runs on every message)
		if u == URL() || ServedAt(u).Pid == l.Pid {
			return u
		}
	}
	return URL()
}
