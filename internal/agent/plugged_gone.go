package agent

// An agent a plugin adds is known to magpie only while the plugin is on:
// turned off or removed, `magpie <agent> default` said "unknown agent" and
// the agent's config was left on magpie, with nothing to put it back
// (#3810). So magpie takes itself out of the agent's config before the
// plugin goes (UnwirePlugin, which internal/provider calls), and what it
// stashed of the file is enough to put it back with the plugin gone
// (RestoreGone, for a plugin deleted on disk).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/agentplug"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

func init() {
	provider.PluginAgentGoing = UnwirePlugin
}

// UnwirePlugin takes magpie out of the config of the agent the plugin spec
// adds and puts back what the user had, as `magpie <agent> default` does
// (Disconnect), before the plugin is turned off or removed. The agent is
// the one listed while the plugin is on, else its module read as it is (a
// plugin off already); with no module that loads, each file stashed as
// the plugin's is put back from the stash alone. An agent not on magpie is
// left as it is.
func UnwirePlugin(spec string) error {
	var a *Agent
	for _, x := range All() {
		if x.Plugin == spec {
			a = x
			break
		}
	}
	if a == nil {
		a = pluggedOff(spec)
	}
	if a != nil {
		if !a.Wired() {
			return nil
		}
		if err := a.Disconnect(); err != nil {
			return fmt.Errorf("%s: %w", a.Name, err)
		}
		return nil
	}
	_, _, err := restoreLeft(func(_ string, st pluggedStash) bool { return st.Spec == spec })
	return err
}

// pluggedOff is the agent of a plugin that isn't listed (switched off, or
// its list not read again yet), its module read as it is; nil when it adds
// none, its module doesn't load, or its id is one of magpie's own agents'.
func pluggedOff(spec string) *Agent {
	file, _ := plugin.Agent(plugin.Target(spec))
	if file == "" {
		return nil
	}
	p, err := agentplug.Compile(spec, file)
	if err != nil {
		return nil
	}
	home, cfg := homes()
	for _, b := range append(builtins(home, cfg), others...) {
		if (b.ID == p.Desc.ID || slices.Contains(b.Aliases, p.Desc.ID)) && !movesTo(b.ID, p) {
			return nil
		}
	}
	return plugged(here(home), p)
}

// Gone is what RestoreGone did: the files it put back as the user had
// them, and those magpie was already out of. PluginOff names the agent of
// a plugin switched off with nothing stashed: magpie is out of it already.
type Gone struct {
	Put, Off  []string
	PluginOff string
}

// RestoreGone puts back what the user had in each file magpie wired for
// the agent id of a plugin that is gone (deleted on disk, or taken out of
// plugins.json by hand), from the stash alone, for `magpie <id> default`.
// ok is false when id is an agent here, or nothing is stashed for it and
// no plugin switched off adds it.
func RestoreGone(id string) (g Gone, ok bool, err error) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, a := range All() {
		if strings.EqualFold(a.ID, id) {
			return g, false, nil
		}
	}
	g.Put, g.Off, err = restoreLeft(func(sid string, _ pluggedStash) bool { return sid == id })
	if len(g.Put)+len(g.Off) > 0 || err != nil {
		return g, true, err
	}
	for _, e := range plugin.Load().Plugins {
		if !e.Off {
			continue
		}
		if a := pluggedOff(e.Spec); a != nil && a.ID == id {
			g.PluginOff = a.Name
			return g, true, nil
		}
	}
	return g, false, nil
}

// restoreLeft puts back each agent plugin's file stashed that match
// takes, from the stash: put are the files put back, off those whose
// wiring was gone already (its stash is forgotten). A stash that doesn't
// parse is left alone.
func restoreLeft(match func(id string, st pluggedStash) bool) (put, off []string, err error) {
	m := stashLoad()
	for _, k := range sortedKeys(m) {
		rest, ok := strings.CutPrefix(k, "plugin:")
		if !ok {
			continue
		}
		// an id has no ':' (agentplug's idOK), a Windows path may
		id, path, ok := strings.Cut(rest, ":")
		if !ok {
			continue
		}
		var st pluggedStash
		if json.Unmarshal([]byte(m[k]), &st) != nil || !match(id, st) {
			continue
		}
		format := st.Format
		if format == "" {
			format = agentplug.FormatOf(path)
		}
		if format == "" {
			err = errors.Join(err, fmt.Errorf("%s: its format isn't known, so magpie keeps what it had in its stash", path))
			continue
		}
		f := pluggedFile{path, format}
		held, e := f.holds(st.Value)
		if e != nil {
			err = errors.Join(err, fmt.Errorf("%s: %w", path, e))
			continue
		}
		if !held || !f.wired(id, st) {
			forget(k)
			off = append(off, path)
			continue
		}
		if e := edit.Atomically(func() error { return f.restore(k, st) }, path); e != nil {
			err = errors.Join(err, fmt.Errorf("%s: %w", path, e))
			continue
		}
		put = append(put, path)
	}
	return put, off, err
}

// holds says the file still has the gateway's key magpie wrote somewhere
// in it: with no plugin to say which key is the model (an object connect
// returned has no Mark), a file that has lost it, or is gone, was taken
// off magpie by hand, and what the user wrote since is theirs. A file
// that can't be read is an error: nothing is put back over it, and its
// stash is kept.
func (f pluggedFile) holds(key string) (bool, error) {
	b, err := os.ReadFile(f.path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return key == "" || bytes.Contains(b, []byte(key)), nil
}
