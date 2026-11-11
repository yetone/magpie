package agent

// Kiro Crew (kirodotdev/KiroCrew, v0.9.0: the KiroCrew.app desktop app, the
// kirocrew CLI, its web dashboard and the Gateway they share; Vincent on
// Discord) is a persistent agent that runs each session on a harness: an
// ACP agent it starts and talks to. It has no model provider of its own
// to point at magpie; its models are its harness's. So what magpie sets is
// which harness it runs: one already connected to magpie, whose own config
// then sends its requests to the gateway.
//
// Its settings are one JSON file in its home, $KIROCREW_HOME (made
// absolute; a root, /usr, /System or /etc is refused and the default used)
// or ~/.kiro/crew, the desktop app's too (config/paths.py config_dir,
// _valid_override_home). config.local.json beside it is merged over it
// key by key and wins (config/loader.py). The keys (config/sections.py):
//
//	{"agent": {"acp_backend": "codex",          // "" is kiro-cli, the default
//	           "member_acp_backend": "codex"}}  // crew members', default "kas"
//
// acp_backend is one of "", kas, claude, codex, opencode, pi, goose and
// deepseek; member_acp_backend one that can dispatch (claude, kas, codex,
// opencode, goose: ACP_BACKENDS_MEMBER_DISPATCH in agent_sdk/backends.py).
// The Gateway reloads the files when they change; a session already open
// keeps its harness.
//
// The harnesses magpie offers are those it wires itself, through the files
// the harness reads under Crew too: Codex (codex-acp reads ~/.codex, or
// CODEX_HOME), Claude Code (claude-agent-acp runs the user's claude, which
// reads ~/.claude), OpenCode (opencode acp), Pi (pi-acp runs pi) and Goose
// (goose acp). Not deepseek: Crew runs it as dsh --profile acp, a profile
// of its own that magpie's dsh wiring doesn't reach. Codex, Claude Code
// and Pi are reached through an npm adapter Crew doesn't ship
// (agent_sdk/backend_install.py); magpie looks for it where Crew does and
// says how to install it when it isn't there, and installs nothing.
//
// Connecting keeps the harness Kiro Crew is set to when it is one of
// these, and connects that agent to magpie too when it isn't yet (Crew's
// first run picks one, Claude Code on a Windows without kiro-cli: wuping9525
// on Discord had to connect it by hand first). Disconnect takes that agent
// off magpie again only when this connect is what put it on, and the user
// hasn't disconnected it since. On a harness magpie doesn't wire (KAS,
// kiro-cli, deepseek) it picks the first harness connected to magpie
// (Codex, Claude Code, OpenCode, Pi, Goose), one whose adapter is here
// first. It keeps what the two keys said before, which Disconnect puts
// back. A harness set in config.local.json is the user's: magpie writes
// none over it.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
)

// kiroCrewOS is the system Kiro Crew's files are looked for as; a var so
// a test on a Mac can say Windows.
var kiroCrewOS = runtime.GOOS

// kiroCrewApp is Kiro Crew's macOS bundle.
var kiroCrewApp = desktopApp{id: "com.amazon.kiro.crew", names: []string{"KiroCrew.app", "Kiro Crew.app"}}

// kiroCrewHarness is one harness magpie can set: Crew's backend id, the
// magpie agent that is it, and the adapter Crew runs it through, with the
// variable that names the adapter's path and the npm package it comes in
// ("" for one that is the agent's own binary).
type kiroCrewHarness struct {
	id, agent, name, icon string
	adapter, env          string
	npm                   string
	dispatch              bool // can run crew members (member_acp_backend)
}

// kiroCrewHarnesses are the harnesses magpie offers, in the order it picks
// one by itself.
var kiroCrewHarnesses = []kiroCrewHarness{
	{id: "codex", agent: "codex", name: "Codex", icon: "codex-color", adapter: "codex-acp", env: "CODEX_ACP_BIN", npm: "@agentclientprotocol/codex-acp", dispatch: true},
	{id: "claude", agent: "claude", name: "Claude Code", icon: "claudecode-color", adapter: "claude-agent-acp", env: "CLAUDE_AGENT_ACP_BIN", npm: "@agentclientprotocol/claude-agent-acp", dispatch: true},
	{id: "opencode", agent: "opencode", name: "OpenCode", icon: "opencode", dispatch: true},
	{id: "pi", agent: "pi", name: "Pi", icon: "pi", adapter: "pi-acp", env: "PI_ACP_BIN", npm: "pi-acp"},
	{id: "goose", agent: "goose", name: "Goose", icon: "goose", dispatch: true},
}

func kiroCrewHarnessOf(id string) (kiroCrewHarness, bool) {
	for _, h := range kiroCrewHarnesses {
		if h.id == id {
			return h, true
		}
	}
	return kiroCrewHarness{}, false
}

const (
	kiroCrewBackend = "agent.acp_backend"
	kiroCrewMember  = "agent.member_acp_backend"
	// the stash: what the two keys said before magpie set them, and what
	// magpie wrote (there while magpie has set the harness)
	kiroCrewWasKey   = "kirocrew.was"
	kiroCrewWroteKey = "kirocrew.wrote"
	// the harness agent magpie connected along with Kiro Crew, for
	// Disconnect to take off again (kiroCrewJoin)
	kiroCrewJoinedKey = "kirocrew.joined"
)

var (
	noticeKiroCrew        = newNotice("Kiro Crew takes the new harness for new sessions; sessions already open keep theirs.")
	noticeKiroCrewAdapter = newNotice("Kiro Crew runs {harness} through {adapter}, which magpie didn't find here: install it with {install}, then start a new session.")
	noticeKiroCrewPinned  = newNotice("Kiro Crew's config.local.json sets its harness (agent.acp_backend), and that wins over config.json, so magpie leaves it alone: set it there to a harness connected to magpie.")
	noticeKiroCrewJoined  = newNotice("Kiro Crew runs on {harness}, so magpie connected {harness} too; turning Kiro Crew off disconnects it again.")
)

// the provider's note: how magpie is its provider, or that its harness is
// the user's in config.local.json
const (
	kiroCrewNote       = "its harness, an agent connected to magpie, runs on magpie's models"
	kiroCrewPinnedNote = "its harness is set in config.local.json, which magpie leaves alone"
)

var (
	errKiroCrewNoHarness = errors.New("Kiro Crew runs on a harness: connect Codex, Claude Code, OpenCode, Pi or Goose to magpie first, then connect Kiro Crew")
	errKiroCrewPinned    = errors.New("Kiro Crew's config.local.json sets its harness, and that wins over config.json: magpie leaves it alone. Change agent.acp_backend there, or take it out, then connect again")
	errKiroCrewHarness   = errors.New("that harness isn't connected to magpie: connect it first")
)

// kiroCrewAgent is the magpie agent of a harness, nil for none.
func kiroCrewAgent(id string) *Agent {
	home, cfg := homes()
	switch id {
	case "codex":
		return codex(home)
	case "claude":
		return claude(home)
	case "opencode":
		return opencode(home, cfg)
	case "pi":
		return pi(home)
	case "goose":
		return goose(home, cfg)
	}
	return nil
}

// kiroCrewWired says whether the magpie agent id is connected to magpie;
// a var so tests can say.
var kiroCrewWired = func(id string) bool {
	a := kiroCrewAgent(id)
	return a != nil && a.Wired()
}

// kiroCrewJoined is the harness agent magpie connected along with Kiro
// Crew, and whether the user had disconnected it before that.
type kiroCrewJoined struct {
	Agent string `json:"agent"`
	Off   bool   `json:"off,omitempty"`
}

func kiroCrewJoinedNow() (kiroCrewJoined, bool) {
	var j kiroCrewJoined
	s := stashShared()[kiroCrewJoinedKey]
	return j, s != "" && json.Unmarshal([]byte(s), &j) == nil && j.Agent != ""
}

// kiroCrewJoin connects the harness agent id to magpie for Kiro Crew, and
// says whether it could: false, with no error, for one not installed here.
var kiroCrewJoin = func(id string) (bool, error) {
	a := kiroCrewAgent(id)
	if a == nil || !a.Detected() {
		return false, nil
	}
	off := a.Disconnected()
	if _, err := a.ConnectHow(); err != nil {
		return false, err
	}
	b, _ := json.Marshal(kiroCrewJoined{Agent: id, Off: off})
	stash(map[string]string{kiroCrewJoinedKey: string(b)})
	return true, nil
}

// kiroCrewLeave takes the harness agent magpie connected along with Kiro
// Crew off magpie again, the user's own disconnect of it, from before,
// kept as it was.
var kiroCrewLeave = func() error {
	j, ok := kiroCrewJoinedNow()
	forget(kiroCrewJoinedKey)
	if !ok {
		return nil
	}
	a := kiroCrewAgent(j.Agent)
	if a == nil || !a.Wired() {
		return nil
	}
	if err := a.Disconnect(); err != nil {
		return err
	}
	if !j.Off {
		forget(a.ID + disconnectedKey)
	}
	return nil
}

// kiroCrewUserLeft forgets that magpie connected the agent id along with
// Kiro Crew once the user disconnects it themselves: connected again
// after, it is theirs, and Kiro Crew's Disconnect leaves it on.
func kiroCrewUserLeft(id string) {
	if j, ok := kiroCrewJoinedNow(); ok && j.Agent == id {
		forget(kiroCrewJoinedKey)
	}
}

// kiroCrewHasAdapter says whether Crew would find harness h's adapter; a
// var so tests can say.
var kiroCrewHasAdapter = func(h kiroCrewHarness) bool {
	home, _ := os.UserHomeDir()
	return kiroCrewFinds(h, home, appdir.Getenv)
}

// kiroCrewFinds looks for h's adapter where Crew looks for it
// (acp/launch.py, env.py augmented_path): the path its variable names,
// magpie's PATH, the folders Crew adds to its own (~/.local/bin, npm's,
// mise's shims, Volta's, Homebrew's), and every node version mise, asdf,
// nvm and fnm keep. A harness with no adapter is its own binary.
func kiroCrewFinds(h kiroCrewHarness, home string, getenv func(string) string) bool {
	bin := h.adapter
	if bin == "" {
		bin = h.agent
	}
	if h.env != "" {
		if p := getenv(h.env); p != "" && isFile(p) {
			return true
		}
	}
	if binAt(bin) != "" {
		return true
	}
	mise := getenv("MISE_DATA_DIR")
	if mise == "" {
		if x := getenv("XDG_DATA_HOME"); x != "" {
			mise = filepath.Join(x, "mise")
		} else {
			mise = filepath.Join(home, ".local", "share", "mise")
		}
	}
	dirs := []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".npm-packages", "bin"),
		filepath.Join(home, ".npm-global", "bin"),
		filepath.Join(mise, "shims"),
		filepath.Join(home, ".volta", "bin"),
		filepath.Join(home, ".bun", "bin"),
	}
	if d := getenv("BUN_INSTALL"); filepath.IsAbs(d) {
		dirs = append(dirs, filepath.Join(d, "bin"))
	}
	if kiroCrewOS == "windows" {
		appdata := getenv("APPDATA")
		if appdata == "" {
			appdata = filepath.Join(home, "AppData", "Roaming")
		}
		dirs = append(dirs, filepath.Join(appdata, "npm"))
	}
	if kiroCrewOS != "windows" {
		dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
	}
	or := func(v, d string) string {
		if p := getenv(v); p != "" {
			return p
		}
		return d
	}
	for _, g := range []string{
		filepath.Join(mise, "installs", "node", "*", "bin"),
		filepath.Join(home, ".asdf", "installs", "nodejs", "*", "bin"),
		filepath.Join(or("NVM_DIR", filepath.Join(home, ".nvm")), "versions", "node", "*", "bin"),
		filepath.Join(or("FNM_DIR", filepath.Join(home, ".local", "share", "fnm")), "node-versions", "*", "installation", "bin"),
		filepath.Join(home, "Library", "Application Support", "fnm", "node-versions", "*", "installation", "bin"),
	} {
		m, _ := filepath.Glob(g)
		dirs = append(dirs, m...)
	}
	// on Windows npm's shim is the name with .cmd, as Crew's shutil.which
	// finds it through PATHEXT
	names := []string{bin}
	if kiroCrewOS == "windows" {
		names = []string{bin + ".cmd", bin + ".exe", bin + ".bat", bin}
	}
	for _, d := range dirs {
		for _, n := range names {
			if isFile(filepath.Join(d, n)) {
				return true
			}
		}
	}
	return false
}

// kiroCrewHome is Kiro Crew's home, as config_dir finds it.
func kiroCrewHome(home string, getenv func(string) string) string {
	if o := getenv("KIROCREW_HOME"); o != "" {
		if strings.HasPrefix(o, "~") && (len(o) == 1 || o[1] == '/' || o[1] == filepath.Separator) {
			o = filepath.Join(home, o[1:])
		}
		if p, err := filepath.Abs(o); err == nil {
			if r, err := filepath.EvalSymlinks(p); err == nil {
				p = r
			}
			if !kiroCrewUnsafe(p) {
				return p
			}
		}
	}
	return filepath.Join(home, ".kiro", "crew")
}

// kiroCrewUnsafe is a home Kiro Crew refuses (_is_unsafe_home): a root, or
// a folder in /usr, /System or /etc (/private/etc, as a Mac resolves it).
func kiroCrewUnsafe(p string) bool {
	p = filepath.Clean(p)
	if filepath.Dir(p) == p {
		return true
	}
	if kiroCrewOS == "windows" {
		return false
	}
	for _, d := range []string{"/usr", "/System", "/etc", "/private/etc"} {
		if p == d || strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}

// kiroCrewWas is what a key said before magpie set it: whether it was
// there, and its JSON as written.
type kiroCrewWas struct {
	Set bool            `json:"set"`
	Raw json.RawMessage `json:"raw,omitempty"`
}

type kiroCrewStash struct {
	Backend kiroCrewWas `json:"backend"`
	Member  kiroCrewWas `json:"member"`
	Made    bool        `json:"made,omitempty"` // magpie made config.json
}

// kiroCrewWrote is what magpie last wrote in the two keys ("" for a key it
// left alone).
type kiroCrewWrote struct {
	Backend string `json:"backend"`
	Member  string `json:"member,omitempty"`
}

func kiroCrewWroteNow() (kiroCrewWrote, bool) {
	var w kiroCrewWrote
	s := stashShared()[kiroCrewWroteKey]
	return w, s != "" && json.Unmarshal([]byte(s), &w) == nil && w.Backend != ""
}

func kiroCrewWasOf(path, key string) kiroCrewWas {
	raw, ok := edit.GetJSONRaw(path, key)
	if !ok {
		return kiroCrewWas{}
	}
	return kiroCrewWas{Set: true, Raw: json.RawMessage(raw)}
}

func kiroCrew(home string) *Agent {
	dir := kiroCrewHome(home, appdir.Getenv)
	path := filepath.Join(dir, "config.json")
	local := filepath.Join(dir, "config.local.json")
	// the harness it runs: config.local.json's, else config.json's
	backend := func() string {
		if v, ok := edit.GetJSON(local, kiroCrewBackend); ok {
			return v
		}
		v, _ := edit.GetJSON(path, kiroCrewBackend)
		return v
	}
	pinned := func() bool {
		_, ok := edit.GetJSON(local, kiroCrewBackend)
		return ok
	}
	// connected: magpie set the harness, and the one it runs is connected
	// to magpie
	on := func() string {
		if _, ok := kiroCrewWroteNow(); !ok {
			return ""
		}
		h, ok := kiroCrewHarnessOf(backend())
		if !ok || !kiroCrewWired(h.agent) {
			return ""
		}
		return h.id
	}
	connected := func() []kiroCrewHarness {
		var out []kiroCrewHarness
		for _, h := range kiroCrewHarnesses {
			if kiroCrewWired(h.agent) {
				out = append(out, h)
			}
		}
		return out
	}
	// write sets the harness to h, the keys' values before kept the first
	// time
	write := func(h kiroCrewHarness) error {
		if pinned() {
			return errKiroCrewPinned
		}
		cur, _ := edit.GetJSON(path, kiroCrewBackend)
		w, had := kiroCrewWroteNow()
		// a stash from a harness the user has since changed to one of their
		// own is stale: what is there now is theirs
		if had {
			if _, ours := kiroCrewHarnessOf(cur); !ours {
				had = false
			}
		}
		if !had {
			s := kiroCrewStash{Backend: kiroCrewWasOf(path, kiroCrewBackend), Member: kiroCrewWasOf(path, kiroCrewMember), Made: !isFile(path)}
			b, _ := json.Marshal(s)
			stash(map[string]string{kiroCrewWasKey: string(b)})
			w = kiroCrewWrote{}
		}
		kvs := []edit.KV{{Path: kiroCrewBackend, Value: h.id}}
		w.Backend = h.id
		if h.dispatch {
			kvs = append(kvs, edit.KV{Path: kiroCrewMember, Value: h.id})
			w.Member = h.id
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := edit.SetJSON(path, kvs...); err != nil {
			return err
		}
		b, _ := json.Marshal(w)
		stash(map[string]string{kiroCrewWroteKey: string(b)})
		return nil
	}
	// unwire puts back what the keys said before magpie set them, a key the
	// user has changed since left as they set it
	unwire := func() error {
		w, ok := kiroCrewWroteNow()
		if !ok {
			return nil
		}
		var s kiroCrewStash
		json.Unmarshal([]byte(stashShared()[kiroCrewWasKey]), &s)
		var set []edit.KV
		var del []string
		put := func(key string, was kiroCrewWas, ours bool) {
			if !ours {
				return
			}
			if was.Set {
				set = append(set, edit.KV{Path: key, Value: was.Raw})
			} else {
				del = append(del, key)
			}
		}
		cur, _ := edit.GetJSON(path, kiroCrewBackend)
		_, ours := kiroCrewHarnessOf(cur)
		put(kiroCrewBackend, s.Backend, ours)
		if w.Member != "" {
			m, _ := edit.GetJSON(path, kiroCrewMember)
			put(kiroCrewMember, s.Member, m == w.Member)
		}
		if isFile(path) {
			if len(set) > 0 {
				if err := edit.SetJSON(path, set...); err != nil {
					return err
				}
			}
			if len(del) > 0 {
				if err := edit.DelJSON(path, del...); err != nil {
					return err
				}
			}
			// a file magpie made that holds nothing else now
			if s.Made {
				if raw, ok := edit.GetJSONRaw(path, "agent"); ok && strings.TrimSpace(strings.Join(strings.Fields(raw), "")) == "{}" {
					if err := edit.DelJSON(path, "agent"); err != nil {
						return err
					}
				}
				if b, err := edit.Read(path); err == nil && strings.Join(strings.Fields(string(b)), "") == "{}" {
					os.Remove(path)
				}
			}
		}
		forget(kiroCrewWasKey, kiroCrewWroteKey)
		return kiroCrewLeave()
	}
	// pick is the harness to run: the one Kiro Crew is set to, when it is
	// one of magpie's, connected to magpie now if it isn't yet (joined);
	// else the first connected to magpie
	pick := func() (h kiroCrewHarness, joined bool, err error) {
		if h, ok := kiroCrewHarnessOf(backend()); ok {
			if kiroCrewWired(h.agent) {
				return h, false, nil
			}
			ok, err := kiroCrewJoin(h.agent)
			if err != nil {
				return h, false, err
			}
			if ok {
				return h, true, nil
			}
		}
		hs := connected()
		if len(hs) == 0 {
			return kiroCrewHarness{}, false, errKiroCrewNoHarness
		}
		for _, h := range hs {
			if kiroCrewHasAdapter(h) {
				return h, false, nil
			}
		}
		return hs[0], false, nil
	}
	return atomic(&Agent{
		ID: "kirocrew", Name: "Kiro Crew", Icon: "generic", Aliases: []string{"kiro-crew", "crew"},
		Dir: dir, Path: path,
		// its app, its command, or its home with its settings in it; never
		// ~/.kiro alone, which is Kiro's and kiro-cli's
		detect: func() bool {
			return isFile(path) || macBundle(kiroCrewApp) || binAt("kirocrew") != "" ||
				isFile(filepath.Join(home, ".kirocrew-app", ".venv", "bin", "kirocrew")) ||
				kiroCrewOS == "windows" && isFile(filepath.Join(home, ".kirocrew-app", ".venv", "Scripts", "kirocrew.exe"))
		},
		Notice: func() string {
			id := on()
			if id == "" {
				if pinned() {
					return noticeKiroCrewPinned.String()
				}
				return ""
			}
			notes := []string{noticeKiroCrew.String()}
			if j, ok := kiroCrewJoinedNow(); ok {
				if h, ok := kiroCrewHarnessOf(id); ok && h.agent == j.Agent {
					notes = append(notes, noticeKiroCrewJoined.say("harness", h.name))
				}
			}
			if h, _ := kiroCrewHarnessOf(id); h.adapter != "" && !kiroCrewHasAdapter(h) {
				notes = append(notes, noticeKiroCrewAdapter.say("harness", h.name, "adapter", h.adapter, "install", "npm i -g "+h.npm))
			}
			return strings.Join(notes, " ")
		},
		Fields: []Field{{
			Key: "provider", Label: "provider",
			Get: func() string {
				if on() != "" {
					return magpieID
				}
				return ""
			},
			Set: func(v string) error {
				if v == "" {
					return unwire()
				}
				if pinned() {
					return errKiroCrewPinned
				}
				// on a harness of magpie's already: it stays
				if id := on(); id != "" {
					return nil
				}
				h, joined, err := pick()
				if err != nil {
					return err
				}
				if err := write(h); err != nil {
					if joined {
						kiroCrewLeave()
					}
					return err
				}
				return nil
			},
			Options: func(map[string]string) []Option {
				note := kiroCrewNote
				if pinned() && on() == "" {
					note = kiroCrewPinnedNote
				}
				return []Option{{Value: magpieID, Label: "magpie", Icon: "magpie", Note: note}}
			},
		}, {
			Key: "harness", Label: "harness", Quiet: true,
			Get: on,
			Set: func(v string) error {
				if v == "" {
					return unwire()
				}
				h, ok := kiroCrewHarnessOf(v)
				if !ok || !kiroCrewWired(h.agent) {
					return errKiroCrewHarness
				}
				if on() == v {
					return nil
				}
				return write(h)
			},
			Options: func(map[string]string) []Option {
				var out []Option
				for _, h := range connected() {
					o := Option{Value: h.id, Label: h.name, Icon: h.icon}
					if h.adapter != "" && !kiroCrewHasAdapter(h) {
						o.Note = "npm i -g " + h.npm
					}
					out = append(out, o)
				}
				return out
			},
		}},
	}, path)
}
