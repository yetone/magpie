// Package agent describes every coding agent magpie can drive: where its config
// lives, which fields matter (model, effort, …) and which values to offer.
package agent

import (
	"debug/buildinfo"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
)

// Option is one value the picker offers for a field.
type Option struct {
	Value string   `json:"value"`
	Label string   `json:"label,omitempty"` // display name, when the value is an id
	Note  string   `json:"note"`
	Icon  string   `json:"icon,omitempty"`  // bundled icon name
	Icons []string `json:"icons,omitempty"` // a routing group's providers' icons, stacked
	Group string   `json:"group,omitempty"` // section header in the picker
	// GroupIcon is the group's own logo in the picker's rail, when it is
	// not the first model's (a provider serving other vendors' models)
	GroupIcon string `json:"groupIcon,omitempty"`
	Ref       string `json:"ref,omitempty"`  // the catalog model, the same in every agent
	Free      bool   `json:"free,omitempty"` // costs its subscription nothing
	// Rate and RateWas are the credits a request costs its subscription,
	// as a multiple, and before a discount running now (Qoder's 0.5×,
	// WorkBuddy's x0.03), when its vendor lists them
	Rate    float64 `json:"rate,omitempty"`
	RateWas float64 `json:"rateWas,omitempty"`
	// Context is the tokens the model takes, when known; the picker marks
	// the large ones
	Context int `json:"context,omitempty"`
	// Takes, on an effort field's default (Value ""), is the level the
	// agent takes for the model with none set (Codex: the catalog entry's
	// default_reasoning_level), shown beside "default"
	Takes string `json:"takes,omitempty"`
	// Direct names who the agent asks for this model itself, on its own
	// sign-in or key, with magpie not in the way ("Anthropic"): its config
	// then names no magpie endpoint, which is right, not a failed setup
	Direct string `json:"direct,omitempty"`
	// Via is a model of the agent's own it asks through magpie's gateway,
	// on its own sign-in all the same (Codex's while routed by its base
	// URL): the picker says via magpie by it, as by a catalog model's
	Via bool `json:"via,omitempty"`
	// Same is a model magpie serves on the very account the agent is
	// signed in to itself, so the agent reaches it on its own too: the
	// picker folds these into one row a click opens (Claude Code, #496)
	Same bool `json:"same,omitempty"`
	// Alias is the value of the option a dated id is another name of
	// (claude-opus-4-5-20251101 → claude-opus-4-5): the picker shows one
	// row for the two, the alias, unless the dated one is the value set
	Alias string `json:"alias,omitempty"`
	// FastFor is the agent a catalog model with a fast mode (provider.
	// CanFast) is switched fast or not for, by the model in the picker
	// (#954): the one whose requests the gateway sends it on; Fast is
	// whether it is now (provider.IsFastPick)
	FastFor string `json:"fastFor,omitempty"`
	Fast    bool   `json:"fast,omitempty"`
	// OwnFast: its fast mode is a GPT model's on a provider the user added
	// by its address (provider.OwnFast, #1615), asked for there as OpenAI's
	// priority, which the provider may or may not serve
	OwnFast bool `json:"ownFast,omitempty"`
	// UltraOK: an account offers the model as Ultrafast (provider.CanUltra,
	// #1954), so the fast switch has a third setting; Ultra is whether it
	// is on (provider.IsUltraPick)
	UltraOK bool `json:"ultraOk,omitempty"`
	Ultra   bool `json:"ultra,omitempty"`

	// own: served on the agent's own sign-in (viaMagpie), for Same
	own bool
	// sub: served on a subscription signed in in magpie, not a key; a
	// Claude account only for Claude Code, which alone may use it safely
	sub bool
}

// Field is one tunable setting of an agent. Set with an empty value puts
// the field back to the agent's own default: anything magpie wired in (the
// gateway as a provider, its model catalog) comes out and the key is removed.
type Field struct {
	Key     string
	Label   string
	Get     func() string
	Set     func(string) error
	Options func(cur map[string]string) []Option
	// Quiet fields are left out of listings while empty: they follow
	// another field until set (Claude Code's per-tier models).
	Quiet bool
	// Live fields are magpie's own settings, read by the gateway on each
	// request (Claude Desktop's subagent model): a change to one needs no
	// restart of the agent, so its Notice isn't shown after it, unless
	// the pick connected the agent to magpie as well.
	Live bool
	// Follows is the key of the field a Quiet one takes after while empty
	// ("model" for Claude Code's tiers and subagents): a profile's details
	// say so, and Drift reads the field as on that one's model (#1050).
	Follows string
}

// ModelChain is an agent's primary model and ordered fallback models.
type ModelChain struct {
	Primary   string   `json:"primary"`
	Fallbacks []string `json:"fallbacks"`
}

// Agent is one supported coding agent.
type Agent struct {
	ID      string
	Name    string
	Icon    string // bundled icon name (internal/gui/assets/icons)
	Aliases []string
	Bin     string // executable name, used for detection
	Dir     string // config directory, used for detection
	Path    string // config file magpie edits
	// Plugin is the spec of the plugin that adds this agent (plugged.go),
	// "" for magpie's own
	Plugin string
	// ReadModelChain and SetModelChain edit the whole sequence atomically.
	// Nil means the agent has no configurable fallback sequence.
	ReadModelChain func() (ModelChain, error)
	SetModelChain  func(ModelChain) error
	Fields         []Field
	Native         *NativeConnection
	// Notice, if set, is advice worth showing after a change: agents that
	// read their config once at start-up need a restart to see it.
	Notice func() string
	// UA is what the agent's User-Agent begins with, lower-case: how the
	// gateway tells its requests from others'
	UA []string
	// ListsModels: the agent's own model menu is magpie's models as picked
	// on the Agents page for it (settings.HiddenModels, PickedModels),
	// though no field of its picks among the catalog: the gateway's
	// /v1/models as asked with its key (Cursor Private Inference), or the
	// list magpie writes into its files, magpieModels(id), where its only
	// field puts magpie in its picker (ZCode, Pencil: #1508)
	ListsModels bool
	// NoNames: the agent's own list doesn't name magpie's models as
	// Names in agents' lists says (provider.Labels): magpie hands it the
	// models' ids alone, its format having no name for one (Goose, Hermes,
	// jcode …), it lists no models of magpie's at all (Gemini CLI, Snow),
	// or it reads the gateway's /v1/models itself, whose display_name is
	// the name alone (Empryo, Cursor Private Inference). The setting says
	// so for each one connected, rather than seeming to do nothing.
	NoNames bool
	// Sync, for an agent that reads magpie's models from a file of its own
	// rather than asking the gateway, rewrites that list as the catalog is
	// now — where magpie wrote one; nothing else changes (see SyncCatalog).
	Sync func() error
	// Unwire, for an agent whose fields' default is the agent as installed
	// rather than what it had before magpie (Codex, Claude Code, Gemini
	// CLI), takes magpie out of its config and puts back what the stash
	// kept: the endpoint, provider and model the user had. Disconnect runs
	// it before the fields' defaults.
	Unwire func() error
	// Restore, for an agent whose files magpie copied aside before its
	// first write (beforeMagpie), puts them back as they were where what
	// Disconnect left says the same. Disconnect runs it last.
	Restore func() error
	// Join, for an agent that can have magpie's models in its own list
	// while it stays on the model it was on (Codex signed in with ChatGPT),
	// connects it so, its model left as its own last pick; false where it
	// can't, and Connect then picks one of magpie's.
	Join func() (bool, error)
	// Joined reports an agent Join connected: magpie is in its config
	// though no field is on one of magpie's models.
	Joined func() bool
	// Beside reports an agent set on one of magpie's models beside its own
	// (Codex by the base URL beside its ChatGPT sign-in), now on one of its
	// own written in by the agent: still connected, as joined (#940), and
	// a pick in the agent rather than drift (#2424).
	Beside func() bool
	// OwnVia is the catalog id magpie serves one of the agent's own models
	// by on a sign-in of the user's, which its model field lists as its
	// own rather than as magpie's ("codex/gpt-5.5" for Codex's gpt-5.5),
	// "" for none: Connect keeps the agent on that model through magpie
	// where it can't Join (#940: Codex went to an unrelated model).
	OwnVia func(model string) string
	// Routed reports that the agent's config sends whatever model it
	// names to magpie's gateway (Codex's openai_base_url or magpie as its
	// provider), so a model's name the gateway takes as a routing group
	// is that group's (#750).
	Routed func() bool
	// FailingOver reports an agent that isn't connected whose requests
	// still go through magpie's gateway, for account failover alone (Codex
	// signed in to ChatGPT with more of its accounts on in magpie, #1385):
	// the Agents page says so and what turns it off, and the gateway lists
	// it only its own models.
	FailingOver func() bool
	// EndFailover takes out what account failover wrote, for magpie
	// quitting: left there, the agent would be pointed at a gateway that
	// no longer answers, and retry till magpie is open again (Leon920717
	// on X). The next start's Sync writes it again.
	EndFailover func() error
	// Follow, for an agent whose own picker moves its main model where
	// magpie keeps other settings following it (Claude Code's /model and
	// its tiers), brings those along to the model picked there. Run as the
	// Agents page is drawn.
	Follow func() error
	// RenameRefs, for an agent whose config names magpie's models beyond
	// its fields (omp's other roles and fallback chains), moves those names
	// off provider from onto to, the rest of each kept; it answers whether
	// any moved. The fields themselves are RenameProvider's.
	RenameRefs func(from, to string) (bool, error)
	// Check, for an agent magpie wires in beyond its model field, says what
	// of that wiring is gone while the model is still one of magpie's —
	// something else rewrote the config — or "" when it is all there
	// (see Drift).
	Check func() string
	// LastUsed, for an agent that keeps a log of its own prompts, is when
	// it was last used — each prompt a request the gateway should have
	// seen, so one it didn't went round magpie (see Drift). Zero if unknown.
	LastUsed func() time.Time
	// Reached, for an agent that logs where its model requests go, is its
	// newest one since a time: when, the address it went to, and whether
	// nothing answered there. Zero if there is none.
	Reached func(since time.Time) (at time.Time, to string, refused bool)
	// WSL is the distro an agent inside WSL lives in, "" for this
	// machine's own (see wsl.go).
	WSL string
	// Home is a WSL agent's $HOME in its distro as magpie opens it
	// (\\wsl.localhost\<distro>\home\me), where its other files are; ""
	// for this machine's, and while the distro is stopped: opening it
	// would start it.
	Home string
	// Gateway is the gateway's address as the agent reaches it, a WSL
	// distro's own way to it; nil is gateway.URL.
	Gateway func() string
	// Import, for an app that takes magpie only through an import link of
	// its own, which the user confirms there (Cindy), is that link; the app
	// has no fields magpie sets. Added says whether it has magpie already;
	// nil means the app exposes no way to read that status.
	Import func() string
	Added  func() bool
	// Launch, for an agent that takes the gateway only from its
	// environment (agy), is the command that starts it on magpie, while
	// it is on one of magpie's models; "" otherwise.
	Launch func() string
	// SplitSuffix, for an agent whose model values carry something of its
	// own after the model that varies from value to value (omp's thinking
	// level, "…:max"), splits a value into the model the picker offers and
	// that suffix, "" when there is none. What matches a value against the
	// picker (drift, Reseat, RenameProvider, Spell) matches the model and
	// puts the suffix back after the one it moves to. A mark that is always
	// the same for a model (Claude Code's [1m]) rides on the option instead.
	// one is false for a value that is no one model but a list the agent
	// falls back through (omp's "a,b"): that is the user's own whatever it
	// names, never taken for one of magpie's models nor moved by what
	// matches the picker (RenameRefs moves the names in it).
	SplitSuffix func(v string) (model, suffix string, one bool)
	// Spelled, when set, says whether a value of the agent's fields is
	// spelled as one of magpie's there (prefixed: it starts with
	// "magpie/", its provider in the agent): one that isn't is a model of
	// one of the agent's own providers, never magpie's, even when magpie
	// has a provider of the same name (OpenHanako's own
	// deepseek/deepseek-v4-pro read as magpie's deepseek, #835).
	Spelled func(v string) bool
	// ModelAliases are the names the agent's model takes besides ids
	// (Claude Code's opus, best, opusplan). One that names a field as well
	// is the model's on the command line: magpie claude opus sets the
	// model, where another field's name alone shows that field.
	ModelAliases []string
	// detect, when set, says whether the agent is here in place of looking
	// for its files and binary: a distro's, probed once.
	detect func() bool
	// reach, when set, is the gateway's address as the agent's config has
	// it where that is kept apart from Gateway (this machine's Codex at an
	// address of the user's, #816): Drift tries it (see reach.go).
	reach func() string
	// move, when set, points the agent's config at to where it names the
	// gateway at from: an address of WSL's that changed (#1013).
	move func(from, to string) error
	// viaProxy, when set, says whether the agent's requests for the
	// gateway get there through the proxy it sends them by (ok), whether
	// that is known yet, and the proxy and where it is set; ok with no
	// proxy when it sends them straight there (see codex_proxy.go).
	viaProxy func() (ok, known bool, proxy, from string)
	// dirShared says Dir is a folder another agent keeps its files in too
	// (omp's, when PI_CODING_AGENT_DIR points it at Pi's): that it is there
	// says nothing of this agent.
	dirShared bool
	// shared, when set, is the name of what the agent keeps in a folder its
	// vendor's other app reads and writes the same way: Cursor CLI's
	// ~/.cursor (MCP servers, skills, hooks, chats) is the Cursor editor's
	// too, so the Library and Sessions call it Cursor (SharedName).
	shared string
	// pathOwn, when set, says whether the file at Path is this agent's,
	// not one another tool wrote there first: a JetBrains plugin's
	// settings.json in Qoder CN's folder (#1495).
	pathOwn func() bool
	// sign, when set, is one more sign the agent is here, for a folder
	// that is dirShared: a file only the agent (or its own app) writes
	// there (the Qoder app's status file, #2138).
	sign func() bool
}

// Running reports whether a process whose command line matches any pattern
// (an extended regexp, as for pgrep -f) is alive. Windows can't be asked
// what runs, so anything may be: every caller is the advice an agent's own
// lists need after magpie changed what it reads at start ("restart Codex",
// "open a new dsh session"), and a Windows that answered no here dropped
// that advice silently — a model picked in magpie looked like it had done
// nothing at all. claudeRunning and Pencil's own check already say they
// can't be told, and say yes for the same reason. A var so tests can say
// what runs, as claudeRunning is: magpie's own CLI tests printed "restart
// the Codex app" on a Mac with Codex open, and on every Windows.
var Running = func(patterns ...string) bool {
	if runtime.GOOS == "windows" {
		return len(patterns) > 0
	}
	for _, pat := range patterns {
		if err := proc.Command("pgrep", "-f", pat).Run(); err == nil {
			return true
		}
	}
	return false
}

// SharedName is the agent's name over what it keeps where another app of
// its vendor's reads it too (the Library's MCP servers and skills, the
// chats Sessions lists): Cursor for Cursor CLI, whose ~/.cursor the Cursor
// editor shares; Name for any other.
func (a *Agent) SharedName() string {
	if a.shared != "" {
		return a.shared
	}
	return a.Name
}

// SharedHere reports whether the folder the agent shares with its vendor's
// other app is here (Cursor CLI's ~/.cursor, made by the Cursor editor),
// whether the agent is or not. The agent isn't Detected for it (dirShared),
// but what the other app reads there too, the Library's MCP servers and
// skills, is still given.
func (a *Agent) SharedHere() bool {
	return a.shared != "" && a.WSL == "" && a.Dir != "" && !Taken(a.Dir) && agentDir(a.Dir)
}

// Detected reports whether the agent seems to be installed or configured.
func (a *Agent) Detected() bool {
	if a.detect != nil {
		return a.detect()
	}
	// a file where the agent keeps its folder is another tool's (a shell's
	// ~/.dsh), and the agent can't be here: it couldn't make its folder
	if a.Dir != "" && Taken(a.Dir) {
		return false
	}
	if _, err := os.Stat(a.Path); err == nil && (a.pathOwn == nil || a.pathOwn()) {
		return true
	}
	if a.Dir != "" && !a.dirShared && agentDir(a.Dir) {
		return true
	}
	if a.sign != nil && a.sign() {
		return true
	}
	if a.Bin != "" && binAt(a.Bin) != "" {
		return true
	}
	// one with no Bin, as its command's name is other tools' too, that
	// Homebrew installed (Grok Build's grok-build cask) and hasn't run yet
	if a.Bin == "" && a.WSL == "" && brewBin(cliSpecs[a.ID]) != "" {
		return true
	}
	return false
}

// binAt is where the agent's command name is, as magpie runs it: on
// magpie's own PATH, else, on Windows, where a terminal opened now finds it
// or a user's tools are installed (proc.FindTool: the registry's PATH now,
// ~/.local/bin, npm's, bun's, Volta's, pnpm's). Windows has no login shell
// to lend magpie its PATH (proc.UserPath does nothing there), so a magpie
// started before Claude Code's installer put claude.exe in ~/.local/bin
// and added that to the user's PATH, or a self-update restarting with the
// PATH it had then, never listed it, while the sign-in, which asked
// FindTool, found it (#839, #1482). A mise wrapper script found there is
// given as the program it runs (proc.LookPath). "" when it is in none.
func binAt(name string) string {
	if p, err := proc.LookPath(name); err == nil {
		return p
	}
	if runtime.GOOS == "windows" {
		return proc.FindTool(name)
	}
	return ""
}

// agentDir reports whether p is a folder an agent itself made. Skill
// installers (npx skills and the like) make <folder>/skills for every agent
// they know, installed or not; a folder holding nothing else is theirs.
func agentDir(p string) bool {
	es, err := os.ReadDir(p)
	if err != nil {
		return isDir(p)
	}
	if len(es) == 0 {
		return true
	}
	for _, e := range es {
		if n := e.Name(); n != "skills" && n != ".DS_Store" && !(e.IsDir() && leftover[strings.ToLower(n)]) {
			return true
		}
	}
	return false
}

// leftover are the folders an agent writes as it runs, not as it is set
// up, and that stay behind when it is uninstalled: ~/.codebuddy holding
// only logs/ and diagnostics/, ~/.qwen's debug/ (#1495, v5tech). A folder
// holding only these (and skills) is no sign the agent is here. A file of
// the same name, or anything else beside them, still is.
var leftover = map[string]bool{"logs": true, "log": true, "debug": true, "diagnostics": true, "cache": true}

// Taken reports whether something that isn't a folder is where the folder
// p, or one it is in, would be: nothing can be written under it.
func Taken(p string) bool {
	for d := filepath.Clean(p); ; {
		if st, err := os.Stat(d); err == nil {
			return !st.IsDir()
		}
		up := filepath.Dir(d)
		if up == d {
			return false
		}
		d = up
	}
}

// goProgram reports whether bin was built by Go: another tool of the same
// name, not the agent, when the agent is not written in Go. Reading a
// binary's build info parses its whole symbol table (~150ms for a large
// one), and detection runs on every state the window asks for, so the
// answer is kept while the file is the same.
func goProgram(bin string) bool {
	st, err := os.Stat(bin)
	if err != nil {
		return false
	}
	key := goProgramKey{bin, st.Size(), st.ModTime()}
	goPrograms.Lock()
	defer goPrograms.Unlock()
	if v, ok := goPrograms.m[key]; ok {
		return v
	}
	_, err = buildinfo.ReadFile(bin)
	if goPrograms.m == nil {
		goPrograms.m = map[goProgramKey]bool{}
	}
	goPrograms.m[key] = err == nil
	return err == nil
}

type goProgramKey struct {
	path string
	size int64
	mod  time.Time
}

var goPrograms struct {
	sync.Mutex
	m map[goProgramKey]bool
}

// Field looks a field up by key, in any case, as Find looks an agent up
// (magpie codex Model was written as the model).
func (a *Agent) Field(key string) *Field {
	for i := range a.Fields {
		if strings.EqualFold(a.Fields[i].Key, key) {
			return &a.Fields[i]
		}
	}
	for i := range a.Fields {
		if strings.EqualFold(a.Fields[i].Label, key) { // `magpie gemini auth …`: the label as shown
			return &a.Fields[i]
		}
	}
	return nil
}

// Values reads every field.
func (a *Agent) Values() map[string]string {
	m := make(map[string]string, len(a.Fields))
	for _, f := range a.Fields {
		m[f.Key] = f.Get()
	}
	return m
}

// Picks are the options f's picker offers: all of them, unless the user
// asked a's pickers to offer magpie's models alone (#1642, MizuIro-H). Then
// a field that picks among magpie's catalog leaves out the models the agent
// reaches on its own (DeepSeek Harness's DeepSeek ones), but for its
// default and the one it is set to.
func (a *Agent) Picks(f Field, cur map[string]string) []Option {
	opts := f.Options(cur)
	if !provider.OwnModelsHidden(a.ListsFor()) || !slices.ContainsFunc(opts, func(o Option) bool { return o.Ref != "" }) {
		return opts
	}
	return slices.DeleteFunc(slices.Clone(opts), func(o Option) bool { return Own(o) && o.Value != cur[f.Key] })
}

// Own reports whether o is a model the agent reaches on its own, not one of
// magpie's catalog: what Picks leaves out on the user's word.
func Own(o Option) bool { return o.Ref == "" && o.Value != "" }

// OwnModels reports whether one of a's pickers that picks among magpie's
// catalog offers models of the agent's own too, which Picks can leave out.
func (a *Agent) OwnModels(cur map[string]string) bool {
	for _, f := range a.Fields {
		opts := f.Options(cur)
		if slices.ContainsFunc(opts, func(o Option) bool { return o.Ref != "" }) && slices.ContainsFunc(opts, Own) {
			return true
		}
	}
	return false
}

// Detected returns the agents present on this machine, in display order.
func Detected() []*Agent {
	var out []*Agent
	for _, a := range All() {
		if a.Detected() {
			out = append(out, a)
		}
	}
	return out
}

// Find resolves a user-typed name (id, alias, or unique prefix).
func Find(q string) (*Agent, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	all := All()
	var prefix []*Agent
	for _, a := range all {
		if strings.EqualFold(a.ID, q) { // codex@wsl:Ubuntu
			return a, nil
		}
		for _, al := range a.Aliases {
			if al == q {
				return a, nil
			}
		}
		if strings.HasPrefix(a.ID, q) || strings.HasPrefix(strings.ToLower(a.Name), q) {
			prefix = append(prefix, a)
		}
	}
	switch len(prefix) {
	case 1:
		return prefix[0], nil
	case 0:
		return nil, fmt.Errorf("unknown agent %q (try: %s)", q, strings.Join(ids(all), ", "))
	}
	return nil, fmt.Errorf("%q is ambiguous: %s", q, strings.Join(ids(prefix), ", "))
}

func ids(as []*Agent) []string {
	s := make([]string, len(as))
	for i, a := range as {
		s[i] = a.ID
	}
	sort.Strings(s)
	return s
}

// Spell puts a value typed for field key the way this agent spells it: a
// catalog model is "copilot/gpt-6" in some agents and "magpie/copilot/gpt-6"
// in others, and either is taken in both. A value the picker offers as is
// stays, and so does one it doesn't know (a model the agent reaches on its
// own that isn't listed); a "magpie/…" value the catalog doesn't have is an
// error rather than a model the agent would ask its own vendor for, and so
// is one of magpie's the agent isn't shown (notOffered). The
// agent's suffix after the model (SplitSuffix) stays as typed, and so does
// a list of models, the user's own.
func (a *Agent) Spell(key, v string) (string, error) {
	f := a.Field(key)
	if f == nil || f.Options == nil || v == "" {
		return v, nil
	}
	model, suffix, one := a.split(v)
	if !one {
		return v, nil
	}
	opts := f.Options(a.Values())
	for _, o := range opts {
		if o.Value == model {
			return v, nil
		}
	}
	ref, prefixed := strings.CutPrefix(model, magpieID+"/")
	// a provider renamed since (a profile saved before) is the same one
	refs := []string{ref}
	if r := provider.RenamedRef(ref); r != ref {
		refs = append(refs, r)
	}
	for _, r := range refs {
		for _, o := range opts {
			if o.Ref != "" && o.Ref == r {
				return o.Value + suffix, nil
			}
		}
	}
	if prefixed {
		return "", fmt.Errorf("%s isn't a model in magpie's catalog (magpie models lists them)", ref)
	}
	if err := a.notOffered(ref, opts); err != nil {
		return "", err
	}
	return v, nil
}

// notOffered refuses a value that names one of magpie's models, or one of
// its providers or routing groups, that the agent isn't shown: kept from
// the agent (magpie visible, a model not ticked for it) or not on its
// provider's list yet (a remote magpie's group added there since the list
// was fetched). Taken as the agent's own, Pi read remote/group/smart as
// its own provider "remote" and model "group/smart", and was taken out of
// magpie for a provider it doesn't have (#2376). A provider id the agent's
// own models go by too (openai/… in Pi) stays the agent's.
func (a *Agent) notOffered(ref string, opts []Option) error {
	pid, model, ok := strings.Cut(ref, "/")
	if !ok || pid == "" || model == "" {
		return nil
	}
	if slices.ContainsFunc(opts, func(o Option) bool { return Own(o) && strings.HasPrefix(o.Value, pid+"/") }) {
		return nil
	}
	ids := []string{ref, provider.RenamedRef(ref)}
	shown, _ := provider.CatalogFor(a.ListsFor())
	if slices.ContainsFunc(shown, func(e provider.Entry) bool { return slices.Contains(ids, e.ID) }) {
		return nil // shown the agent: a field may offer fewer (Claude Desktop's tiers)
	}
	for _, e := range provider.Catalog() {
		if slices.Contains(ids, e.ID) {
			return fmt.Errorf("%s is one of magpie's models, but %s isn't shown it: magpie models %s says why, and magpie visible %s all shows it every model", ref, a.Name, a.ID, a.ID)
		}
	}
	if pid+"/" == provider.GroupPrefix {
		return fmt.Errorf("magpie has no routing group %s (magpie models lists them)", ref)
	}
	if p, err := provider.Find(pid); err == nil && (p.ID == pid || slices.Contains(p.Was, pid)) {
		return fmt.Errorf("%s's model list has no %s: magpie provider refresh %s fetches the list again (or add the model to it in its editor), and magpie models %s lists what %s is shown", p.ID, model, p.ID, a.ID, a.Name)
	}
	return nil
}

// split is v as the model the picker offers and the agent's suffix after
// it (SplitSuffix), one false for a list of models; the whole of v for an
// agent without one.
func (a *Agent) split(v string) (model, suffix string, one bool) {
	if a.SplitSuffix == nil {
		return v, "", true
	}
	return a.SplitSuffix(v)
}

// atomic makes each of an agent's field sets, and its Sync, one edit of the
// files at paths: one that fails part way puts them all back as they were,
// rather than leaving, say, Codex's config.toml with magpie's provider table
// written but its model not (#253).
func atomic(a *Agent, paths ...string) *Agent {
	for i := range a.Fields {
		if set := a.Fields[i].Set; set != nil {
			a.Fields[i].Set = func(v string) error {
				return edit.Atomically(func() error { return set(v) }, paths...)
			}
		}
	}
	if sync := a.Sync; sync != nil {
		a.Sync = func() error { return edit.Atomically(sync, paths...) }
	}
	if follow := a.Follow; follow != nil {
		a.Follow = func() error { return edit.Atomically(follow, paths...) }
	}
	if unwire := a.Unwire; unwire != nil {
		a.Unwire = func() error { return edit.Atomically(unwire, paths...) }
	}
	return a
}
