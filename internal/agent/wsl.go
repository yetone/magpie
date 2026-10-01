package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
)

// An agent installed in a WSL distro reads its config there, not in
// Windows' home — so does the Codex app's WSL connection. On Windows magpie
// lists the distros (wsl.exe -l -q), asks each running one once for its
// $HOME and which of wslKinds' agents are there (never starting one that is
// stopped), and edits the files through \\wsl.localhost\<distro>. Each is
// an agent of its own, <id>@wsl:<distro> (codex@wsl:Ubuntu,
// pi@wsl:Ubuntu, claude@wsl:Ubuntu). The gateway it is pointed at is
// 127.0.0.1 when WSL shares Windows' network (networkingMode=mirrored in
// .wslconfig); under NAT it is Windows as WSL sees it, which reaches the
// gateway only while that listens beyond loopback.

// place is where an agent lives: its home as magpie opens it, how a path
// there is spelt in the agent's own config, and the gateway as it reaches
// it. here(home) is this machine's.
type place struct {
	home  string
	id    string              // the agent's id when not its own: its stash keys go under it
	spell func(string) string // a path under home as the agent names it; nil: as is
	sys   func(string) string // a path of the agent's system (/etc/…) as magpie opens it; nil: as is
	base  func() string       // the gateway's URL from the agent; nil: gateway.URL
}

func here(home string) place { return place{home: home} }

func (p place) gw() string {
	if p.base != nil {
		return p.base()
	}
	return gateway.URL()
}

func (p place) v1() string       { return p.gw() + "/v1" }
func (p place) codexURL() string { return p.gw() + gateway.CodexPath }

// host is the gateway's host as the agent reaches it.
func (p place) host() string {
	if p.base == nil {
		return "127.0.0.1"
	}
	h, _, err := net.SplitHostPort(strings.TrimPrefix(p.base(), "http://"))
	if err != nil {
		return "127.0.0.1"
	}
	return h
}

// native is a path under home as the agent names it in its config.
func (p place) native(path string) string {
	if p.spell == nil {
		return path
	}
	return p.spell(path)
}

// key is a stash key of this machine's agent ("codex.model") as this
// place's agent's.
func (p place) key(k string) string {
	if p.id == "" {
		return k
	}
	_, rest, _ := strings.Cut(k, ".")
	return p.id + "." + rest
}

// distro is one WSL distro, as probed.
type distro struct {
	Name     string            `json:"name"`
	Home     string            `json:"home"`              // $HOME inside it, e.g. /home/me
	Root     string            `json:"root"`              // where magpie opens its / from, e.g. \\wsl.localhost\Ubuntu
	Has      map[string]bool   `json:"has"`               // "dir:.codex", "bin:pi": what the probe found
	Probe    int               `json:"probe,omitempty"`   // the wslProbeVersion that found it
	Gateway  string            `json:"gateway,omitempty"` // the Windows host as the distro reaches it, when not mirrored
	Values   map[string]string `json:"values,omitempty"`  // its agents' fields as last read (wslKind.memo), shown while it is stopped
	Mirrored bool              `json:"-"`
	Running  bool              `json:"-"`
}

// local is a path inside the distro as magpie opens it.
func (d distro) local(linux string) string {
	return d.Root + strings.ReplaceAll(linux, "/", string(filepath.Separator))
}

// native is a path magpie opens as the distro spells it.
func (d distro) native(local string) string {
	rel := local
	if len(local) >= len(d.Root) && strings.EqualFold(local[:len(d.Root)], d.Root) {
		rel = local[len(d.Root):]
	}
	rel = strings.ReplaceAll(rel, `\`, "/")
	if !strings.HasPrefix(rel, "/") {
		rel = "/" + rel
	}
	return rel
}

// base is the gateway's URL from inside the distro.
func (d distro) base() string {
	if d.Mirrored || d.Gateway == "" {
		return gateway.URL()
	}
	return "http://" + net.JoinHostPort(d.Gateway, gateway.Port())
}

func (d distro) place(id string) place {
	return place{home: d.local(d.Home), id: id, spell: d.native, sys: d.local, base: d.base}
}

// wslKind is an agent magpie looks for in a distro: what the probe finds
// of it, and how it lives at a place. Adding one here is all it takes.
type wslKind struct {
	id, name string
	dir, bin string             // under $HOME, and on its PATH: either says it is there
	in       func(place) *Agent // the agent at a place, as on this machine
	restart  string             // advice after a change, when mirrored; "" none
	// asleep, when set, is a stopped distro's options for a field, which
	// must read nothing of its files; nil keeps the live agent's
	asleep func(key string) func(map[string]string) []Option
}

var wslKinds = []wslKind{
	{id: "codex", name: "Codex", dir: ".codex", bin: "codex", in: codexIn,
		restart: "builds its model list at start-up — restart it (and the Codex app's WSL connection) to see this.",
		asleep: func(key string) func(map[string]string) []Option {
			switch key {
			case "model", "subagent":
				return func(map[string]string) []Option {
					return append(group("OpenAI", options(ownCodex(), "")), viaMagpieFor("codex", "")...)
				}
			case "effort":
				return func(cur map[string]string) []Option {
					if e := catalog.Efforts(append(catalog.Codex(), magpieModels("codex")...), cur["model"]); len(e) > 0 {
						return static(e...)
					}
					return static("low", "medium", "high", "xhigh")
				}
			}
			return nil
		}},
	{id: "pi", name: "Pi", dir: ".pi", bin: "pi", in: piIn,
		asleep: func(key string) func(map[string]string) []Option {
			if key == "model" {
				// the providers of its auth.json are the distro's files
				return func(cur map[string]string) []Option {
					return append(ownOptions("", cur["model"]), viaMagpie("pi", magpieID+"/")...)
				}
			}
			return nil
		}},
	// OmO, a fork of Pi, at its default folder: the distro's variables
	// that move it aren't read
	{id: "omo", name: "OmO", dir: ".omo", bin: "omo", in: omoIn,
		asleep: func(key string) func(map[string]string) []Option {
			if key == "model" {
				return func(cur map[string]string) []Option {
					return append(ownOptions("", cur["model"]), viaMagpie("omo", magpieID+"/")...)
				}
			}
			return nil
		}},
	// only its settings.json: its sign-in, sessions and prompt history,
	// read on this machine for Claude Code here, aren't read in a distro
	{id: "claude", name: "Claude Code", dir: ".claude", bin: "claude", in: claudeIn,
		asleep: func(key string) func(map[string]string) []Option {
			switch key {
			case "model":
				// whether it has a base URL of its own is the distro's file
				return func(map[string]string) []Option {
					return append(group("Claude Code", claudeOwn()), claudeViaMagpie()...)
				}
			case "effort":
				return nil
			}
			// a tier: magpie's models while the model last seen is one
			return func(cur map[string]string) []Option {
				if !isMagpie(cur["model"]) {
					return nil
				}
				return claudeViaMagpie()
			}
		}},
}

// found reports whether the probe found the agent in d.
func (k wslKind) found(d distro) bool { return d.Has["dir:"+k.dir] || d.Has["bin:"+k.bin] }

// memo is the key a field of the agent is kept under in distro.Values:
// Codex's as they are, as wsl.json has always had them.
func (k wslKind) memo(key string) string {
	if k.id == "codex" {
		return key
	}
	return k.id + "." + key
}

// wslFound reports whether any agent magpie knows is in d.
func wslFound(d distro) bool {
	for _, k := range wslKinds {
		if k.found(d) {
			return true
		}
	}
	return false
}

func wslKindOf(id string) wslKind {
	for _, k := range wslKinds {
		if k.id == id {
			return k
		}
	}
	panic("no WSL agent " + id)
}

// wslCodex is Codex in a distro.
func wslCodex(d distro) *Agent { return wslAgent(wslKindOf("codex"), d) }

// wslPi is Pi in a distro.
func wslPi(d distro) *Agent { return wslAgent(wslKindOf("pi"), d) }

// wslAgent is an agent in a distro: its own reading and writing, at the
// distro's home, with the distro's way to the gateway. A distro that isn't
// running is shown as magpie last saw it, and nothing of it is read: any
// access to its files starts it. Picking a value starts it, as asked.
func wslAgent(k wslKind, d distro) *Agent {
	id := k.id + "@wsl:" + d.Name
	a := k.in(d.place(id))
	a.ID, a.Name, a.Aliases, a.Bin, a.UA, a.WSL = id, k.name+" · WSL "+d.Name, nil, "", nil, d.Name
	if d.Running {
		a.Home = d.local(d.Home)
	}
	a.detect = func() bool { return k.found(d) }
	// its requests carry the agent's User-Agent and are counted as its
	// Windows twin's, so a prompt with none of "its" own seen isn't a bypass
	a.LastUsed = nil
	a.Notice = func() string {
		if !d.Mirrored {
			return "WSL " + d.Name + " isn't in mirrored networking, so its " + k.name + " can't reach magpie on 127.0.0.1 and was pointed at Windows (" + d.base() +
				"), which answers only while the gateway listens beyond loopback and Windows' firewall lets WSL in. " +
				"Set networkingMode=mirrored under [wsl2] in %UserProfile%\\.wslconfig and run wsl --shutdown, then pick the model again."
		}
		if k.restart == "" {
			return ""
		}
		return k.name + " in WSL " + d.Name + " " + k.restart
	}
	if !d.Running {
		return asleep(a, k, d)
	}
	if reached := a.Reached; reached != nil {
		a.Reached = func(since time.Time) (time.Time, string, bool) {
			at, to, refused := reached(since)
			if sameHost(to, d.base()) {
				to = gateway.URL() // the gateway, however WSL reaches it
			}
			return at, to, refused
		}
	}
	for i := range a.Fields {
		f := &a.Fields[i]
		get, key := f.Get, k.memo(f.Key)
		f.Get = func() string { v := get(); wslRemember(d.Name, key, v); return v }
	}
	// what a set leaves is kept at once, in case the distro stops before
	// the next look
	for i := range a.Fields {
		f := &a.Fields[i]
		if set := f.Set; set != nil {
			f.Set = func(v string) error {
				err := set(v)
				for _, g := range a.Fields {
					g.Get()
				}
				wslSave()
				return err
			}
		}
	}
	return a
}

// asleep is a stopped distro's agent: its fields read what magpie last
// saw, their options come from magpie alone, and it has no files to check,
// sync or migrate (Path and Dir are empty); setting a field goes to the
// files, which starts the distro.
func asleep(live *Agent, k wslKind, d distro) *Agent {
	started := false
	a := &Agent{ID: live.ID, Name: live.Name, Icon: live.Icon, WSL: d.Name, detect: live.detect,
		Notice: func() string {
			if started {
				return live.Notice()
			}
			return "WSL " + d.Name + " isn't running: magpie shows what it last saw there, and starts it only to change something."
		}}
	for _, lf := range live.Fields {
		key, set := lf.Key, lf.Set
		f := Field{Key: key, Label: lf.Label, Quiet: lf.Quiet, Options: lf.Options,
			Get: func() string { return wslLastSeen(d.Name, k.memo(key)) },
			Set: func(v string) error {
				// opening a stopped distro's files is aborted rather than
				// waiting for it to start, so it is started first
				if _, err := wslRun(time.Minute, "-d", d.Name, "-e", "true"); err != nil {
					return fmt.Errorf("start WSL %s: %w", d.Name, err)
				}
				started = true
				err := set(v)
				// a model settles the effort too
				for _, f := range live.Fields {
					wslRemember(d.Name, k.memo(f.Key), f.Get())
				}
				wslSave()
				return err
			}}
		if k.asleep != nil {
			if o := k.asleep(key); o != nil {
				f.Options = o
			}
		}
		a.Fields = append(a.Fields, f)
	}
	return a
}

// wslAgents are the agents in this machine's WSL distros; none off Windows.
func wslAgents() []*Agent {
	if !wslOn {
		return nil
	}
	return wslAgentsOf(wslDistros())
}

func wslAgentsOf(ds []distro) []*Agent {
	var out []*Agent
	for _, d := range ds {
		// only the agents it has: magpie writes nothing for one that isn't there
		for _, k := range wslKinds {
			if a := wslAgent(k, d); a.Detected() {
				out = append(out, a)
			}
		}
	}
	return out
}

// Only running distros are probed — asking one anything starts it — once
// each in a process, and again only after a probe that failed. Those an
// agent of wslKinds was found in are kept in wsl.json beside magpie's settings (not the
// stash, which profiles and backups carry), with their fields as last
// read, so a stopped one is listed without being started.
var wsl struct {
	sync.Mutex
	at      time.Time
	names   []string        // every distro installed
	running map[string]bool // those running
	listed  bool            // names is a real answer, and may forget distros
	seen    map[string]*distro
	probed  map[string]bool
	failed  map[string]time.Time
	dirty   bool
}

const (
	wslListAge  = time.Minute
	wslRetryAge = 10 * time.Minute
)

// wslOn is whether there is WSL to look in: on Windows, or in tests.
var wslOn = runtime.GOOS == "windows"

// wslRun runs wsl.exe; a var for tests.
var wslRun = func(timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := proc.CommandContext(ctx, "wsl.exe", args...)
	// wsl.exe speaks UTF-16 unless told otherwise; either is read
	cmd.Env = append(os.Environ(), "WSL_UTF8=1")
	return cmd.Output()
}

// wslRoot is where magpie opens a running distro's / from; a var for tests.
var wslRoot = func(name string) string {
	if _, err := os.Stat(`\\wsl.localhost\` + name + `\`); err == nil {
		return `\\wsl.localhost\` + name
	}
	return `\\wsl$\` + name // before Windows 11 / WSL 0.50
}

func wslStatePath() string { return filepath.Join(filepath.Dir(provider.Path()), "wsl.json") }

func wslDistros() []distro {
	wsl.Lock()
	defer wsl.Unlock()
	if wsl.seen == nil {
		wsl.seen, wsl.probed, wsl.failed = map[string]*distro{}, map[string]bool{}, map[string]time.Time{}
		if b, err := os.ReadFile(wslStatePath()); err == nil {
			json.Unmarshal(b, &wsl.seen)
		}
		wslForgetOldBins()
	}
	if time.Since(wsl.at) > wslListAge {
		wsl.names, wsl.listed = wslList("-l", "-q")
		run, _ := wslList("-l", "--running", "-q")
		wsl.running = map[string]bool{}
		for _, n := range run {
			wsl.running[n] = true
		}
		wsl.at = time.Now()
	}
	installed := map[string]bool{}
	mirrored := wslMirrored(wslConfig())
	var out []distro
	for _, n := range wsl.names {
		installed[n] = true
		if wsl.running[n] && !wsl.probed[n] {
			if t, ok := wsl.failed[n]; !ok || time.Since(t) > wslRetryAge {
				if d := wslProbe(n); d == nil {
					wsl.failed[n] = time.Now()
				} else {
					wsl.probed[n] = true
					if wslFound(*d) {
						if old := wsl.seen[n]; old != nil {
							d.Values = old.Values
						}
						wsl.seen[n] = d
					} else {
						delete(wsl.seen, n)
					}
					wsl.dirty = true
				}
			}
		}
		d := wsl.seen[n]
		if d == nil {
			continue
		}
		c := *d
		c.Running, c.Mirrored = wsl.running[n], mirrored
		out = append(out, c)
	}
	// an unregistered distro is forgotten
	for n := range wsl.seen {
		if wsl.listed && !installed[n] {
			delete(wsl.seen, n)
			wsl.dirty = true
		}
	}
	wslSaveLocked()
	return out
}

// wslForgetOldBins drops the commands a probe before wslProbeVersion
// found: it took a Windows command on the PATH WSL inherits (Windows npm's
// pi under /mnt/c, #406) for one in the distro. A distro then found by
// nothing else is forgotten, until a probe of it while it runs says again.
func wslForgetOldBins() {
	for n, d := range wsl.seen {
		if d == nil || d.Probe >= wslProbeVersion {
			continue
		}
		for k := range d.Has {
			if strings.HasPrefix(k, "bin:") {
				delete(d.Has, k)
				wsl.dirty = true
			}
		}
		if !wslFound(*d) {
			delete(wsl.seen, n)
			wsl.dirty = true
		}
	}
}

// wslSave writes wsl.json if anything in it changed.
func wslSave() {
	wsl.Lock()
	defer wsl.Unlock()
	wslSaveLocked()
}

func wslSaveLocked() {
	if wsl.dirty {
		if b, err := json.MarshalIndent(wsl.seen, "", "  "); err == nil && edit.WriteAtomic(wslStatePath(), b) == nil {
			wsl.dirty = false
		}
	}
}

// wslRemember keeps what a distro's field reads, for while it is stopped.
func wslRemember(name, key, v string) {
	wsl.Lock()
	defer wsl.Unlock()
	d := wsl.seen[name]
	if d == nil || d.Values[key] == v {
		return
	}
	if d.Values == nil {
		d.Values = map[string]string{}
	}
	d.Values[key] = v
	wsl.dirty = true
}

func wslLastSeen(name, key string) string {
	wsl.Lock()
	defer wsl.Unlock()
	if d := wsl.seen[name]; d != nil {
		return d.Values[key]
	}
	return ""
}

// wslClaudeStandIn is StandIn for a Claude Code in a WSL distro routed
// through magpie: the first, of the distros running at the last look, whose
// settings.json has a stand-in. Only magpie's memory of them is asked —
// nothing is listed nor probed, and a stopped distro is never opened.
func wslClaudeStandIn(model string) string {
	wsl.Lock()
	var ds []distro
	for _, n := range wsl.names {
		if d := wsl.seen[n]; d != nil && wsl.running[n] && wslKindOf("claude").found(*d) {
			ds = append(ds, *d)
		}
	}
	wsl.Unlock()
	mirrored := wslMirrored(wslConfig())
	for _, d := range ds {
		d.Mirrored = mirrored
		path := filepath.Join(d.local(d.Home), ".claude", "settings.json")
		if m := claudeStandInAt(path, model, d.base()); m != "" {
			return m
		}
	}
	return ""
}

// wslList is the names wsl.exe lists with args; ok is false when it
// couldn't say (no WSL, or none installed).
func wslList(args ...string) (names []string, ok bool) {
	b, err := wslRun(10*time.Second, args...)
	if err != nil {
		return nil, false
	}
	return parseDistros(b), true
}

// wslProbeVersion is that of wslProbeScript: 2 says where each command is,
// so one from Windows' drives isn't taken for the distro's own.
const wslProbeVersion = 2

// wslProbeScript prints the distro's home, what of each of wslKinds it
// has (each command with where it is), where Windows' drives are mounted,
// and its default route (the Windows host under NAT).
var wslProbeScript = func() string {
	s := `echo "home:$HOME"; `
	for _, k := range wslKinds {
		s += `[ -d "$HOME/` + k.dir + `" ] && echo dir:` + k.dir + `; ` +
			`p=$(command -v ` + k.bin + ` 2>/dev/null) && echo "bin:` + k.bin + ` $p"; `
	}
	// a drive's source in /proc/mounts is C:\ (written C:\134), under any automount root
	s += `awk '$1 ~ /^[A-Za-z]:/ {print "win:" $2}' /proc/mounts 2>/dev/null; `
	return s + `ip route show default 2>/dev/null | head -n1 | sed 's/^/route:/'; ` +
		`grep -m1 '^nameserver' /etc/resolv.conf 2>/dev/null | sed 's/^/ns:/'; true`
}()

func wslProbe(name string) *distro {
	b, err := wslRun(30*time.Second, "-d", name, "-e", "sh", "-lc", wslProbeScript)
	if err != nil {
		return nil
	}
	d := parseProbe(name, string(b))
	if d == nil {
		return nil
	}
	if wslFound(*d) {
		d.Root = wslRoot(name)
	}
	return d
}

// parseProbe reads wslProbeScript's output. A command on one of Windows'
// drives — WSL puts Windows' PATH on its own, so Windows npm's pi is
// /mnt/c/Users/me/AppData/Roaming/npm/pi there — is Windows', not the
// distro's.
func parseProbe(name, out string) *distro {
	d := &distro{Name: name, Has: map[string]bool{}, Probe: wslProbeVersion}
	var ns string
	bins := map[string]string{}
	var win []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		switch k, v, _ := strings.Cut(l, ":"); k {
		case "home":
			d.Home = strings.TrimRight(v, "/")
		case "dir":
			d.Has[l] = true
		case "bin":
			bin, path, _ := strings.Cut(v, " ")
			bins[bin] = strings.TrimSpace(path)
		case "win":
			if v = strings.TrimRight(v, "/"); strings.HasPrefix(v, "/") {
				win = append(win, v+"/")
			}
		case "route":
			// default via 172.20.0.1 dev eth0 …
			if f := strings.Fields(v); len(f) >= 3 && f[1] == "via" && net.ParseIP(f[2]) != nil {
				d.Gateway = f[2]
			}
		case "ns":
			if f := strings.Fields(v); len(f) >= 2 && net.ParseIP(f[1]) != nil {
				ns = f[1]
			}
		}
	}
	if !strings.HasPrefix(d.Home, "/") {
		return nil
	}
	for bin, path := range bins {
		if !onWindowsDrive(path, win) {
			d.Has["bin:"+bin] = true
		}
	}
	if d.Gateway == "" {
		d.Gateway = ns
	}
	return d
}

// onWindowsDrive is whether path, inside a distro, is on one of Windows'
// drives: under a mount win lists, or under /mnt/<letter>/ as WSL mounts
// them unless told otherwise.
func onWindowsDrive(path string, win []string) bool {
	for _, w := range win {
		if strings.HasPrefix(path, w) {
			return true
		}
	}
	rest, ok := strings.CutPrefix(path, "/mnt/")
	return ok && len(rest) >= 2 && rest[1] == '/' &&
		(rest[0] >= 'a' && rest[0] <= 'z' || rest[0] >= 'A' && rest[0] <= 'Z')
}

// parseDistros reads wsl.exe -l -q: UTF-16LE (with or without a BOM), or
// UTF-8 under WSL_UTF8. Docker Desktop's own distros are left out.
func parseDistros(b []byte) []string {
	s := decodeWSL(b)
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(strings.Trim(l, "\x00\ufeff\r"))
		if l == "" || strings.HasPrefix(strings.ToLower(l), "docker-desktop") {
			continue
		}
		out = append(out, l)
	}
	return out
}

func decodeWSL(b []byte) string {
	utf16le := len(b) >= 2 && (b[0] == 0xff && b[1] == 0xfe || b[1] == 0 && b[0] != 0)
	if !utf16le {
		return string(b)
	}
	if b[0] == 0xff && b[1] == 0xfe {
		b = b[2:]
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}

// wslConfig is %UserProfile%\.wslconfig, "" if there is none.
func wslConfig() string {
	home, _ := os.UserHomeDir()
	b, _ := os.ReadFile(filepath.Join(home, ".wslconfig"))
	return string(b)
}

// wslMirrored reports whether a .wslconfig puts WSL 2 in mirrored
// networking: networkingMode=mirrored under [wsl2].
func wslMirrored(cfg string) bool {
	section, mirrored := "", false
	sc := bufio.NewScanner(strings.NewReader(strings.TrimPrefix(cfg, "\ufeff")))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || l[0] == '#' || l[0] == ';' {
			continue
		}
		if strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]") {
			section = strings.ToLower(strings.TrimSpace(l[1 : len(l)-1]))
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok || section != "wsl2" || !strings.EqualFold(strings.TrimSpace(k), "networkingMode") {
			continue
		}
		if i := strings.IndexAny(v, "#;"); i >= 0 {
			v = v[:i]
		}
		mirrored = strings.EqualFold(strings.Trim(strings.TrimSpace(v), `"`), "mirrored")
	}
	return mirrored
}
