// Package agent describes every coding agent magpie can drive: where its config
// lives, which fields matter (model, effort, …) and which values to offer.
package agent

import (
	"debug/buildinfo"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

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
	// Context is the tokens the model takes, when known; the picker marks
	// the large ones
	Context int `json:"context,omitempty"`
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
	// Import, for an app that takes magpie only through an import link of
	// its own, which the user confirms there (Cindy), is that link; the app
	// has no fields magpie sets. Added says whether it has magpie already.
	Import func() string
	Added  func() bool
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
	if _, err := os.Stat(a.Path); err == nil {
		return true
	}
	if a.Dir != "" {
		if _, err := os.Stat(a.Dir); err == nil {
			return true
		}
	}
	if a.Bin != "" {
		if _, err := exec.LookPath(a.Bin); err == nil {
			return true
		}
	}
	return false
}

// goProgram reports whether bin was built by Go: another tool of the same
// name, not the agent, when the agent is not written in Go.
func goProgram(bin string) bool {
	_, err := buildinfo.ReadFile(bin)
	return err == nil
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
// error rather than a model the agent would ask its own vendor for.
func (a *Agent) Spell(key, v string) (string, error) {
	f := a.Field(key)
	if f == nil || f.Options == nil || v == "" {
		return v, nil
	}
	opts := f.Options(a.Values())
	for _, o := range opts {
		if o.Value == v {
			return v, nil
		}
	}
	ref, prefixed := strings.CutPrefix(v, magpieID+"/")
	// a provider renamed since (a profile saved before) is the same one
	refs := []string{ref}
	if r := provider.RenamedRef(ref); r != ref {
		refs = append(refs, r)
	}
	for _, r := range refs {
		for _, o := range opts {
			if o.Ref != "" && o.Ref == r {
				return o.Value, nil
			}
		}
	}
	if prefixed {
		return "", fmt.Errorf("%s isn't a model in magpie's catalog (magpie models lists them)", ref)
	}
	return v, nil
}
