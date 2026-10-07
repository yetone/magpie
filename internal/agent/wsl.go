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
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
)

// An agent installed in a WSL distro reads its config there, not in
// Windows' home — so does the Codex app's WSL connection. On Windows magpie
// lists the distros (wsl.exe -l -q), asks each running one once for its
// $HOME and which of wslKinds' agents are there (never starting one that is
// stopped), and edits the files through \\wsl.localhost\<distro>. Each is
// an agent of its own, <id>@wsl:<distro> (codex@wsl:Ubuntu,
// pi@wsl:Ubuntu, claude@wsl:Ubuntu). The gateway it is pointed at is
// 127.0.0.1 when WSL shares Windows' network (mirrored networking, as the
// distro's wslinfo says, or .wslconfig where there is no wslinfo); under
// NAT it is Windows as WSL sees it, which reaches the gateway only while
// that listens beyond loopback.

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
}

func here(home string) place { return place{home: home} }

// getenv is this machine's variable for this machine's agent; "" for one
// in a distro, whose variables magpie can't read.
func (p place) getenv(k string) string {
	if p.spell != nil {
		return ""
	}
	return appdir.Getenv(k)
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
	Gateway string          `json:"gateway,omitempty"` // the Windows host as the distro reaches it, when not mirrored
	// Was are the addresses it reached Windows at before, newest last: an
	// agent's config still on one is offered the one now (#1013)
	Was    []string          `json:"was,omitempty"`
	Values map[string]string `json:"values,omitempty"` // its agents' fields as last read (wslKind.memo), shown while it is stopped
	// Net is WSL's networking mode as the distro's wslinfo said at the
	// last probe ("mirrored", "nat", "virtioproxy", "none"); "" when it
	// couldn't say (a WSL without wslinfo), and .wslconfig is read instead
	Net string `json:"net,omitempty"`
	// Versions is what the CLIs of wslKinds that ask (version) said their
	// versions are at the last probe, by the kind's id
	Versions map[string]string `json:"versions,omitempty"`
	Mirrored bool              `json:"-"`
	Running  bool              `json:"-"`
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

// mirror settles Mirrored: what wslinfo said at the probe, or cfg
// (.wslconfig's say) when it couldn't.
func (d *distro) mirror(cfg bool) {
	d.Mirrored = d.Net == "mirrored" || d.Net == "" && cfg
}

// notMirrored is the notice of an agent in a distro not known to be in
// mirrored networking: what WSL said, or that only .wslconfig was read.
func (d distro) notMirrored(agent string) string {
	why := "WSL " + d.Name + " is in " + d.Net + " networking, not mirrored"
	if d.Net == "" {
		why = "magpie couldn't ask WSL " + d.Name + " its networking mode (it has no wslinfo) and %UserProfile%\\.wslconfig doesn't set it to mirrored"
	}
	return why + ", so its " + agent + " was pointed at Windows (" + d.base() + ") rather than 127.0.0.1; that answers only while the gateway listens beyond loopback and Windows' firewall lets WSL in. " +
		"For 127.0.0.1, set networkingMode=mirrored under [wsl2] in %UserProfile%\\.wslconfig and run wsl --shutdown, then pick the model again."
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
	return place{home: d.local(d.Home), id: id, spell: d.native, sys: d.local, base: d.base, cold: !d.Running, version: d.Versions[kind]}
}

// wslKind is an agent magpie looks for in a distro: what the probe finds
// of it, and how it lives at a place. Adding one here is all it takes.
type wslKind struct {
	id, name string
	// dir, under $HOME, and bin, on its PATH: either says it is there; ""
	// for one that says nothing (a folder other tools keep too)
	dir, bin string
	in       func(place) *Agent // the agent at a place, as on this machine
	restart  string             // advice after a change, when mirrored; "" none
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
				return func(cur map[string]string) []Option {
					return append(group("Claude Code", claudeOwn(cur["model"], nil)), claudeViaMagpie(false)...)
				}
			case "effort":
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
			// a tier: magpie's models while the model last seen is one
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
		restart: "reads its config at start-up — restart open opencode sessions to use this.",
		asleep:  wslOwnAsleep("opencode", "model", "small")},
	{id: "mimocode", name: "MiMo Code", dir: ".config/mimocode", bin: "mimo", in: mimocodeIn,
		restart: "reads its config at start-up — restart open mimo sessions to use this.",
		asleep:  wslOwnAsleep("mimocode", "model", "small")},
	// Kimi Code's ~/.kimi-code, or the old kimi-cli's ~/.kimi where that is
	// all there is; a stopped distro's is looked at once it is started
	{id: "kimi", name: "Kimi Code", dir: ".kimi-code", bin: "kimi", in: kimiIn,
		restart: "reads its settings at start-up — restart open kimi sessions to use this.",
		asleep: func(key string) func(map[string]string) []Option {
			if key != "model" {
				return nil
			}
			return func(cur map[string]string) []Option {
				return append(kimiOwnOptions("", cur["model"]), viaMagpie("kimi", magpieID+"/")...)
			}
		}},
	{id: "omp", name: "omp", dir: ".omp", bin: "omp", in: ompIn, version: true,
		restart: "reads its settings at start-up — restart open omp sessions to use this.",
		asleep: func(key string) func(map[string]string) []Option {
			if key == "effort" {
				return nil
			}
			return func(cur map[string]string) []Option {
				return append(ompOwnOptions("", cur[key]), viaMagpie("omp", magpieID+"/")...)
			}
		}},
	{id: "crush", name: "Crush", dir: ".config/crush", bin: "crush", in: crushIn,
		restart: "reads its settings at start-up — restart open crush sessions to use this.",
		asleep:  wslOwnAsleep("crush", "model", "small")},
	{id: "hermes", name: "Hermes Agent", dir: ".hermes", bin: "hermes", in: hermesIn,
		restart: "reads its settings at start-up — restart open Hermes sessions to use this."},
	// no bin: morph is other tools' name too
	{id: "morph", name: "Mister Morph", dir: ".morph", in: morphIn,
		restart: "uses this for new tasks in its Console — restart open morph chats to use it there."},
	// no bin: grok is also other tools' name, as on this machine
	{id: "grok", name: "Grok Build", dir: ".grok", in: grokIn,
		restart: "reads its settings at start-up — restart open grok sessions to use this."},
	{id: "droid", name: "Droid", dir: ".factory", bin: "droid", in: droidIn,
		restart: "reads its settings at start-up — restart open droid sessions to use this.",
		asleep: func(key string) func(map[string]string) []Option {
			if key != "model" {
				return nil
			}
			return func(cur map[string]string) []Option {
				var own []Option
				if c := cur["model"]; c != "" && !usesMagpie(c) {
					own = append(own, Option{Value: c, Icon: modelIcon("", c)})
				}
				return append(group("Droid", own), viaMagpie("droid", magpieID+"/")...)
			}
		}},
	// no bin: fx is also the JSON viewer's name
	{id: "fx", name: "fx", dir: ".fx", in: fxIn,
		restart: "reads its settings at start-up — restart open fx sessions to use this."},
	{id: "commandcode", name: "Command Code", dir: ".commandcode", bin: "command-code", in: commandCodeIn,
		restart: "wants its own sign-in there (cmd login) even for models through magpie, and reads its settings at start-up — restart open Command Code sessions to use this."},
	{id: "minimax-code", name: "MiniMax Code", dir: ".minimax", bin: "mcode", in: miniMaxIn,
		restart: "reads its settings at start-up — restart open mcode sessions to use this.",
		asleep: func(key string) func(map[string]string) []Option {
			if key != "model" {
				return nil
			}
			return func(cur map[string]string) []Option {
				return append(miniMaxOwnOptions("", cur["model"]), viaMagpie("minimax-code", magpieID+"/")...)
			}
		}},
	{id: "dsh", name: "DeepSeek Harness", dir: ".dsh", bin: "dsh", in: dshIn,
		restart: "reads its config at start-up — restart open dsh sessions to use this.",
		asleep: func(key string) func(map[string]string) []Option {
			// the thinking levels hang on which patch lists there are, the
			// distro's files: a dsh of today's (0.1.5 on) is taken
			if key != "effort" {
				return nil
			}
			return func(cur map[string]string) []Option {
				if ref, ok := strings.CutPrefix(cur["model"], magpieID+"/"); ok {
					return static(dshLevels(ref)...)
				}
				return static(dshEfforts...)
			}
		}},
	{id: "empryo", name: "Empryo", dir: ".empryo", bin: "empryo", in: empryoIn,
		restart: "reads its config at start-up — restart open empryo sessions to use this."},
	{id: "muse", name: "Muse Code", dir: ".config/muse", bin: "muse", in: museIn,
		restart: "reads its settings at start-up — restart open muse sessions to use this."},
	{id: "qoder", name: "Qoder", dir: ".qoder", bin: "qodercli", in: qoderIn,
		restart: "reads its settings as a session starts — open sessions keep the model they have; new ones use this."},
	{id: "qoder-cn", name: "Qoder CN", dir: ".qoder-cn", bin: "qoderclicn", in: qoderCNIn,
		restart: "reads its settings as a session starts — open sessions keep the model they have; new ones use this."},
	// lgtm on Discord: magpie didn't find a CodeBuddy Code installed in WSL
	{id: "codebuddy", name: "CodeBuddy Code", dir: ".codebuddy", bin: "codebuddy", in: codebuddyIn,
		restart: "reads its model when a session starts — restart open codebuddy sessions, or run /clear in them, to use this."},
	// Cline's CLI, and its VS Code extension in a Remote - WSL window, which
	// keep the same ~/.cline
	{id: "cline", name: "Cline", dir: ".cline", bin: "cline", in: clineIn,
		restart: "reads its provider as a session starts — open sessions keep the model they have; new ones use this. Reload VS Code's window for its extension.",
		asleep:  wslOwnAsleep("cline", "model")},
	{id: "atomcode", name: "AtomCode", dir: ".atomcode", bin: "atomcode", in: atomcodeIn,
		restart: "reads its settings at start-up — restart open atomcode sessions to use this.",
		asleep: func(key string) func(map[string]string) []Option {
			if key != "model" {
				return nil
			}
			// the models of its config.toml are the distro's files
			return func(cur map[string]string) []Option {
				return append(atomcodeOwnModels("", cur["model"]), viaMagpie("atomcode", magpieID+"/")...)
			}
		}},
	// no dir: Antigravity keeps its folders in ~/.gemini too, so only the
	// command says Gemini CLI is there
	{id: "gemini", name: "Gemini CLI", bin: "gemini", in: geminiIn,
		restart: "reads its settings at start-up — restart open gemini sessions to see this.",
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
	// Block's goose, by its folder alone: a goose command may be pressly's
	// database migration tool
	{id: "goose", name: "Goose", dir: ".config/goose", in: func(at place) *Agent { return gooseIn(at, filepath.Join(at.home, ".config")) },
		restart: "loads its providers at start-up — restart open goose sessions to use magpie's models.",
		asleep:  wslOwnAsleep("goose", "model")},
	// Reasonix's native CLI (1.39 on), by its command and the version it
	// gives: the historical npm client is reasonix too, and ~/.reasonix is
	// both's
	{id: "reasonix", name: "Reasonix", bin: "reasonix", in: reasonixIn, version: true,
		accept: func(d distro) bool {
			v := d.Versions["reasonix"]
			return strings.HasPrefix(v, "2.") || strings.HasPrefix(v, "1.39.")
		},
		restart: "reads its settings at start-up — start a new reasonix process to use this.",
		asleep: func(key string) func(map[string]string) []Option {
			switch key {
			case "model", "planner":
				return func(cur map[string]string) []Option {
					out := append(ownOptions("", cur[key]), viaMagpie("reasonix", magpieID+"/")...)
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
			return append(ownOptions("", cur[key]), viaMagpie(id, magpieID+"/")...)
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

// ListsFor is whose model lists an agent's are (provider.CatalogFor): its
// own id's, or, for one in a WSL distro, its Windows twin's (claude for
// claude@wsl:Ubuntu). A distro's agent is written magpie's models under
// that id and sends the gateway that id's key, so the models picked for
// its row are the ones it is shown only when kept under that id too (#927).
func (a *Agent) ListsFor() string {
	id, _, _ := strings.Cut(a.ID, "@wsl:")
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
	a := k.in(d.place(id))
	a.ID, a.Name, a.Aliases, a.Bin, a.UA, a.WSL = id, k.name+" · WSL "+d.Name, nil, "", nil, d.Name
	a.Gateway = d.base
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
	for _, p := range []*func() bool{&a.Routed, &a.Joined, &a.Beside, &a.Added} {
		if f := *p; f != nil {
			*p = func() bool { return d.live() && f() }
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

// asleep is a stopped distro's agent: its fields read what magpie last
// saw, their options come from magpie alone, and it has no files to check,
// sync or migrate (Path and Dir are empty); setting a field goes to the
// files, which starts the distro.
func asleep(live *Agent, k wslKind, d distro) *Agent {
	started := false
	a := &Agent{ID: live.ID, Name: live.Name, Icon: live.Icon, WSL: d.Name, detect: live.detect, Spelled: live.Spelled,
		Notice: func() string {
			if started {
				return live.Notice()
			}
			return "WSL " + d.Name + " isn't running: magpie shows what it last saw there, and starts it only to change something."
		}}
	for _, lf := range live.Fields {
		key := lf.Key
		f := Field{Key: key, Label: lf.Label, Quiet: lf.Quiet, Follows: lf.Follows, Options: lf.Options,
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

// wslHomes are the distros agents were found in, each user's home as magpie
// opens it, for internal/sessions to read their sessions in; none off
// Windows. A stopped distro's is its home as last probed.
func wslHomes() []sessions.WSLHome {
	if !wslOn {
		return nil
	}
	var out []sessions.WSLHome
	for _, d := range wslDistros() {
		if d.Root == "" || d.Home == "" {
			continue
		}
		out = append(out, sessions.WSLHome{Distro: d.Name, Home: d.local(d.Home), Running: d.Running})
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
	runAt   time.Time       // when running was listed
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
		wsl.at = time.Now()
	}
	wslRunningLocked()
	installed := map[string]bool{}
	mirrored := wslMirrored(wslConfig())
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
					if wslFound(*d) {
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
		c.mirror(mirrored)
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
		if !wslFound(*d) {
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
	if !wsl.runAt.IsZero() && time.Since(wsl.runAt) <= wslRunningAge {
		return
	}
	run, _ := wslList("-l", "--running", "-q")
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
func WSLRunning(distro string) bool { return wslOn && wslUp(distro) }

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
// through magpie: the first, of the distros running now, whose
// settings.json has a stand-in. Only magpie's memory of them and which run
// (wslRunningLocked) are asked — nothing is probed, and a stopped distro
// is never opened.
func wslClaudeStandIn(model string) string {
	wsl.Lock()
	wslRunningLocked()
	var ds []distro
	for _, n := range wsl.names {
		if d := wsl.seen[n]; d != nil && wsl.running[n] && wslKindOf("claude").found(*d) {
			ds = append(ds, *d)
		}
	}
	wsl.Unlock()
	mirrored := wslMirrored(wslConfig())
	for _, d := range ds {
		d.mirror(mirrored)
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
var wslProbeScript = func() string {
	s := `wslbin() { for d in "$HOME/.bun/bin" "$HOME/.local/bin" "$HOME/.npm-global/bin" "$HOME/.volta/bin" "$HOME/.local/share/pnpm" "$HOME"/.nvm/versions/node/*/bin "$HOME/.local/share/mise/shims" "$HOME/.asdf/shims"; do [ -x "$d/$1" ] && { echo "$d/$1"; return 0; }; done; return 1; }; `
	s += `echo "home:$HOME"; `
	for _, k := range wslKinds {
		if k.dir != "" {
			s += `[ -d "$HOME/` + k.dir + `" ] && echo dir:` + k.dir + `; `
		}
		if k.bin != "" {
			s += `p=$(command -v ` + k.bin + ` 2>/dev/null || wslbin ` + k.bin + `) && echo "bin:` + k.bin + ` $p"`
			if k.version {
				// not one on Windows' drives (WSL's /mnt/c), which isn't the distro's
				s += ` && case "$p" in /mnt/*) ;; *) echo "ver:` + k.id + ` $(PATH="${p%/*}:$PATH" timeout 10 "$p" --version </dev/null 2>&1 | head -n1)";; esac`
			}
			s += `; `
		}
	}
	// a drive's source in /proc/mounts is C:\ (written C:\134), under any automount root
	s += `awk '$1 ~ /^[A-Za-z]:/ {print "win:" $2}' /proc/mounts 2>/dev/null; `
	return s + `ip route show default 2>/dev/null | head -n1 | sed 's/^/route:/'; ` +
		`grep -m1 '^nameserver' /etc/resolv.conf 2>/dev/null | sed 's/^/ns:/'; ` +
		`command -v wslinfo >/dev/null 2>&1 && echo "net:$(wslinfo --networking-mode 2>/dev/null)"; true`
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
		case "net":
			// one word; anything else (an old wslinfo's usage) says nothing
			if m := strings.ToLower(strings.TrimSpace(v)); m != "" && !strings.ContainsAny(m, " \t") {
				d.Net = m
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
	section, mirrored := "", false
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
