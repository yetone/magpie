package proc

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// Who holds a port, for a message that names it (pluoki on X: Tailscale's
// `tailscale serve` kept magpie's gateway port, and the page said only
// "another program"). Only read, never ended: Terminate and Kill are the
// take-over's, and only for a magpie.

// Holder is a process listening on a port.
type Holder struct {
	PID  int
	Name string // the program's name, without a folder or .exe
	Path string // where it runs from, "" when the system won't say
}

// PortHolder is the first process other than this one that this user can
// see listening on TCP port. false when none is seen, or the read failed:
// an unreadable holder is unknown, never "none".
func PortHolder(ctx context.Context, port int) (Holder, bool) {
	pids, err := ListeningOn(ctx, port)
	if err != nil {
		return Holder{}, false
	}
	for _, pid := range pids {
		if pid == os.Getpid() {
			continue
		}
		h := Holder{PID: pid}
		h.Path, _ = Executable(pid)
		if h.Path != "" {
			h.Name = programName(h.Path)
		} else {
			h.Name = programName(processName(pid))
		}
		return h, true
	}
	return Holder{}, false
}

// programName is the file name of a program's path, without .exe.
func programName(p string) string {
	if p == "" {
		return ""
	}
	n := filepath.Base(filepath.Clean(strings.ReplaceAll(p, `\`, "/")))
	n = strings.TrimSuffix(n, ".exe")
	if n == "." || n == "/" {
		return ""
	}
	return n
}

// IsTailscale says whether the holder is Tailscale's: tailscaled, the
// tailscale CLI, the Mac app (Tailscale.app, whose network extension is
// io.tailscale.ipn.macsys.network-extension) or the Windows service.
func (h Holder) IsTailscale() bool {
	s := strings.ToLower(strings.ReplaceAll(h.Path+"/"+h.Name, `\`, "/"))
	return strings.Contains(s, "tailscale")
}

// TailscaleServes says whether Tailscale's serve or funnel is set to answer
// on port, as `tailscale serve status --json` says it. The daemon runs as
// root, so its sockets are often out of this user's sight and PortHolder
// finds nobody; Tailscale itself still tells. Read only, within ctx: false
// for no Tailscale, no answer or output that doesn't parse.
func TailscaleServes(ctx context.Context, port int) bool {
	bin := tailscaleCLI()
	if bin == "" {
		return false
	}
	out, err := CommandContext(ctx, bin, "serve", "status", "--json").Output()
	if err != nil {
		return false
	}
	return slices.Contains(servePorts(out), port)
}

// tailscaleCLI is the tailscale command: on PATH, or where the Mac app and
// the Windows installer put it.
func tailscaleCLI() string {
	if p, err := Look("tailscale"); err == nil {
		return p
	}
	var cand []string
	switch runtime.GOOS {
	case "darwin":
		cand = []string{"/Applications/Tailscale.app/Contents/MacOS/Tailscale"}
	case "windows":
		for _, k := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
			if d := os.Getenv(k); d != "" {
				cand = append(cand, filepath.Join(d, "Tailscale", "tailscale.exe"))
			}
		}
	}
	for _, c := range cand {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// servePorts is the ports `tailscale serve status --json` has: Tailscale's
// ipn.ServeConfig, whose TCP map is keyed by port ("443") and whose Web
// and AllowFunnel maps are keyed by host:port ("host.tailnet.ts.net:8848").
// A warning line before the JSON (a client older than its daemon prints
// one) is skipped.
func servePorts(out []byte) []int {
	if i := strings.IndexByte(string(out), '{'); i > 0 {
		out = out[i:]
	}
	var cfg struct {
		TCP         map[string]json.RawMessage `json:"TCP"`
		Web         map[string]json.RawMessage `json:"Web"`
		AllowFunnel map[string]json.RawMessage `json:"AllowFunnel"`
	}
	if json.Unmarshal(out, &cfg) != nil {
		return nil
	}
	var ps []int
	add := func(s string) {
		if n, err := strconv.Atoi(s); err == nil && !slices.Contains(ps, n) {
			ps = append(ps, n)
		}
	}
	for k := range cfg.TCP {
		add(k)
	}
	for _, m := range []map[string]json.RawMessage{cfg.Web, cfg.AllowFunnel} {
		for k := range m {
			if _, p, err := net.SplitHostPort(k); err == nil {
				add(p)
			}
		}
	}
	return ps
}
