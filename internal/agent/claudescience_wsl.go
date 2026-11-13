package agent

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/gateway"
)

// Claude Science in a WSL distro (shuitunlulu_ on Discord): its daemon
// runs in the distro, so magpie on Windows starts it there through
// wsl.exe, with ANTHROPIC_BASE_URL set, as it does this machine's
// (claudescience.go), and keeps it so from the keeper while the distro
// runs; it never starts a stopped distro to.
//
// The daemon takes http only to a loopback address, and sends its Claude
// sign-in as its key. In mirrored (or consomme) networking the distro's
// 127.0.0.1 is Windows', and it is given the gateway's own address. Under
// NAT it is given a relay magpie starts in the distro: socat on
// 127.0.0.1:<the gateway's port> there, to Windows' address on the WSL
// network, which the gateway listens on while on loopback; the WSL key,
// which it can't send as a key, goes first in the address's path, where
// the gateway takes it off (gateway.takeWSLPathKey).

// scienceWSLScript runs claude-science in the distro: $1 is the address
// the daemon is given ("": none), the rest its arguments. The program is
// where its installer puts it (~/.local/bin), else on the login PATH.
const scienceWSLScript = `g=$1; shift; b="$HOME/.local/bin/claude-science"; [ -x "$b" ] || b=$(command -v claude-science 2>/dev/null) || { echo "claude-science isn't installed"; exit 127; }; ` +
	`if [ -n "$g" ]; then ANTHROPIC_BASE_URL=$g; export ANTHROPIC_BASE_URL; else unset ANTHROPIC_BASE_URL; fi; exec "$b" "$@" </dev/null`

// scienceRelayScript starts the relay: $1 is the port it listens on at
// 127.0.0.1, $2 where it leads. One running to the same place is left
// running; one to another (Windows' address moved with a restart of WSL)
// is stopped first, with the connections it forked (its own process
// group, as setsid starts it). Its pid and where it leads are kept in
// ~/.claude-science/magpie-relay; a pid there is taken for it only while
// /proc says it is socat.
const scienceRelayScript = `port=$1; to=$2; d="$HOME/.claude-science"; pf="$d/magpie-relay"; ` +
	`if [ -r "$pf" ]; then read -r pid was < "$pf"; if [ -n "$pid" ] && [ "$(cat /proc/$pid/comm 2>/dev/null)" = socat ]; then [ "$was" = "$to" ] && exit 0; kill -TERM -"$pid" 2>/dev/null || kill "$pid" 2>/dev/null; sleep 0.3; fi; rm -f "$pf"; fi; ` +
	`[ -z "$to" ] && exit 0; ` +
	`command -v socat >/dev/null 2>&1 || { echo "socat isn't installed in the distro (sudo apt install socat)"; exit 3; }; ` +
	`mkdir -p "$d"; setsid socat TCP-LISTEN:$port,bind=127.0.0.1,reuseaddr,fork TCP:$to </dev/null >/dev/null 2>&1 & pid=$!; sleep 0.5; ` +
	`if [ "$(cat /proc/$pid/comm 2>/dev/null)" = socat ]; then echo "$pid $to" > "$pf"; exit 0; fi; ` +
	`echo "socat couldn't listen on 127.0.0.1:$port there (taken by another program?)"; exit 4`

// scienceWSLKeeps are the keepers' states of the distros' daemons, by
// distro.
var scienceWSLKeeps = struct {
	sync.Mutex
	keep map[string]*scienceKeeper
	mu   map[string]*sync.Mutex
}{keep: map[string]*scienceKeeper{}, mu: map[string]*sync.Mutex{}}

// scienceWSLMark is magpie's note of the daemon it started in distro
// name: one file a distro, as each keeps its own.
func scienceWSLMark(name string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return r
		}
		return '_'
	}, name)
	return filepath.Join(appdir.Config(), "claude-science-wsl-"+safe+".json")
}

// scienceWSLRun runs script in distro name with args; a var for tests.
var scienceWSLRun = func(name string, timeout time.Duration, script string, args ...string) ([]byte, error) {
	return wslRun(timeout, append([]string{"-d", name, "-e", "sh", "-lc", script, "sh"}, args...)...)
}

// scienceInWSL is the Claude Science of distro d.
func scienceInWSL(d distro) *scienceHost {
	scienceWSLKeeps.Lock()
	k := scienceWSLKeeps.keep[d.Name]
	if k == nil {
		k = &scienceKeeper{}
		scienceWSLKeeps.keep[d.Name] = k
	}
	mu := scienceWSLKeeps.mu[d.Name]
	if mu == nil {
		mu = &sync.Mutex{}
		scienceWSLKeeps.mu[d.Name] = mu
	}
	scienceWSLKeeps.Unlock()
	home := d.local(d.Home)
	relay := !d.Mirrored && d.Gateway != ""
	h := &scienceHost{
		id: ScienceID + "@wsl:" + d.Name,
		// found by the probe: the script finds it again where it runs
		bin: func() string {
			if d.Has["bin:claude-science"] {
				return "claude-science"
			}
			return ""
		},
		run: func(_, gw string, args ...string) ([]byte, error) {
			out, err := scienceWSLRun(d.Name, 90*time.Second, scienceWSLScript, append([]string{gw}, args...)...)
			if err != nil && len(out) > 0 {
				err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
			}
			return out, err
		},
		dir: func() string {
			p, err := d.open(filepath.Join(home, ".claude-science"))
			if err != nil {
				return filepath.Join(home, ".claude-science")
			}
			return p
		},
		stat: func(string) (os.FileInfo, error) {
			return os.Stat(filepath.Join(home, ".local", "bin", "claude-science"))
		},
		gw: func() string {
			if !relay {
				return d.base()
			}
			k := gateway.WSLKey()
			if k == "" {
				return ""
			}
			return "http://127.0.0.1:" + gateway.Port() + "/" + k
		},
		mark: func() string { return scienceWSLMark(d.Name) },
		keep: k,
		mu:   mu,
	}
	if relay {
		to := net.JoinHostPort(d.host(), gateway.Port())
		h.ready = func() error { return scienceRelay(d.Name, gateway.Port(), to) }
		h.unready = func() error { return scienceRelay(d.Name, gateway.Port(), "") }
	}
	return h
}

// scienceRelay starts the relay in distro name on port to to, or stops
// it for to "".
func scienceRelay(name, port, to string) error {
	out, err := scienceWSLRun(name, 30*time.Second, scienceRelayScript, port, to)
	if err != nil {
		if said := strings.TrimSpace(string(out)); said != "" {
			return fmt.Errorf("the relay to magpie's gateway in WSL %s: %s", name, said)
		}
		return fmt.Errorf("the relay to magpie's gateway in WSL %s: %w", name, err)
	}
	return nil
}

// scienceIn is Claude Science's row at a place: a WSL distro's
// (wslKinds); there is none elsewhere.
func scienceIn(p place) *Agent {
	if p.wsl == nil {
		return &Agent{ID: ScienceID, Name: scienceName, detect: func() bool { return false }}
	}
	h := scienceInWSL(*p.wsl)
	a := scienceAgent(h)
	notice := a.Notice
	d := *p.wsl
	// the relay leads to Windows' address on the WSL network, where the
	// gateway listens and Windows' firewall may keep WSL out: its row says
	// so as any agent pointed there would (reach.go), though its one value
	// isn't a model of magpie's, which is what Drift asks that of
	check := a.Check
	a.Check = func() string {
		if c := check(); c != "" || !h.wired() {
			return c
		}
		if u := a.unreachable(a.Fields[0], magpieID); u != nil {
			return u.Detail
		}
		return ""
	}
	a.Notice = func() string {
		if d.noRelay {
			return noticeWSLNoRelay.say("distro", d.Name, "net", d.Net, "agent", scienceName)
		}
		if notice != nil {
			return notice()
		}
		return ""
	}
	return a
}

// wslScienceHosts are the Claude Sciences of the running distros magpie
// connected: none, and no wsl.exe run, while it connected none.
func wslScienceHosts() []*scienceHost {
	marks, _ := filepath.Glob(filepath.Join(appdir.Config(), "claude-science-wsl-*.json"))
	if len(marks) == 0 {
		return nil
	}
	var out []*scienceHost
	for _, d := range wslReady(ScienceID) {
		h := scienceInWSL(d)
		if h.wired() {
			out = append(out, h)
		}
	}
	return out
}
