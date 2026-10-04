// Package agent describes every coding agent magpie can drive: where its config
// lives, which fields matter (model, effort, …) and which values to offer.
package agent

import (
	"debug/buildinfo"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	// Direct names who the agent asks for this model itself, on its own
	// sign-in or key, with magpie not in the way ("Anthropic"): its config
	// then names no magpie endpoint, which is right, not a failed setup
	Direct string `json:"direct,omitempty"`
	// Same is a model magpie serves on the very account the agent is
	// signed in to itself, so the agent reaches it on its own too: the
	// picker folds these into one row a click opens (Claude Code, #496)
	Same bool `json:"same,omitempty"`
	// Alias is the value of the option a dated id is another name of
	// (claude-opus-4-5-20251101 → claude-opus-4-5): the picker shows one
	// row for the two, the alias, unless the dated one is the value set
	Alias string `json:"alias,omitempty"`

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
	// Follows is the key of the field a Quiet one takes after while empty
	// ("model" for Claude Code's tiers), for a profile's details to say so.
	Follows string
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
	Fields  []Field
	// Notice, if set, is advice worth showing after a change: agents that
	// read their config once at start-up need a restart to see it.
	Notice func() string
	// UA is what the agent's User-Agent begins with, lower-case: how the
	// gateway tells its requests from others'
	UA []string
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
	// Join, for an agent that can have magpie's models in its own list
	// while it stays on the model it was on (Codex signed in with ChatGPT),
	// connects it so, its model left as its own last pick; false where it
	// can't, and Connect then picks one of magpie's.
	Join func() (bool, error)
	// Joined reports an agent Join connected: magpie is in its config
	// though no field is on one of magpie's models.
	Joined func() bool
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
	// has no fields magpie sets. Added says whether it has magpie already.
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
	// detect, when set, says whether the agent is here in place of looking
	// for its files and binary: a distro's, probed once.
	detect func() bool
}

// Running reports whether a process whose command line matches any pattern
// (an extended regexp, as for pgrep -f) is alive. Unknown on Windows.
func Running(patterns ...string) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	for _, pat := range patterns {
		if err := proc.Command("pgrep", "-f", pat).Run(); err == nil {
			return true
		}
	}
	return false
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
	if _, err := os.Stat(a.Path); err == nil {
		return true
	}
	if a.Dir != "" && isDir(a.Dir) {
		return true
	}
	if a.Bin != "" {
		if _, err := exec.LookPath(a.Bin); err == nil {
			return true
		}
	}
	return false
}

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

// Field looks a field up by key.
func (a *Agent) Field(key string) *Field {
	for i := range a.Fields {
		if a.Fields[i].Key == key {
			return &a.Fields[i]
		}
	}
	for i := range a.Fields {
		if a.Fields[i].Label == key { // `magpie gemini auth …`: the label as shown
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
// error rather than a model the agent would ask its own vendor for. The
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
	return v, nil
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
