package agent

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/wslrun"
)

// An agent installed in a WSL distro reads its config there, not in
// Windows' home — so does the Codex app's WSL connection. On Windows magpie
// lists the distros (wsl.exe -l -q), asks each running one once for its
// $HOME and which of wslKinds' agents are there (never starting one that is
// stopped), and edits the files through \\wsl.localhost\<distro>. Each is
// an agent of its own, <id>@wsl:<distro> (codex@wsl:Ubuntu,
// pi@wsl:Ubuntu, claude@wsl:Ubuntu). The gateway it is pointed at is
// 127.0.0.1 when the distro's 127.0.0.1 reaches Windows' (mirrored or
// consomme networking, as the distro's wslinfo says, wslrun.HostLoopback;
// or mirrored in .wslconfig where there is no wslinfo); under NAT it is
// Windows as WSL sees it, which reaches the gateway only while that
// listens beyond loopback.

// place is where an agent lives: its home as magpie opens it, how a path
// there is spelt in the agent's own config, and the gateway as it reaches
// it. here(home) is this machine's.
type place struct {
	home  string
	id    string              // the agent's id when not its own: its stash keys go under it
	spell func(string) string // a path under home as the agent names it; nil: as is
	sys   func(string) string // a path of the agent's system (/etc/…) as magpie opens it; nil: as is
	base  func() string       // the gateway's URL from the agent; nil: gateway.URL
	// cold: a stopped distro's, whose files can't be looked at without
	// starting it — an agent built there takes its files at their defaults
	cold bool
	// version is the agent's CLI's there, as the probe found it; "" when
	// not known (one on this machine is asked itself)
	version string
	// reach is a path under home as magpie can open it (distro.open): one
	// below a link the distro has is opened where the link leads; nil: as
	// is. unreached keeps the first path it couldn't tell the way to.
	reach     func(string) (string, error)
	unreached *error
	// chmod takes the group's and others' bits off a file under home, where
	// magpie's own mode doesn't hold (a distro's: see ownerOnly); nil: here
	chmod func(string) error
	// dir is the agent's config folder when magpie keeps one of its own
	// for it (claude-magpie's launch file, claude_launch.go); "": its own
	dir string
}

func here(home string) place { return place{home: home} }

// open is path, a path under the place's home, where magpie can open it:
// in a WSL distro, a folder that is a link there (~/.codex ->
// /mnt/c/Users/me/.codex, #4502) is opened where it leads, as nothing
// below it can be opened through \\wsl.localhost. A link the distro
// couldn't resolve leaves path as it is, and the place unreached.
func (p place) open(path string) string {
	if p.reach == nil {
		return path
	}
	o, err := p.reach(path)
	if err != nil {
		if p.unreached != nil && *p.unreached == nil {
			*p.unreached = err
		}
		return path
	}
	return o
}

// join is filepath.Join(home, elem...), opened where magpie can (open).
func (p place) join(elem ...string) string {
	return p.open(filepath.Join(append([]string{p.home}, elem...)...))
}

// getenv is this machine's variable for this machine's agent; "" for one
// in a distro, whose variables magpie can't read.
func (p place) getenv(k string) string {
	if p.spell != nil {
		return ""
	}
	return appdir.Getenv(k)
}

// codexHome is Codex's folder at the place: $CODEX_HOME on this machine
// (appdir.CodexHome), ~/.codex in a distro, whose variables magpie can't
// read.
func (p place) codexHome() string {
	if p.spell != nil {
		return p.join(".codex")
	}
	return appdir.CodexHomeIn(p.home)
}

// claudeDir is Claude Code's folder at the place: $CLAUDE_CONFIG_DIR on
// this machine (appdir.ClaudeDir), ~/.claude in a distro, whose variables
// magpie can't read.
func (p place) claudeDir() string {
	if p.dir != "" {
		return p.dir
	}
	if p.spell != nil {
		return p.join(".claude")
	}
	return appdir.ClaudeDirIn(p.home)
}

// hermesHome is Hermes Agent's folder at the place: $HERMES_HOME, else
// %LOCALAPPDATA%\hermes on Windows and ~/.hermes elsewhere, on this machine
// (appdir.HermesHome); ~/.hermes in a distro, which is Linux and whose
// variables magpie can't read.
func (p place) hermesHome() string {
	if p.spell != nil {
		return p.join(".hermes")
	}
	return appdir.HermesHomeIn(p.home)
}

// hermesDefault is Hermes's default folder at the place, whatever
// HERMES_HOME says: the root of its profiles.
func (p place) hermesDefault() string {
	if p.spell != nil {
		return p.join(".hermes")
	}
	return appdir.HermesDefaultIn(p.home)
}

// exists is whether there is a file or folder at path, as an agent looks
// for the one it reads; false at a cold place.
func (p place) exists(path string) bool {
	if p.cold {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// isDir is isDir at the place; false at a cold one.
func (p place) isDir(path string) bool { return !p.cold && isDir(path) }

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
	Name    string          `json:"name"`
	Home    string          `json:"home"`              // $HOME inside it, e.g. /home/me
	Root    string          `json:"root"`              // where magpie opens its / from, e.g. \\wsl.localhost\Ubuntu
	Has     map[string]bool `json:"has"`               // "dir:.codex", "bin:pi": what the probe found
	Probe   int             `json:"probe,omitempty"`   // the wslProbeVersion that found it
	Gateway string          `json:"gateway,omitempty"` // the Windows host as the distro reaches it, when not at 127.0.0.1
	// Was are the addresses it reached Windows at before, newest last: an
	// agent's config still on one is offered the one now (#1013)
	Was    []string          `json:"was,omitempty"`
	Values map[string]string `json:"values,omitempty"` // its agents' fields as last read (wslKind.memo), shown while it is stopped
	// Net is WSL's networking mode as the distro's wslinfo said at the
	// last probe ("mirrored", "consomme", "nat", "bridged", "none", and
	// "virtioproxy" before WSL 2.9); "" when it couldn't say (a WSL without
	// wslinfo), and .wslconfig is read instead
	Net string `json:"net,omitempty"`
	// Versions is what the CLIs of wslKinds that ask (version) said their
	// versions are at the last probe, by the kind's id
	Versions map[string]string `json:"versions,omitempty"`
	// OmpProfiles are the named omp profiles the probe found under
	// ~/.omp/profiles there: each is an agent of its own (omp#<name>).
	// Kept so a stopped distro lists them without being started.
	OmpProfiles []string `json:"ompProfiles,omitempty"`
	// Links are the agents' folders (and files) under $HOME that lead
	// elsewhere, by their path under it (".codex"): where readlink -f
	// there says they lead, "" where it couldn't say (#4502)
	Links map[string]string `json:"links,omitempty"`
	// Drives are where Windows' drives are mounted in it, by mount point:
	// "/mnt/c" is "C:"
	Drives map[string]string `json:"drives,omitempty"`
	// Mirrored is whether the distro's 127.0.0.1 is Windows' (mirror): in
	// mirrored networking, or in consomme
	Mirrored bool `json:"-"`
	// noRelay: consomme with localhostForwarding=false in .wslconfig, where
	// the distro's 127.0.0.1 is its own after all
	noRelay bool
	Running bool `json:"-"`
	// up asks whether it runs now (wslUp), for one wslDistros listed
	up func() bool
}

// live is whether the distro runs now: asked of wsl.exe again, at most
// wslRunningAge after the last answer, for one wslDistros listed (an
// agent of it may be read after the user stopped it); Running otherwise.
func (d distro) live() bool {
	if d.up == nil {
		return d.Running
	}
	return d.up()
}

// local is a path inside the distro as magpie opens it.
func (d distro) local(linux string) string {
	return d.Root + strings.ReplaceAll(linux, "/", string(filepath.Separator))
}

// native is a path magpie opens as the distro spells it: one open moved
// to where a link leads is spelt through the link, as the agent knows it.
func (d distro) native(local string) string {
	best, spelt := "", ""
	for rel, to := range d.Links {
		if to == "" {
			continue
		}
		at := d.where(to)
		if r, ok := below(local, at); ok && len(at) > len(best) {
			best, spelt = at, strings.TrimRight(d.Home, "/")+"/"+rel+strings.ReplaceAll(r, string(filepath.Separator), "/")
		}
	}
	if best != "" {
		return spelt
	}
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

// open is local, a path in the distro as magpie opens it, where magpie can
// open it. Through \\wsl.localhost Windows can't follow a link the
// distro has: a path below ~/.codex, when that leads to
// /mnt/c/Users/me/.codex, can be neither made, written, read nor listed,
// and a mkdir of ~/.codex itself says it exists (linusxiong's #4502). So a
// path at or below one of Links is opened where the link leads: on one of
// Windows' drives, there itself (C:\Users\me\.codex); elsewhere in the
// distro, through \\wsl.localhost again. A link the distro couldn't
// resolve is an error: where it leads isn't guessed.
func (d distro) open(local string) (string, error) {
	h := d.local(d.Home)
	best, rest := "", ""
	for rel := range d.Links {
		at := filepath.Join(h, filepath.FromSlash(rel))
		r, ok := below(local, at)
		if ok && len(rel) > len(best) {
			best, rest = rel, r
		}
	}
	if best == "" {
		return local, nil
	}
	to := d.Links[best]
	if to == "" {
		return "", fmt.Errorf("~/%s in WSL %s is a link magpie couldn't follow (readlink -f there failed), so nothing is written through it; check where it points from a shell there", best, d.Name)
	}
	return d.where(to) + rest, nil
}

// where is linux, a path in the distro, as magpie opens it: on one of
// Windows' drives, the drive's own path; anywhere else, through the
// distro's root.
func (d distro) where(linux string) string {
	drives := d.Drives
	if len(drives) == 0 {
		// a probe before Drives: WSL's default mounts, /mnt/<letter>
		if rest, ok := strings.CutPrefix(linux, "/mnt/"); ok && len(rest) >= 1 && (len(rest) == 1 || rest[1] == '/') && isLetter(rest[0]) {
			drives = map[string]string{"/mnt/" + rest[:1]: strings.ToUpper(rest[:1]) + ":"}
		}
	}
	best := ""
	for m := range drives {
		if (linux == m || strings.HasPrefix(linux, m+"/")) && len(m) > len(best) {
			best = m
		}
	}
	if best == "" {
		return d.local(linux)
	}
	return wslDrive(drives[best]) + strings.ReplaceAll(strings.TrimPrefix(linux[len(best):], "/"), "/", string(filepath.Separator))
}

// wslDrive is the root of a Windows drive ("C:") as magpie opens it; a var
// for tests.
var wslDrive = func(drive string) string { return drive + `\` }

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// below is what path has below dir (with its leading separator, "" for
// dir itself), and whether it is dir or under it; Windows' paths compared
// without case.
func below(path, dir string) (string, bool) {
	if len(path) < len(dir) || !strings.EqualFold(path[:len(dir)], dir) {
		return "", false
	}
	rest := path[len(dir):]
	if rest != "" && rest[0] != filepath.Separator {
		return "", false
	}
	return rest, true
}

// mirror settles Mirrored from what wslinfo said at the probe, or, when it
// couldn't, from cfg (.wslconfig) saying mirrored: a WSL without wslinfo
// predates consomme, so .wslconfig's consomme isn't taken for one. Under
// consomme, cfg's localhostForwarding=false (wsl2.localhostForwarding)
// turns the relay of 127.0.0.1 off; it stays the address, with a notice,
// as Windows' next hop (the default route) is never Windows there.
func (d *distro) mirror(cfg string) {
	d.Mirrored = wslrun.HostLoopback(d.Net) || d.Net == "" && wslMirrored(cfg)
	d.noRelay = d.Net != "mirrored" && wslrun.HostLoopback(d.Net) && wslOff(cfg, "localhostForwarding")
}

// notMirrored is the notice of an agent in a distro not known to be in
// mirrored networking: what WSL said, or that only .wslconfig was read.
func (d distro) notMirrored(agent string) string {
	n := noticeWSLNotMirrored
	if d.Net == "" {
		n = noticeWSLNoMode
	}
	return n.say("distro", d.Name, "net", d.Net, "agent", agent, "url", d.base())
}

// base is the gateway's URL from inside the distro.
func (d distro) base() string {
	if d.Mirrored || d.Gateway == "" {
		return gateway.URL()
	}
	return "http://" + net.JoinHostPort(d.Gateway, gateway.Port())
}

func (d distro) place(id string) place {
	kind, _, _ := strings.Cut(id, "@")
	// a profile's row is omp#<name>@wsl:<distro>, whose kind is
	// omp#<name>: the probe keeps the binary version under omp, so the
	// lookup cuts the profile off
	if k, _, ok := strings.Cut(kind, "#"); ok {
		kind = k
	}
	return place{home: d.local(d.Home), id: id, spell: d.native, sys: d.local, base: d.base, cold: !d.Running, version: d.Versions[kind],
		reach: d.open, unreached: new(error), chmod: d.chmod}
}

// chmod takes the group's and others' bits off local, a file in the distro
// magpie opens, from inside it: a file written through \\wsl.localhost is
// 0644 there whatever mode magpie asks for, and a key store so is refused.
func (d distro) chmod(local string) error {
	if _, err := os.Stat(local); err != nil {
		return nil
	}
	// a failure is said in the log, not as the pick's: the route and the key
	// are written, and the next pick or sync tries again
	if out, err := wslRun(30*time.Second, "-d", d.Name, "-e", "chmod", "go-rwx", d.native(local)); err != nil {
		log.Printf("chmod go-rwx %s in %s: %v %s", d.native(local), d.Name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// wslKind is an agent magpie looks for in a distro: what the probe finds
// of it, and how it lives at a place. Adding one here is all it takes.
type wslKind struct {
	id, name string
	// dir, under $HOME, and bin, on its PATH: either says it is there; ""
	// for one that says nothing (a folder other tools keep too)
	dir, bin string
	in       func(place) *Agent // the agent at a place, as on this machine
	restart  notice             // advice after a change, when mirrored; "" none
	// restartAt, when set, is restart's choice at a place (kimi-cli or Kimi
	// Code 2.x, told by the folder there)
	restartAt func(place) notice
	// version: the probe asks the distro's own bin its version (--version),
	// for an agent whose config depends on it
	version bool
	// accept, when set, is whether what the probe found is the agent: a
	// command another tool's release shares the name of, told by its version
	accept func(d distro) bool
	// asleep, when set, is a stopped distro's options for a field, which
	// must read nothing of its files; nil keeps the live agent's
	asleep func(key string) func(map[string]string) []Option
}

var wslKinds = []wslKind{
	{id: "codex", name: "Codex", dir: ".codex", bin: "codex", in: codexIn,
		restart: newNotice("{agent} in WSL {distro} builds its model list at start-up — restart it, the app-server its sessions share ({command} there) and the Codex app's WSL connection to see this."),
		asleep: func(key string) func(map[string]string) []Option {
			switch key {
			case "model", "subagent", "memories":
				return func(map[string]string) []Option {
					// over several accounts magpie routes, so they are
					// offered via magpie too, as on a live Codex (#1603)
					if codexFailover() {
						return withVia(group("OpenAI", codexNamed(ownCodex())), "codex", "")
					}
					own := codexInMagpie(group("OpenAI", codexNamed(ownCodex())))
					return append(own, codexViaMagpieFor(own)...)
				}
			case "effort":
				return func(cur map[string]string) []Option {
					return codexEfforts(append(catalog.Codex(), magpieModels("codex")...), cur["model"])
				}
			case "subagent_effort":
				return func(cur map[string]string) []Option {
					m := cur["subagent"]
					if m == "" {
						m = cur["model"]
					}
					if e := catalog.Efforts(append(catalog.Codex(), magpieModels("codex")...), m); len(e) > 0 {
						return static(e...)
					}
					return static("low", "medium", "high", "xhigh")
				}
			}
			return nil
		}},
	// its version says which file its MCP servers go in (Pi 0.99 reads
	// mcp.json itself, library's piMCP)
	{id: "pi", name: "Pi", dir: ".pi", bin: "pi", in: piIn, version: true,
		asleep: func(key string) func(map[string]string) []Option {
			if key == "model" {
				// the providers of its auth.json are the distro's files
				return func(cur map[string]string) []Option {
					return withVia(ownOptions("", cur["model"]), "pi", magpieID+"/")
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
					return withVia(ownOptions("", cur["model"]), "omo", magpieID+"/")
				}
			}
			return nil
		}},
	// Prime Agent, Pi's files in a folder of its own, at its default
	// folder: the distro's variable that moves it isn't read
	{id: "prime-agent", name: "Prime Agent", dir: ".prime/agent", bin: "prime-agent", in: primeAgentIn,
		asleep: func(key string) func(map[string]string) []Option {
			if key == "model" {
				return func(cur map[string]string) []Option {
					return withVia(ownOptions("", cur["model"]), "prime-agent", magpieID+"/")
				}
			}
			return nil
		}},
	{id: "stepcode", name: "StepCode", dir: ".stepcode", in: stepcodeIn,
		asleep: wslOwnAsleep("stepcode", "model")},
	// only its settings.json: its sign-in, sessions and prompt history,
	// read on this machine for Claude Code here, aren't read in a distro
	{id: "claude", name: "Claude Code", dir: ".claude", bin: "claude", in: claudeIn,
		asleep: func(key string) func(map[string]string) []Option {
			switch key {
			case "model":
				// whether it has a base URL of its own is the distro's file
				return func(cur map[string]string) []Option {
					return joinedVia(group("Claude Code", claudeOwn(cur["model"], nil, catalog.Provider("anthropic"))), claudeViaShared(false), [2]string{"\x00claude-via", "\x00asleep"})
				}
			case "effort", "ultracode", "statusline":
				// their own options read only what was last seen; magpie's
				// status line isn't offered in a distro
				return nil
			case "login":
				// its sign-in, while the model last seen is magpie's
				return func(cur map[string]string) []Option {
					if !isMagpie(cur["model"]) {
						return nil
					}
					return claudeSignIns()
				}
			}
			// a tier's effort or the subagents': the levels of the model it
			// was last seen on, as when running, not a model to pick
			if tier, ok := strings.CutSuffix(key, "_effort"); ok {
				return func(cur map[string]string) []Option {
					if !isMagpie(cur["model"]) {
						return nil
					}
					return static(tierEfforts(cmp.Or(cur[tier], cur["model"]))...)
				}
			}
			// a tier, the subagents or auto mode: magpie's models while the
			// model last seen is one
			return func(cur map[string]string) []Option {
				if !isMagpie(cur["model"]) {
					return nil
				}
				return claudeViaMagpie(false)
			}
		}},
	// The CLIs below keep a plain file in the distro's home, written as on
	// this machine, at their default folders: the distro's variables that
	// move them (OPENCODE_CONFIG_DIR, KIMI_CODE_HOME, HERMES_HOME, …)
	// aren't read. Each one's restart advice is its own Notice's.
	{id: "opencode", name: "OpenCode", dir: ".config/opencode", bin: "opencode", in: opencodeIn,
		restart: newNotice("{agent} in WSL {distro} reads its config at start-up — restart open opencode sessions to use this."),
		asleep:  wslOwnAsleep("opencode", "model", "small")},
	{id: "mimocode", name: "MiMo Code", dir: ".config/mimocode", bin: "mimo", in: mimocodeIn,
		restart: newNotice("{agent} in WSL {distro} reads its config at start-up — restart open mimo sessions to use this."),
		asleep:  wslOwnAsleep("mimocode", "model", "small")},
	// Kimi Code's ~/.kimi-code, or the old kimi-cli's ~/.kimi where that is
	// all there is; a stopped distro's is looked at once it is started
	{id: "kimi", name: "Kimi Code", dir: ".kimi-code", bin: "kimi", in: kimiIn,
		restart: noticeWSLKimiCode, restartAt: kimiWSLNotice,
		asleep: func(key string) func(map[string]string) []Option {
			if key != "model" {
				return nil
			}
			return func(cur map[string]string) []Option {
				return withVia(kimiOwnOptions("", cur["model"]), "kimi", magpieID+"/")
			}
		}},
	// Qwen Code's ~/.qwen, one settings.json of the same shape as this
	// machine's; a stopped distro's is looked at once it is started
	{id: "qwen", name: "Qwen Code", dir: ".qwen", bin: "qwen", in: qwenIn,
		restart: newNotice("{agent} in WSL {distro} reads a session's model at start-up — start a new session, or /model anew, to use this."),
		asleep: func(key string) func(map[string]string) []Option {
			if key != "model" {
				return nil
			}
			return func(cur map[string]string) []Option {
				return withVia(qwenOwnOptions("", cur["model"]), "qwen", magpieID+"/")
			}
		}},
	{id: "omp", name: "omp", dir: ".omp", bin: "omp", in: ompIn, version: true,
		restart: newNotice("{agent} in WSL {distro} reads its settings at start-up — restart open omp sessions to use this."),
		asleep: func(key string) func(map[string]string) []Option {
			if key == "effort" {
				return nil
			}
			return func(cur map[string]string) []Option {
				return withVia(ompOwnOptions("", cur[key]), "omp", magpieID+"/")
			}
		}},
	{id: "crush", name: "Crush", dir: ".config/crush", bin: "crush", in: crushIn,
		restart: newNotice("{agent} in WSL {distro} reads its settings at start-up — restart open crush sessions to use this."),
		asleep:  wslOwnAsleep("crush", "model", "small")},
	{id: "hermes", name: "Hermes Agent", dir: ".hermes", bin: "hermes", in: hermesIn,
		restart: newNotice("{agent} in WSL {distro} reads its settings at start-up — restart open Hermes sessions to use this.")},
	// no bin: morph is other tools' name too
	{id: "morph", name: "Mister Morph", dir: ".morph", in: morphIn,
		restart: newNotice("{agent} in WSL {distro} uses this for new tasks in its Console — restart open morph chats to use it there.")},
	// no bin: grok is also other tools' name, as on this machine
	{id: "grok", name: "Grok Build", dir: ".grok", in: grokIn,
		restart: newNotice("{agent} in WSL {distro} reads its settings at start-up — restart open grok sessions to use this."),
		asleep: func(key string) func(map[string]string) []Option {
			if key != "model" {
				return nil
			}
			// its models_cache.json and config.toml are the distro's files
			return func(cur map[string]string) []Option {
				opts := viaMagpie("grok", magpieID+"/")
				for i := range opts {
					opts[i].Same = opts[i].own
				}
				return append(grokOwnOptions("", "", cur["model"]), opts...)
			}
		}},
	{id: "droid", name: "Droid", dir: ".factory", bin: "droid", in: droidIn,
		restart: newNotice("{agent} in WSL {distro} reads its settings at start-up — restart open droid sessions to use this."),
		asleep: func(key string) func(map[string]string) []Option {
			if key != "model" {
				return nil
			}
			return func(cur map[string]string) []Option {
				var own []Option
				if c := cur["model"]; c != "" && !usesMagpie(c) {
					own = append(own, Option{Value: c, Icon: modelIcon("", c)})
				}
				return withVia(group("Droid", own), "droid", magpieID+"/")
			}
		}},
	// no bin: fx is also the JSON viewer's name
	{id: "fx", name: "fx", dir: ".fx", in: fxIn,
		restart: newNotice("{agent} in WSL {distro} reads its settings at start-up — restart open fx sessions to use this.")},
	{id: "commandcode", name: "Command Code", dir: ".commandcode", bin: "command-code", in: commandCodeIn,
		restart: newNotice("{agent} in WSL {distro} wants its own sign-in there (cmd login) even for models through magpie, and reads its settings at start-up — restart open Command Code sessions to use this.")},
	{id: "minimax-code", name: "MiniMax Code", dir: ".minimax", bin: "mcode", in: miniMaxIn,
		restart: newNotice("{agent} in WSL {distro} reads its settings at start-up — restart open mcode sessions to use this."),
		asleep: func(key string) func(map[string]string) []Option {
			if key != "model" {
				return nil
			}
			return func(cur map[string]string) []Option {
				return withVia(miniMaxOwnOptions("", cur["model"]), "minimax-code", magpieID+"/")
			}
		}},
	// version: a dsh with no profile yet is told from one before profiles
	// by it (liby233)
	{id: "dsh", name: "DeepSeek Harness", dir: ".dsh", bin: "dsh", in: dshIn, version: true,
		restart: newNotice("{agent} in WSL {distro} reads its config at start-up — restart open dsh sessions to use this."),
		asleep: func(key string) func(map[string]string) []Option {
			// the thinking levels hang on which patch lists there are, the
			// distro's files: a dsh of today's (0.1.5 on) is taken
			if key == "model" {
				return func(map[string]string) []Option {
					own := append([]Option{}, dshModels...)
					for i := range own {
						own[i].Direct = "DeepSeek"
					}
					return withVia(own, "dsh", magpieID+"/")
				}
			}
			if key != "effort" {
				return nil
			}
			return func(cur map[string]string) []Option {
				if ref, ok := strings.CutPrefix(cur["model"], magpieID+"/"); ok {
					return static(dshLevels("dsh", ref)...)
				}
				return static(dshEfforts...)
			}
		}},
	{id: "empryo", name: "Empryo", dir: ".empryo", bin: "empryo", in: empryoIn,
		restart: newNotice("{agent} in WSL {distro} reads its config at start-up — restart open empryo sessions to use this.")},
	// Ante ships macOS and Linux builds alone, and suggests WSL on Windows,
	// so a Windows machine's Ante is usually this one
	{id: "ante", name: "Ante", dir: ".ante", bin: "ante", in: anteIn,
		restart: newNotice("{agent} in WSL {distro} reads its catalog at start-up — restart open ante sessions to use this.")},
	{id: "muse", name: "Muse Code", dir: ".config/muse", bin: "muse", in: museIn,
		restart: newNotice("{agent} in WSL {distro} reads its settings at start-up — restart open muse sessions to use this.")},
	{id: "qoder", name: "Qoder", dir: ".qoder", bin: "qodercli", in: qoderIn,
		restart: newNotice("{agent} in WSL {distro} reads its settings as a session starts — open sessions keep the model they have; new ones use this.")},
	{id: "qoder-cn", name: "Qoder CN", dir: ".qoder-cn", bin: "qoderclicn", in: qoderCNIn,
		restart: newNotice("{agent} in WSL {distro} reads its settings as a session starts — open sessions keep the model they have; new ones use this.")},
	// lgtm on Discord: magpie didn't find a CodeBuddy Code installed in WSL
	{id: "codebuddy", name: "CodeBuddy Code", dir: ".codebuddy", bin: "codebuddy", in: codebuddyIn,
		restart: newNotice("{agent} in WSL {distro} reads its model when a session starts — restart open codebuddy sessions, or run /clear in them, to use this.")},
	// Cline's CLI, and its VS Code extension in a Remote - WSL window, which
	// keep the same ~/.cline
	{id: "cline", name: "Cline", dir: ".cline", bin: "cline", in: clineIn,
		restart: newNotice("{agent} in WSL {distro} reads its provider as a session starts — open sessions keep the model they have; new ones use this. Reload VS Code's window for its extension."),
		asleep:  wslOwnAsleep("cline", "model")},
	{id: "atomcode", name: "AtomCode", dir: ".atomcode", bin: "atomcode", in: atomcodeIn,
		restart: newNotice("{agent} in WSL {distro} reads its settings at start-up — restart open atomcode sessions to use this."),
		asleep: func(key string) func(map[string]string) []Option {
			if key != "model" {
				return nil
			}
			// the models of its config.toml are the distro's files
			return func(cur map[string]string) []Option {
				return withVia(atomcodeOwnModels("", cur["model"]), "atomcode", magpieID+"/")
			}
		}},
	// Snow CLI's ~/.snow, its profiles the distro's own; a stopped
	// distro's are looked at once it is started
	{id: "snow", name: "Snow CLI", dir: ".snow", bin: "snow", in: snowIn,
		restart: newNotice("{agent} in WSL {distro} reads its profile at start-up — restart open snow sessions to use this."),
		asleep:  wslOwnAsleep("snow", "model")},
	// no dir: Antigravity keeps its folders in ~/.gemini too, so only the
	// command says Gemini CLI is there
	{id: "gemini", name: "Gemini CLI", bin: "gemini", in: geminiIn,
		restart: newNotice("{agent} in WSL {distro} reads its settings at start-up — restart open gemini sessions to see this."),
		asleep: func(key string) func(map[string]string) []Option {
			if key != "provider" {
				return nil
			}
			// which key its .env has is the distro's file
			return func(cur map[string]string) []Option {
				out := []Option{
					{Value: "google", Label: "Google", Icon: "gemini-color", Note: "Google account · OAuth sign-in"},
					{Value: "api-key", Label: "API key", Icon: "gemini-color"},
					{Value: "vertex", Label: "Vertex AI", Icon: "googlecloud-color", Note: "Vertex AI · $GOOGLE_CLOUD_PROJECT"},
				}
				if v := cur["provider"]; v == magpieID || v == "custom" {
					out = append(out, Option{Value: v})
				}
				return out
			}
		}},
	// Antigravity CLI, by its own folder under ~/.gemini or its command
	// (mimi001234 on Discord: magpie didn't find agy in WSL); it takes the
	// gateway only from how it is started, so the distro's shells get the
	// function agy (agyDistroFunction)
	{id: "agy", name: "Antigravity CLI", dir: ".gemini/antigravity-cli", bin: "agy", in: agyIn,
		restart: newNotice("{agent} in WSL {distro} takes magpie's gateway and model only from how it is started — type agy in a new terminal there: magpie put it in the shell's profile."),
		asleep: func(key string) func(map[string]string) []Option {
			if key != "model" {
				return nil
			}
			// its custom models are the distro's settings.json
			return func(cur map[string]string) []Option {
				var own []Option
				if c := cur["model"]; c != "" && !usesMagpie(c) {
					own = append(own, Option{Value: c, Icon: modelIcon("", c), Direct: "Google"})
				}
				return withVia(group("Antigravity CLI", own), "agy", magpieID+"/")
			}
		}},
	// Block's goose, by its folder alone: a goose command may be pressly's
	// database migration tool
	{id: "goose", name: "Goose", dir: ".config/goose", in: func(at place) *Agent { return gooseIn(at, at.join(".config")) },
		restart: newNotice("{agent} in WSL {distro} loads its providers at start-up — restart open goose sessions to use magpie's models."),
		asleep:  wslOwnAsleep("goose", "model")},
	// Reasonix's native CLI (1.39 on), by its command and the version it
	// gives: the historical npm client is reasonix too, and ~/.reasonix is
	// both's
	// MothX's ~/.mothx in the distro: its MOTHX_DIR there isn't read
	{id: "mothx", name: "MothX", dir: ".mothx", bin: "mothx", in: mothxIn,
		restart: newNotice("{agent} in WSL {distro} reads its settings at start-up — restart open mothx sessions to use this."),
		asleep:  wslOwnAsleep("mothx", "model")},
	// PenguinHarness's ~/.penguin/data in the distro: its PENGUIN_HOME
	// there isn't read; its server reads the file again as it changes
	{id: "penguin", name: "PenguinHarness", dir: ".penguin", bin: "penguin", in: penguinIn,
		asleep: wslOwnAsleep("penguin", "model")},
	{id: "reasonix", name: "Reasonix", bin: "reasonix", in: reasonixIn, version: true,
		accept: func(d distro) bool {
			v := d.Versions["reasonix"]
			return strings.HasPrefix(v, "2.") || strings.HasPrefix(v, "1.39.")
		},
		restart: newNotice("{agent} in WSL {distro} reads its settings at start-up — start a new reasonix process to use this."),
		asleep: func(key string) func(map[string]string) []Option {
			switch key {
			case "model", "planner":
				return func(cur map[string]string) []Option {
					out := append(ownOptions("", cur[key]), viaMagpieShared("reasonix", magpieID+"/")...)
					if key == "planner" {
						out = append([]Option{{Value: "off", Label: "off", Note: "Disable the separate planner"}}, out...)
					}
					return out
				}
			case "effort":
				// the levels of magpie's model, as its provider is written them
				return func(cur map[string]string) []Option {
					ref, ok := strings.CutPrefix(cur["model"], magpieID+"/")
					if !ok {
						return nil
					}
					levels := reasonixCatalogAt(magpieModels("reasonix"), ref, "", "").Overrides[ref].Efforts
					if len(levels) == 0 {
						return nil
					}
					return static(append([]string{"auto"}, levels...)...)
				}
			}
			return nil
		}},
}

// wslOwnAsleep is a stopped distro's options for an agent's model fields
// (keys) whose live ones read its files: the providers of the value last
// seen, and magpie's.
func wslOwnAsleep(id string, keys ...string) func(string) func(map[string]string) []Option {
	return func(key string) func(map[string]string) []Option {
		if !slices.Contains(keys, key) {
			return nil
		}
		return func(cur map[string]string) []Option {
			return withVia(ownOptions("", cur[key]), id, magpieID+"/")
		}
	}
}

// found reports whether the probe found the agent in d.
func (k wslKind) found(d distro) bool {
	if k.accept != nil && !k.accept(d) {
		return false
	}
	return k.dir != "" && d.Has["dir:"+k.dir] || k.bin != "" && d.Has["bin:"+k.bin]
}

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

// wslKept is whether magpie keeps a distro it probed: one of wslKinds is
// there, or the Copilot CLI's folder, whose sign-in it reads (#723).
func wslKept(d distro) bool { return wslFound(d) || d.Has["dir:"+wslCopilotDir] }

// ListsFor is whose model lists an agent's are (provider.CatalogFor): its
// own id's, or, for one in a WSL distro, its Windows twin's (claude for
// claude@wsl:Ubuntu). A distro's agent is written magpie's models under
// that id and sends the gateway that id's key, so the models picked for
// its row are the ones it is shown only when kept under that id too (#927).
func (a *Agent) ListsFor() string {
	id, _, _ := strings.Cut(a.ID, "@wsl:")
	// a named omp profile (omp#work) shares omp's lists: it is another
	// omp, not another agent with a catalog of its own
	id, _, _ = strings.Cut(id, "#")
	return id
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
	at := d.place(id)
	a := k.in(at)
	if k.restartAt != nil {
		k.restart = k.restartAt(at)
	}
	a.ID, a.Name, a.Aliases, a.Bin, a.UA, a.WSL = id, k.name+" · WSL "+d.Name, nil, "", nil, d.Name
	a.wslVersion = at.version
	a.Gateway = d.base
	a.reachFrom = func(base string) (ok, known bool) {
		if !d.live() {
			return false, false // never started to be asked
		}
		key := "wsl " + d.Name + " " + base
		return probed(key, func() bool {
			if !d.live() {
				return true // stopped since: never started to be asked
			}
			ok, told := wslReach(d.Name, base)
			return ok || !told // can't tell: taken as answering
		})
	}
	if d.Running {
		a.Home = d.local(d.Home)
	}
	a.detect = func() bool { return k.found(d) }
	// its requests carry the agent's User-Agent and are counted as its
	// Windows twin's, so a prompt with none of "its" own seen isn't a bypass
	a.LastUsed = nil
	a.Notice = func() string {
		if !d.Mirrored {
			return d.notMirrored(k.name)
		}
		if d.noRelay {
			return noticeWSLNoRelay.say("distro", d.Name, "net", d.Net, "agent", k.name)
		}
		if k.restart == "" {
			return ""
		}
		return k.restart.say("agent", k.name, "distro", d.Name, "command", provider.CodexDaemonRestart)
	}
	if !d.Running {
		return asleep(a, k, d)
	}
	if k.dir != "" {
		at.join(filepath.FromSlash(k.dir))
	}
	if err := *at.unreached; err != nil {
		return unreached(a, err)
	}
	if reached := a.Reached; reached != nil {
		a.Reached = func(since time.Time) (time.Time, string, bool) {
			if !d.live() {
				return time.Time{}, "", false
			}
			at, to, refused := reached(since)
			if sameHost(to, d.base()) {
				to = gateway.URL() // the gateway, however WSL reaches it
			}
			return at, to, refused
		}
	}
	// the agent may be read after the distro stopped (it is made at most
	// wslRunningAge before): what reads its files in the background asks
	// again first, and a stopped one reads as last seen
	for i := range a.Fields {
		f := &a.Fields[i]
		get, key := f.Get, k.memo(f.Key)
		f.Get = func() string {
			if !d.live() {
				return wslLastSeen(d.Name, key)
			}
			v := get()
			wslRemember(d.Name, key, v)
			return v
		}
	}
	if sync := a.Sync; sync != nil {
		a.Sync = func() error {
			if !d.live() {
				return nil // its files get the catalog once it runs
			}
			return sync()
		}
	}
	if check := a.Check; check != nil {
		a.Check = func() string {
			if !d.live() {
				return ""
			}
			return check()
		}
	}
	for _, p := range []*func() bool{&a.Routed, &a.Joined, &a.Beside} {
		if f := *p; f != nil {
			*p = func() bool { return d.live() && f() }
		}
	}
	// a stopped distro's app can't be read: unknown, not "not added"
	if f := a.Added; f != nil {
		a.Added = func() (bool, bool) {
			if !d.live() {
				return false, false
			}
			return f()
		}
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

// unreached is an agent in a distro whose folder is a link the distro
// couldn't resolve (distro.open): nothing of it is written, and each
// change, and its notice, says why.
func unreached(a *Agent, err error) *Agent {
	a.unreached = err
	a.Notice = func() string { return err.Error() }
	for i := range a.Fields {
		if a.Fields[i].Set != nil {
			a.Fields[i].Set = func(string) error { return err }
		}
	}
	if a.Sync != nil {
		a.Sync = func() error { return err }
	}
	return a
}

// Unreached is why magpie can't open the folder of a, an agent in a WSL
// distro: a link there the distro couldn't resolve; nil for any other.
func (a *Agent) Unreached() error { return a.unreached }

// WSLOpen is path, under the home of a, an agent in a WSL distro, where
// magpie can open it (distro.open): below a link of the distro's, where
// the link leads. The error says a link there couldn't be resolved. Any
// other agent's path is as it is.
func WSLOpen(a *Agent, path string) (string, error) {
	if a == nil || a.WSL == "" {
		return path, nil
	}
	wsl.Lock()
	d := wsl.seen[a.WSL]
	var c distro
	if d != nil {
		c = *d
	}
	wsl.Unlock()
	if d == nil || c.Root == "" {
		return path, nil
	}
	return c.open(path)
}

// asleep is a stopped distro's agent: its fields read what magpie last
// saw, their options come from magpie alone, and it has no files to check,
// sync or migrate (Path and Dir are empty); setting a field goes to the
// files, which starts the distro.
func asleep(live *Agent, k wslKind, d distro) *Agent {
	started := false
	a := &Agent{ID: live.ID, Name: live.Name, Icon: live.Icon, WSL: d.Name, detect: live.detect, Spelled: live.Spelled, ModelAliases: live.ModelAliases, NoNames: live.NoNames,
		Notice: func() string {
			if started {
				return live.Notice()
			}
			return noticeWSLAsleep.say("distro", d.Name)
		}}
	for _, lf := range live.Fields {
		key := lf.Key
		f := Field{Key: key, Label: lf.Label, Quiet: lf.Quiet, Live: lf.Live, Follows: lf.Follows, Options: lf.Options,
			Get: func() string { return wslLastSeen(d.Name, k.memo(key)) },
			Set: func(v string) error {
				// opening a stopped distro's files is aborted rather than
				// waiting for it to start, so it is started first
				if _, err := wslRun(time.Minute, "-d", d.Name, "-e", "true"); err != nil {
					return fmt.Errorf("start WSL %s: %w", d.Name, err)
				}
				started = true
				// the agent again, now its files can be looked at: which of
				// them it reads (opencode.jsonc or .json, ~/.kimi-code or
				// ~/.kimi) is theirs to say, not the defaults it was built on
				w := d
				w.Running = true
				warm := k.in(w.place(live.ID))
				wf := warm.Field(key)
				if wf == nil || wf.Set == nil {
					return fmt.Errorf("%s has no %s", live.Name, key)
				}
				err := wf.Set(v)
				// a model settles the effort too
				for _, f := range warm.Fields {
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
		// Claude Code's claude.ai sign-in is kept only where the gateway
		// takes any key, which the distro's address says without its files
		if k.id == "claude" && key == "login" && d.place(live.ID).gwKey() != gateway.Token {
			f.Options = func(map[string]string) []Option { return nil }
		}
		a.Fields = append(a.Fields, f)
	}
	return a
}

// wslAgents are the agents in this machine's WSL distros; none off Windows.
func wslAgents() []*Agent {
	if !wslLooks() {
		return nil
	}
	return wslAgentsOf(wslDistros())
}

func wslAgentsOf(ds []distro) []*Agent {
	var out []*Agent
	for _, d := range ds {
		// only the agents it has: magpie writes nothing for one that isn't
		// there, and doesn't make it, which reads its files: a stopped
		// distro's files opened start it again (#3896)
		for _, k := range wslKinds {
			if k.found(d) {
				out = append(out, wslAgent(k, d))
			}
		}
		// the named omp profiles the probe remembered: the row is made
		// because the probe found it, not because a default omp row is
		// there, and its detection is that memory
		for _, name := range d.OmpProfiles {
			k := wslKindOf("omp")
			k.id, k.name = "omp#"+name, "omp · "+name
			k.in = func(at place) *Agent {
				return ompAt(at, at.join(".omp", "profiles", name, "agent"),
					func() ompProviderEntry { return ompProviderAt(at.gw(), at.version) })
			}
			out = append(out, wslAgent(k, d))
		}
	}
	return out
}

// wslHomes are the distros agents (or the Copilot CLI's folder, wslKept)
// were found in, each user's home as magpie
// opens it, for internal/sessions to read their sessions in; none off
// Windows. A stopped distro's is its home as last probed.
func wslHomes() []sessions.WSLHome {
	if !wslLooks() {
		return nil
	}
	var out []sessions.WSLHome
	for _, d := range wslDistros() {
		if d.Root == "" || d.Home == "" {
			continue
		}
		open := func(path string) string {
			if to, err := d.open(path); err == nil {
				return to
			}
			return path
		}
		out = append(out, sessions.WSLHome{Distro: d.Name, Home: d.local(d.Home), Running: d.Running, Open: open})
	}
	return out
}

// wslCopilotDir is the Copilot CLI's folder under a distro's $HOME: not an
// agent magpie sets up, but a sign-in it uses (provider.CopilotWSLDirs),
// so a distro that has only it is kept as one agents are found in (#723).
const wslCopilotDir = ".copilot"

// copilotWSLDirs are the Copilot CLI's folders, as magpie opens them (where
// a link leads, #4502), in the running distros it was found in, for
// provider to read its sign-in in; none off Windows. A stopped distro is
// left out: reading it would start it.
func copilotWSLDirs() []string {
	var out []string
	for _, h := range wslHomes() {
		if h.Running && wslHasCopilot(h.Distro) {
			out = append(out, h.Open(filepath.Join(h.Home, wslCopilotDir)))
		}
	}
	return out
}

func wslHasCopilot(name string) bool {
	wsl.Lock()
	defer wsl.Unlock()
	d := wsl.seen[name]
	return d != nil && d.Has["dir:"+wslCopilotDir]
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
	runAt   time.Time       // when running was listed
	runOK   bool            // running is wsl.exe's answer, not a failure
	listed  bool            // names is a real answer, and may forget distros
	seen    map[string]*distro
	probed  map[string]bool
	failed  map[string]time.Time
	dirty   bool
}

const (
	wslListAge  = time.Minute
	wslRetryAge = 10 * time.Minute
	// wslRunningAge is how long which distros run is taken as wsl.exe
	// said: one the user stops (wsl --shutdown or --terminate, to repair
	// WSL) is seen stopped within it, before anything opens its files,
	// which would start it again (TJHHHH on Discord)
	wslRunningAge = 3 * time.Second
	// wslRunningFailedAge is how long wsl.exe failing to say is kept: each
	// ask that times out holds wsl's lock, and so the GUI, for 10s, and
	// leaves a wsl.exe behind (#2341)
	wslRunningFailedAge = 30 * time.Second
)

// wslOn is whether there is WSL to look in: on Windows, or in tests.
var wslOn = runtime.GOOS == "windows"

// wslLooks is whether magpie looks in WSL on its own: there is WSL, and
// Settings' Detect agents in WSL is on (#1264).
func wslLooks() bool { return wslOn && !settings.Load().NoWSLAgents }

// wslRun runs wsl.exe; a var for tests.
// wslReachScript asks, inside a distro, whether anything answers at $1
// (http://host:port) on host $2, port $3: "ok" when it does, "no" when the
// connection is refused or times out, "?" when the distro has nothing to
// ask with.
const wslReachScript = `u="$1"; h="$2"; p="$3"
if command -v curl >/dev/null 2>&1; then
  curl -s -o /dev/null -m 3 "$u/v1/models"; c=$?
  case $c in 0) echo ok;; 7|28) echo no;; *) echo '?';; esac; exit 0
fi
if command -v wget >/dev/null 2>&1; then
  wget -q -O /dev/null -T 3 -t 1 "$u/v1/models"; c=$?
  case $c in 0|8) echo ok;; 4) echo no;; *) echo '?';; esac; exit 0
fi
if command -v bash >/dev/null 2>&1 && command -v timeout >/dev/null 2>&1; then
  if timeout 3 bash -c 'exec 3<>/dev/tcp/'"$h"'/'"$p" 2>/dev/null; then echo ok; else echo no; fi; exit 0
fi
echo '?'`

// wslReach is whether base answers from inside the running distro name,
// and whether that could be told.
func wslReach(name, base string) (ok, known bool) {
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" {
		return false, false
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443"}[u.Scheme]
		if port == "" {
			port = "80"
		}
	}
	out, err := wslRun(10*time.Second, "-d", name, "-e", "sh", "-c", wslReachScript, "sh", strings.TrimSuffix(base, "/"), u.Hostname(), port)
	if err != nil {
		return false, false
	}
	switch strings.TrimSpace(string(out)) {
	case "ok":
		return true, true
	case "no":
		return false, true
	}
	return false, false
}

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

// wslHungPath, while recent, says a wsl.exe listing timed out: each
// command a hung WSL held for its 10s timeout (20s with the running list),
// `magpie providers` for Copilot's sign-in in a distro, `magpie version`
// for start-up's moves.
func wslHungPath() string { return filepath.Join(filepath.Dir(provider.Path()), "wsl-hung") }

// wslHungAfter: a failure this slow is a hang, not no WSL; a var for tests.
var wslHungAfter = 5 * time.Second

const wslHungAge = 10 * time.Minute // how long another process takes it as hung

func wslHungLately() bool {
	fi, err := os.Stat(wslHungPath())
	return err == nil && time.Since(fi.ModTime()) < wslHungAge
}

func wslHung(hung bool) {
	if hung {
		_ = os.WriteFile(wslHungPath(), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
	} else {
		_ = os.Remove(wslHungPath())
	}
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
		if wsl.at.IsZero() && wslHungLately() {
			// another magpie waited out a hung wsl.exe moments ago: a
			// command doesn't wait 10s more (this one lists again in a minute)
			wsl.names, wsl.listed = nil, false
		} else {
			start := time.Now()
			wsl.names, wsl.listed = wslList("-l", "-q")
			wslHung(!wsl.listed && time.Since(start) >= wslHungAfter)
		}
		wsl.at = time.Now()
		if !wsl.listed || len(wsl.names) == 0 {
			// none to run: a wsl.exe that couldn't list them isn't asked
			// again which run, another timeout under the lock the GUI waits on
			wsl.running, wsl.runOK, wsl.runAt = map[string]bool{}, wsl.listed, time.Now()
		}
	}
	wslRunningLocked()
	installed := map[string]bool{}
	cfg := wslConfig()
	var out []distro
	for _, n := range wsl.names {
		installed[n] = true
		// a distro that stopped is probed again once it runs: WSL may have
		// been restarted meanwhile in another networking mode
		if !wsl.running[n] {
			delete(wsl.probed, n)
		}
		if wsl.running[n] && !wsl.probed[n] {
			if t, ok := wsl.failed[n]; !ok || time.Since(t) > wslRetryAge {
				if d := wslProbe(n); d == nil {
					wsl.failed[n] = time.Now()
				} else {
					wsl.probed[n] = true
					if wslKept(*d) {
						if old := wsl.seen[n]; old != nil {
							d.Values = old.Values
							d.Was = wslWas(old.Was, old.Gateway, d.Gateway)
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
		c.Running = wsl.running[n]
		c.up = func() bool { return wslUp(n) }
		c.mirror(cfg)
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

// wslWas is the addresses a distro reached Windows at before, with the
// last one (was) added when it reaches Windows elsewhere now: the newest
// few, now's left out.
func wslWas(before []string, was, now string) []string {
	out := slices.DeleteFunc(slices.Clone(before), func(h string) bool { return h == was || h == now })
	if was != "" && was != now {
		out = append(out, was)
	}
	if len(out) > 4 {
		out = out[len(out)-4:]
	}
	if len(out) == 0 {
		return nil
	}
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
		if !wslKept(*d) {
			delete(wsl.seen, n)
			wsl.dirty = true
		}
	}
}

// wslRunningLocked lists the running distros again (wsl.exe -l --running,
// which starts none) when the last list is older than wslRunningAge. When
// wsl.exe can't say, none is taken to run: a distro is never opened on a
// guess.
func wslRunningLocked() {
	age := wslRunningAge
	if !wsl.runOK {
		age = wslRunningFailedAge
	}
	if !wsl.runAt.IsZero() && time.Since(wsl.runAt) <= age {
		return
	}
	run, ok := wslList("-l", "--running", "-q")
	wsl.runOK = ok
	wsl.running = map[string]bool{}
	for _, n := range run {
		wsl.running[n] = true
	}
	wsl.runAt = time.Now()
}

// wslUp is whether distro runs, as wsl.exe said at most wslRunningAge ago:
// what reads a distro's files in the background (a field shown, the
// catalog synced, requests counted, sessions listed) asks it first, so a
// distro stopped since its agent was made isn't started again.
func wslUp(distro string) bool {
	wsl.Lock()
	defer wsl.Unlock()
	wslRunningLocked()
	return wsl.running[distro]
}

// WSLRunning is whether the WSL distro runs now (wslUp); false off
// Windows. What another package does in a distro on its own, rather than
// at the user's asking, asks it first.
func WSLRunning(distro string) bool { return wslLooks() && wslUp(distro) }

// wslSave writes wsl.json if anything in it changed.
func wslSave() {
	wsl.Lock()
	defer wsl.Unlock()
	wslSaveLocked()
}

// wslSaveLocked writes wsl.seen when it changed. wsl.json holds every
// distro's last probe, and wslDistros reads a file that doesn't parse as
// no distro: such a file is copied aside first, and one that can't be
// copied isn't written over (#2263).
func wslSaveLocked() {
	if !wsl.dirty {
		return
	}
	if err := lastgood.KeepUnparsed(wslStatePath(), lastgood.Into[map[string]*distro]); err != nil {
		log.Printf("wsl.json wasn't written over: %v", err)
		return
	}
	if b, err := json.MarshalIndent(wsl.seen, "", "  "); err == nil && edit.WriteAtomic(wslStatePath(), b) == nil {
		wsl.dirty = false
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

// WSLKeep has magpie remember v under key for the WSL distro name, in
// wsl.json beside what its probe found, for while the distro is stopped:
// what another package learnt of the distro's files while it ran (the
// library's places for each agent there), which it can't read again then
// without starting it (#3896). Nothing is kept for a distro never found.
func WSLKeep(name, key, v string) {
	wslRemember(name, key, v)
	wslSave()
}

// WSLKept is what WSLKeep last kept under key for the distro; "" for
// nothing.
func WSLKept(name, key string) string { return wslLastSeen(name, key) }

func wslLastSeen(name, key string) string {
	wsl.Lock()
	defer wsl.Unlock()
	if d := wsl.seen[name]; d != nil {
		return d.Values[key]
	}
	return ""
}

// wslReady are the distros running now that the probe found kind in, each
// with its mirrored networking settled, ready for a read of its files.
// Only magpie's memory of them and which run (wslRunningLocked) are
// asked — nothing is probed, and a stopped distro is never opened.
// Detection off (wslLooks) reads none: a request's stand-in asks wsl.exe
// nothing, which on a machine without WSL would answer with a console
// window asking to install it (#4872).
func wslReady(kind string) []distro {
	if !wslLooks() {
		return nil
	}
	wsl.Lock()
	wslRunningLocked()
	var ds []distro
	for _, n := range wsl.names {
		if d := wsl.seen[n]; d != nil && wsl.running[n] && wslKindOf(kind).found(*d) {
			ds = append(ds, *d)
		}
	}
	wsl.Unlock()
	cfg := wslConfig()
	for i := range ds {
		ds[i].mirror(cfg)
	}
	return ds
}

// wslClaudeStandIn is StandIn for a Claude Code in a WSL distro routed
// through magpie: the first, of the distros running now, whose
// settings.json has a stand-in.
func wslClaudeStandIn(model string) string {
	for _, d := range wslReady("claude") {
		path := filepath.Join(d.local(d.Home), ".claude", "settings.json")
		if m := claudeStandInAt(d.place("claude@wsl:"+d.Name), path, model); m != "" {
			return m
		}
	}
	return ""
}

// wslClaudeClassifier is ClassifierStandIn for a Claude Code in a running
// distro, known by the same User-Agent. Detection off reads none (#4872).
func wslClaudeClassifier(model string) string {
	if !wslLooks() {
		return ""
	}
	wsl.Lock()
	wslRunningLocked()
	var ds []distro
	for _, n := range wsl.names {
		if d := wsl.seen[n]; d != nil && wsl.running[n] && wslKindOf("claude").found(*d) {
			ds = append(ds, *d)
		}
	}
	wsl.Unlock()
	cfg := wslConfig()
	for _, d := range ds {
		d.mirror(cfg)
		if m := claudeClassifierIn(d.place("claude@wsl:"+d.Name), model); m != "" {
			return m
		}
	}
	return ""
}

// wslCodexStandIn is StandIn for a Codex in a WSL distro routed through
// magpie: the first, of the distros running now, whose config.toml has
// magpie as its provider, as this machine's codexStandIn reads it.
func wslCodexStandIn() string {
	for _, d := range wslReady("codex") {
		path := filepath.Join(d.place("codex@wsl:"+d.Name).codexHome(), "config.toml")
		if m := codexStandIn(path); m != "" {
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
// (wslinfo's networking mode came without a new version: a probe that
// didn't ask leaves Net "", read as .wslconfig says, as before.)
const wslProbeVersion = 2

// wslProbeScript prints the distro's home, what of each of wslKinds it
// has (each command with where it is), where Windows' drives are mounted,
// its default route (the Windows host under NAT), and WSL's networking
// mode as wslinfo (WSL 2.0 on) says it; and the version of each that asks
// for it (omp, whose models.yml takes max only from 16.4.0).
//
// A command is also looked for in the folders its installers put it in
// (wslbin): sh -l reads ~/.profile but not ~/.bashrc, where bun's and
// nvm's installers add theirs to PATH (whqtian on Discord: a WSL omp from
// bun was never asked its version, so its models still offered xhigh).
// It is asked with its own folder on PATH, where bun's script finds bun.
//
// Of nvm's Nodes, the one a terminal there gets is looked in first: the
// version nvm's default alias resolves to, asked of nvm itself
// (nvm_version default, nvm.sh read by bash with --no-use), then the rest
// highest first. The glob this replaced took them in name order, so a
// command under v18 was found before the default v22's, and v9's after
// both (as proc.UserPath did on a Mac or Linux, 9f407cafa).
//
// The folders are those of proc.UserBinDirs: fnm's default and then its
// versions, mise's installs, ~/.npmrc's prefix and Homebrew's among them
// (千里不再 on Discord: a WSL Pi from fnm, not run yet so with no ~/.pi,
// wasn't found). The shell isn't asked interactively for the PATH ~/.bashrc
// builds: under timeout(1) an interactive bash stops itself on the
// terminal (state T) and outlived its timeout on WSL (Ubuntu 24.04).
//
// A command on Windows' drives isn't the distro's (parseProbe), so one
// there is looked past (wslwin): WSL puts Windows' PATH on the distro's,
// and a Pi npm installed on Windows hid the distro's own from wslbin.
var wslProbeScript = func() string {
	s := `nv="${NVM_DIR:-$HOME/.nvm}"; nd=; [ -s "$nv/nvm.sh" ] && command -v bash >/dev/null 2>&1 && nd=$(NVM_DIR="$nv" timeout 10 bash -c '. "$NVM_DIR/nvm.sh" --no-use >/dev/null 2>&1; nvm_version default 2>/dev/null' </dev/null 2>/dev/null); ` +
		// Windows' drives, as /proc/mounts has them (C:\ written C:\134)
		`wm=$(awk '$1 ~ /^[A-Za-z]:/ {print $2}' /proc/mounts 2>/dev/null); ` +
		`wslwin() { case "$1" in /mnt/[A-Za-z]/*) return 0;; esac; for m in $wm; do case "$1/" in "$m"/*) return 0;; esac; done; return 1; }; ` +
		`inpath() { o=$IFS; IFS=:; r=; for d in $2; do case "$d" in /*) wslwin "$d" || { [ -f "$d/$1" ] && [ -x "$d/$1" ] && { r="$d/$1"; break; }; };; esac; done; IFS=$o; [ -n "$r" ] && echo "$r"; }; ` +
		`np=$(sed -n 's/^[[:space:]]*prefix[[:space:]]*=[[:space:]]*//p' "$HOME/.npmrc" 2>/dev/null | head -n1 | tr -d "\"'\r"); case "$np" in "~/"*) np="$HOME/${np#??}";; /*) ;; *) np=;; esac; ` +
		`fr=; for r in "$FNM_DIR" "${XDG_DATA_HOME:-$HOME/.local/share}/fnm" "$HOME/.fnm"; do case "$r" in /*) [ -d "$r/node-versions" ] && { fr=$r; break; };; esac; done; ` +
		`vs() { ls "$1" 2>/dev/null | sed 's/^v*\(.*\)$/\1 &/' | sort -t. -k1,1nr -k2,2nr -k3,3nr | cut -d' ' -f2; }; ` +
		`wslbin() { for d in "$HOME/.bun/bin" "$HOME/.local/bin" "$HOME/.npm-global/bin" "$HOME/.volta/bin" "$HOME/.local/share/pnpm"; do [ -x "$d/$1" ] && { echo "$d/$1"; return 0; }; done; ` +
		`for v in $nd $(ls "$nv/versions/node" 2>/dev/null | sort -t. -k1.2,1nr -k2,2nr -k3,3nr); do d="$nv/versions/node/$v/bin"; [ -x "$d/$1" ] && { echo "$d/$1"; return 0; }; done; ` +
		`if [ -n "$fr" ]; then for d in "$fr/aliases/default/bin" $(vs "$fr/node-versions" | sed "s|.*|$fr/node-versions/&/installation/bin|"); do [ -x "$d/$1" ] && { echo "$d/$1"; return 0; }; done; fi; ` +
		`for d in $(vs "$HOME/.local/share/mise/installs/node" | sed "s|.*|$HOME/.local/share/mise/installs/node/&/bin|"); do [ -x "$d/$1" ] && { echo "$d/$1"; return 0; }; done; ` +
		`for d in "$HOME/.local/share/mise/shims" "$HOME/.asdf/shims" "$HOME/.npm/bin" ${np:+"$np/bin"} "$HOME/.cargo/bin" "$HOME/.deno/bin" /home/linuxbrew/.linuxbrew/bin "$HOME/.linuxbrew/bin"; do [ -x "$d/$1" ] && { echo "$d/$1"; return 0; }; done; return 1; }; ` +
		`wslcmd() { p=$(command -v "$1" 2>/dev/null) && case "$p" in /*) wslwin "$p" || { echo "$p"; return 0; };; esac; inpath "$1" "$PATH" || wslbin "$1"; }; `
	s += `echo "home:$HOME"; `
	for _, k := range wslKinds {
		if k.dir != "" {
			s += `[ -d "$HOME/` + k.dir + `" ] && echo dir:` + k.dir + `; `
		}
		if k.bin != "" {
			s += `p=$(wslcmd ` + k.bin + `) && echo "bin:` + k.bin + ` $p"`
			if k.version {
				// not one on Windows' drives (WSL's /mnt/c), which isn't the distro's
				s += ` && case "$p" in /mnt/*) ;; *) echo "ver:` + k.id + ` $(PATH="${p%/*}:$PATH" timeout 10 "$p" --version </dev/null 2>&1 | head -n1)";; esac`
			}
			s += `; `
		}
	}
	s += `[ -d "$HOME/` + wslCopilotDir + `" ] && echo dir:` + wslCopilotDir + `; `
	// where each agent folder that leads elsewhere leads (distro.open):
	// "link:.codex /mnt/c/Users/me/.codex", or "link:.codex" alone where
	// readlink -f couldn't say
	// (a $HOME that is itself through a link is left as it is)
	s += `h=$(readlink -f -- "$HOME" 2>/dev/null); for r in ` + strings.Join(wslLinkPaths(), " ") + `; do p="$HOME/$r"; { [ -e "$p" ] || [ -L "$p" ]; } || continue; ` +
		`if t=$(readlink -f -- "$p" 2>/dev/null) && [ -n "$t" ]; then [ "$t" = "$p" ] || [ "$t" = "$h/$r" ] || echo "link:$r $t"; else echo "link:$r"; fi; done; `
	// a drive's source in /proc/mounts is C:\ (written C:\134), under any
	// automount root: its mount point, and the drive
	s += `awk '$1 ~ /^[A-Za-z]:/ {print "win:" $2 " " toupper(substr($1,1,1)) ":"}' /proc/mounts 2>/dev/null; `
	// named omp profiles: the probe runs only in a distro that is running,
	// so this never starts a stopped one to find them
	s += `for d in "$HOME"/.omp/profiles/*; do [ -d "$d" ] && echo "profile:omp:$(basename "$d")"; done; `
	return s + `ip route show default 2>/dev/null | head -n1 | sed 's/^/route:/'; ` +
		`grep -m1 '^nameserver' /etc/resolv.conf 2>/dev/null | sed 's/^/ns:/'; ` +
		`command -v wslinfo >/dev/null 2>&1 && echo "net:$(wslinfo --networking-mode 2>/dev/null)"; true`
}()

// wslLinkPaths are the paths under a distro's $HOME the probe asks where
// they lead (distro.Links): each agent's folder and its skills, and the
// folders and files the agents keep beside theirs.
func wslLinkPaths() []string {
	out := []string{".config", ".local", ".local/share", ".agents", ".agents/skills", ".gemini", ".gemini/config", ".gemini/skills", ".kimi", ".reasonix", ".claude.json", ".slock", ".kimi-code/sessions", wslCopilotDir}
	for _, k := range wslKinds {
		if k.dir != "" {
			out = append(out, k.dir, k.dir+"/skills")
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func wslProbe(name string) *distro {
	b, err := wslRun(30*time.Second, "-d", name, "-e", "sh", "-lc", wslProbeScript)
	if err != nil {
		return nil
	}
	d := parseProbe(name, string(b))
	if d == nil {
		return nil
	}
	if wslKept(*d) {
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
		case "ver":
			id, said, _ := strings.Cut(v, " ")
			if ver := parseVersion(said); ver != "" {
				if d.Versions == nil {
					d.Versions = map[string]string{}
				}
				d.Versions[id] = ver
			}
		case "bin":
			bin, path, _ := strings.Cut(v, " ")
			bins[bin] = strings.TrimSpace(path)
		case "win":
			// win:/mnt/c C: (a probe before it said only /mnt/c)
			f := strings.Fields(v)
			if len(f) == 0 {
				continue
			}
			if m := strings.TrimRight(f[0], "/"); strings.HasPrefix(m, "/") {
				win = append(win, m+"/")
				if len(f) > 1 && len(f[1]) == 2 && f[1][1] == ':' && isLetter(f[1][0]) {
					if d.Drives == nil {
						d.Drives = map[string]string{}
					}
					d.Drives[m] = strings.ToUpper(f[1])
				}
			}
		case "link":
			// link:.codex /mnt/c/Users/me/.codex; link:.codex alone where
			// it couldn't be resolved
			rel, to, _ := strings.Cut(v, " ")
			if !slices.Contains(wslLinkPaths(), rel) {
				continue
			}
			if to = strings.TrimSpace(to); !strings.HasPrefix(to, "/") {
				to = ""
			}
			if d.Links == nil {
				d.Links = map[string]string{}
			}
			d.Links[rel] = strings.TrimRight(to, "/")
		case "route":
			// default via 172.20.0.1 dev eth0 …
			if f := strings.Fields(v); len(f) >= 3 && f[1] == "via" && net.ParseIP(f[2]) != nil {
				d.Gateway = f[2]
			}
		case "ns":
			if f := strings.Fields(v); len(f) >= 2 && net.ParseIP(f[1]) != nil {
				ns = f[1]
			}
		case "net":
			// one word; anything else (an old wslinfo's usage) says nothing
			if m := strings.ToLower(strings.TrimSpace(v)); m != "" && !strings.ContainsAny(m, " \t") {
				d.Net = m
			}
		case "profile":
			// profile:omp:<name>: a named omp profile the distro has
			kind, name, _ := strings.Cut(v, ":")
			if kind != "omp" || !ompProfileOK(name) {
				continue
			}
			if !slices.Contains(d.OmpProfiles, name) {
				d.OmpProfiles = append(d.OmpProfiles, name)
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
	// the default route of a distro whose 127.0.0.1 is Windows' is
	// Windows' own next hop (consomme: 198.18.0.2 under a TUN proxy,
	// #1230), not Windows: an agent left on it is moved to 127.0.0.1
	// (wslMovedFrom). A bridged distro's is the LAN's router: Windows is
	// at its address on the bridge's adapter (wslrun.HostAt), and an agent
	// an older magpie pointed at the router is written over on the next
	// sync, the router being among the addresses it was seen at (Was)
	d.Gateway = wslrun.HostAt(d.Net, d.Gateway, gateway.WSLNets())
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

// wslConfig is %UserProfile%\.wslconfig, "" if there is none: UTF-8, or
// UTF-16 as Notepad's "Unicode" saves it, which WSL reads as well.
func wslConfig() string {
	home, _ := os.UserHomeDir() // %UserProfile% on Windows
	b, _ := os.ReadFile(filepath.Join(home, ".wslconfig"))
	return decodeText(b)
}

// decodeText is a text file's contents: UTF-16 with a BOM (either byte
// order), UTF-16LE without one, or else UTF-8.
func decodeText(b []byte) string {
	if len(b) >= 2 && b[0] == 0xfe && b[1] == 0xff {
		u := make([]uint16, (len(b)-2)/2)
		for i := range u {
			u[i] = uint16(b[2+2*i])<<8 | uint16(b[3+2*i])
		}
		return string(utf16.Decode(u))
	}
	return decodeWSL(b)
}

// wslMirrored reports whether a .wslconfig puts WSL 2 in mirrored
// networking: networkingMode=mirrored under [wsl2].
func wslMirrored(cfg string) bool {
	v, _ := wslSetting(cfg, "networkingMode")
	return strings.EqualFold(v, "mirrored")
}

// wslOff reports whether a .wslconfig turns key under [wsl2] off: false,
// as WSL reads a boolean (false, or 0).
func wslOff(cfg, key string) bool {
	v, ok := wslSetting(cfg, key)
	return ok && (strings.EqualFold(v, "false") || v == "0")
}

// wslSetting is key's value under [wsl2] in a .wslconfig, the last one
// set, unquoted and without a trailing comment; ok is false when it isn't
// set there.
func wslSetting(cfg, key string) (val string, ok bool) {
	section := ""
	sc := bufio.NewScanner(strings.NewReader(strings.TrimPrefix(cfg, "\ufeff")))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || l[0] == '#' || l[0] == ';' {
			continue
		}
		if strings.HasPrefix(l, "[") {
			// [wsl2], or [wsl2] # a comment
			if end := strings.Index(l, "]"); end > 0 {
				section = strings.ToLower(strings.TrimSpace(l[1:end]))
				continue
			}
		}
		k, v, found := strings.Cut(l, "=")
		if !found || section != "wsl2" || !strings.EqualFold(strings.TrimSpace(k), key) {
			continue
		}
		if i := strings.IndexAny(v, "#;"); i >= 0 {
			v = v[:i]
		}
		val, ok = strings.Trim(strings.TrimSpace(v), `"`), true
	}
	return val, ok
}

// what an agent in WSL says after a change, besides its kind's restart
// (notice.go)
var (
	noticeWSLNotMirrored = newNotice("WSL {distro} is in {net} networking, not mirrored, so its {agent} was pointed at Windows ({url}) rather than 127.0.0.1; magpie listens there for WSL, and it answers once Windows' firewall lets WSL in. For 127.0.0.1, set networkingMode=mirrored under [wsl2] in %UserProfile%\\.wslconfig and run wsl --shutdown, then pick the model again.")
	noticeWSLNoMode      = newNotice("magpie couldn't ask WSL {distro} its networking mode (it has no wslinfo) and %UserProfile%\\.wslconfig doesn't set it to mirrored, so its {agent} was pointed at Windows ({url}) rather than 127.0.0.1; magpie listens there for WSL, and it answers once Windows' firewall lets WSL in. For 127.0.0.1, set networkingMode=mirrored under [wsl2] in %UserProfile%\\.wslconfig and run wsl --shutdown, then pick the model again.")
	noticeWSLNoRelay     = newNotice("WSL {distro} is in {net} networking with localhostForwarding=false in %UserProfile%\\.wslconfig, so 127.0.0.1 there, where its {agent} was pointed, isn't relayed to Windows, where magpie listens. Remove localhostForwarding=false under [wsl2] (it is on by default) and run wsl --shutdown.")
	noticeWSLAsleep      = newNotice("WSL {distro} isn't running: magpie shows what it last saw there, and starts it only to change something.")
)
