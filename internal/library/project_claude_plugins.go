package library

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/edit"
)

// A project can turn on a plugin Claude Code has installed for that folder
// alone (#7254, jinpwng on X): enabledPlugins in the project's
// .claude/settings.local.json, which Claude Code reads for the folder and
// which wins over the user's settings.json. The plugin itself stays
// installed through Claude Code's own CLI; magpie writes the one key
// (claudeSettingsRel, the file the project's skill offs and hooks share) and
// takes out only a key it wrote, still true (Project.ClaudePlugins is what
// the project wants, ClaudeWrote what magpie wrote). An entry the user put
// there is theirs: it is neither written over nor taken out.
//
//	<project>/.claude/settings.local.json   {"enabledPlugins": {"p@m": true}}

// ClaudePluginView is a plugin Claude Code has installed, as the project
// card lists it: whether the user's own settings have it on.
type ClaudePluginView struct {
	ID string `json:"id"`
	On bool   `json:"on"`
}

// ReadClaudeInstalled lists the plugins Claude Code's own record
// (plugins/installed_plugins.json) has installed, with whether
// settings.json turns each on.
func ReadClaudeInstalled() []ClaudePluginView {
	raw, err := readClaudeJSON(claudeInstalled())
	if err != nil {
		return nil
	}
	set, _ := readClaudeJSON(claudeSettings())
	var out []ClaudePluginView
	gjson.GetBytes(raw, "plugins").ForEach(func(k, _ gjson.Result) bool {
		id := k.String()
		if strings.Contains(id, "@") {
			out = append(out, ClaudePluginView{ID: id, On: gjson.GetBytes(set, "enabledPlugins."+gjson.Escape(id)).Type == gjson.True})
		}
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func projectClaudeSettings(p *Project) string {
	return filepath.Join(p.Dir, filepath.FromSlash(claudeSettingsRel))
}

// syncProjectClaudePlugins keeps enabledPlugins.<id> = true in the project's
// .claude/settings.local.json for each plugin the project turns on, and
// takes out the ones magpie wrote that it no longer does.
func (l *Library) syncProjectClaudePlugins(p *Project, fail func(what string, err error)) {
	if len(p.ClaudePlugins) == 0 && len(p.ClaudeWrote) == 0 {
		return
	}
	abs := projectClaudeSettings(p)
	if err := p.inside(abs); err != nil {
		fail("claude-plugin", err)
		return
	}
	key := func(id string) string { return "enabledPlugins." + gjson.Escape(id) }
	var mine []string
	for _, id := range p.ClaudeWrote {
		if slices.Contains(p.ClaudePlugins, id) {
			if v, ok := edit.GetJSON(abs, key(id)); ok && v == "true" {
				mine = append(mine, id)
			}
			continue
		}
		if v, ok := edit.GetJSON(abs, key(id)); ok && v != "true" {
			continue // the user's own by now
		}
		if err := edit.DelJSON(abs, key(id)); err != nil {
			fail("claude-plugin:"+id, err)
			mine = append(mine, id)
		}
	}
	for _, id := range p.ClaudePlugins {
		if slices.Contains(mine, id) {
			continue
		}
		if v, ok := edit.GetJSON(abs, key(id)); ok {
			if v != "true" {
				fail("claude-plugin:"+id, fmt.Errorf("%s sets %s for this project already: it's left as it is", claudeSettingsRel, id))
			}
			continue // the user's own
		}
		if !exists(abs) {
			if err := p.mkdirs(filepath.Dir(abs)); err != nil {
				fail("claude-plugin:"+id, err)
				continue
			}
			if !slices.Contains(p.MadeFiles, claudeSettingsRel) {
				p.MadeFiles = append(p.MadeFiles, claudeSettingsRel)
			}
		}
		if err := edit.SetJSON(abs, edit.KV{Path: key(id), Value: true}); err != nil {
			fail("claude-plugin:"+id, err)
			continue
		}
		mine = append(mine, id)
	}
	sort.Strings(mine)
	p.ClaudeWrote = mine
	if len(mine) == 0 {
		p.ClaudeWrote = nil
		if len(p.Overrides) == 0 && len(p.HookRecs["claude"]) == 0 {
			p.dropMadeFile(claudeSettingsRel)
		}
	}
}

// ProjectClaudePlugin sets whether a project turns on a plugin Claude Code
// has installed, in its own settings; off takes the key magpie wrote out.
func ProjectClaudePlugin(dir, id string, on bool) (*Result, error) {
	if i := strings.Index(id, "@"); i < 1 || i == len(id)-1 {
		return nil, fmt.Errorf("%q isn't a plugin id: it looks like name@marketplace", id)
	}
	return change(func(l *Library) error {
		p := l.project(dir)
		if p == nil {
			return fmt.Errorf("no project %s", dir)
		}
		p.ClaudePlugins = slices.DeleteFunc(p.ClaudePlugins, func(x string) bool { return x == id })
		if on {
			p.ClaudePlugins = append(p.ClaudePlugins, id)
			sort.Strings(p.ClaudePlugins)
		}
		return nil
	})
}
