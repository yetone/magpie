package library

// Claude Desktop's Cowork (local-agent-mode) finds the user's skills in a
// plugin of its own, one for each account it has been signed in with:
//
//	<Claude or Claude-3p>/local-agent-mode-sessions/skills-plugin/<org>/<account>/
//	  .claude-plugin/plugin.json   {"name":"anthropic-skills",…}
//	  manifest.json                {"lastUpdated":…,"skills":[{"skillId","name",
//	                                 "description","creatorType":"user",
//	                                 "syncManaged":false,"updatedAt","enabled"}]}
//	  skills/<name>/SKILL.md
//
// A skill is listed only once the manifest has it, and stays listed after
// its folder goes, so magpie writes both (#638). Each is a copy, not a
// link: Cowork runs a linked skill but won't read the files beside its
// SKILL.md from outside the plugin's folder. The copy is magpie's by its
// mark, which also keeps the hash of what was copied: one changed in
// Desktop since is left as it is, and set aside rather than deleted when
// the library stops giving it. Desktop reads the manifest again when its
// window is reloaded.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/desktopdir"
	"github.com/yetone/magpie/internal/edit"
)

const desktopManifest = "manifest.json"

// desktopSkillRoots are the skills-plugin folders of every account Claude
// Desktop has, in its own folder and Claude-3p's alike: each one with a
// manifest.json or the plugin's plugin.json.
func desktopSkillRoots(desktopDir string) []string {
	dirs := append([]string{desktopDir}, desktopdir.Here().All()...)
	var out []string
	seen := map[string]bool{}
	for _, d := range dirs {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		base := filepath.Join(d, "local-agent-mode-sessions", "skills-plugin")
		orgs, _ := os.ReadDir(base)
		for _, o := range orgs {
			if !o.IsDir() {
				continue
			}
			accts, _ := os.ReadDir(filepath.Join(base, o.Name()))
			for _, a := range accts {
				p := filepath.Join(base, o.Name(), a.Name())
				if a.IsDir() && (isFile(filepath.Join(p, desktopManifest)) || isFile(filepath.Join(p, ".claude-plugin", "plugin.json"))) {
					out = append(out, p)
				}
			}
		}
	}
	return out
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// desktopCopy puts a copy of the library's skill at p, its mark keeping
// the hash of what was copied (markCopy).
func desktopCopy(p, name string) error {
	return copyIn(p, name)
}

// desktopEdited is whether magpie's copy at p was changed since it was
// made: its files are no longer what its mark says were copied.
func desktopEdited(p string) bool {
	b, err := os.ReadFile(filepath.Join(p, marker))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if h, ok := strings.CutPrefix(strings.TrimSpace(line), "hash "); ok {
			return h != hashDir(p)
		}
	}
	return false
}

// desktopStamp is now as the manifest's lastUpdated is written: a number
// of milliseconds where it is one, else an ISO time.
func desktopStamp(raw []byte) any {
	if gjson.GetBytes(raw, "lastUpdated").Type == gjson.Number {
		return time.Now().UnixMilli()
	}
	return isoStamp(time.Now())
}

// isoStamp is t as a skill's updatedAt is written. Desktop checks the whole
// list of skills at once and takes each updatedAt to be such a string (or
// null), whatever lastUpdated is: one number in it and none of the skills
// are listed (#863).
func isoStamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// fixUpdatedAt is the entry with its updatedAt, when a number of
// milliseconds (as magpie wrote them before #863), written as Desktop's
// are, the other keys as they were; "" when it isn't a number.
func fixUpdatedAt(e gjson.Result) string {
	if e.Get("updatedAt").Type != gjson.Number {
		return ""
	}
	v, _ := json.Marshal(isoStamp(time.UnixMilli(e.Get("updatedAt").Int())))
	var b strings.Builder
	b.WriteByte('{')
	e.ForEach(func(k, x gjson.Result) bool {
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		b.WriteString(k.Raw + ":")
		if k.String() == "updatedAt" {
			b.Write(v)
		} else {
			b.WriteString(x.Raw)
		}
		return true
	})
	b.WriteByte('}')
	return b.String()
}

// manifestEntry is a manifest's entry for the skill with that id, "" for
// none.
func manifestEntry(raw []byte, id string) string {
	for _, e := range gjson.GetBytes(raw, "skills").Array() {
		if e.Get("skillId").String() == id {
			return e.Raw
		}
	}
	return ""
}

// writeManifest sets the manifest's entries for the skills in put (made
// anew, or their description and time renewed, the rest of each kept:
// enabled above all) and takes out those in drop; the
// other entries and keys stay as they are.
func writeManifest(root string, put map[string]bool, drop []string) error {
	path := filepath.Join(root, desktopManifest)
	raw, err := edit.Read(path)
	if err != nil && isFile(path) {
		return err
	}
	stamp, updated := desktopStamp(raw), isoStamp(time.Now())
	var list []json.RawMessage
	changed := false
	done := map[string]bool{}
	entry := func(name string, old string) json.RawMessage {
		desc := ""
		if md, ok := readMeta(skillDir(name)); ok {
			desc = md.Description
		}
		enabled := json.RawMessage("true")
		if e := gjson.Get(old, "enabled"); e.Exists() {
			enabled = json.RawMessage(e.Raw)
		}
		// in the order Desktop writes them, any other key of the old
		// entry after them
		keys := []string{"skillId", "name", "description", "creatorType", "syncManaged", "updatedAt", "enabled"}
		vals := []any{name, name, desc, "user", false, updated, enabled}
		var b strings.Builder
		b.WriteByte('{')
		for i, k := range keys {
			v, _ := json.Marshal(vals[i])
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, "%q:%s", k, v)
		}
		gjson.Parse(old).ForEach(func(k, v gjson.Result) bool {
			if !slices.Contains(keys, k.String()) {
				b.WriteString("," + k.Raw + ":" + v.Raw)
			}
			return true
		})
		b.WriteByte('}')
		return json.RawMessage(b.String())
	}
	for _, e := range gjson.GetBytes(raw, "skills").Array() {
		id := e.Get("skillId").String()
		switch {
		case slices.Contains(drop, id):
			changed = true
		case put[id] && !done[id]:
			list = append(list, entry(id, e.Raw))
			done[id], changed = true, true
		case fixUpdatedAt(e) != "":
			list = append(list, json.RawMessage(fixUpdatedAt(e)))
			changed = true
		default:
			list = append(list, json.RawMessage(e.Raw))
		}
	}
	var added []string
	for id := range put {
		if !done[id] {
			added = append(added, id)
		}
	}
	slices.Sort(added)
	for _, id := range added {
		list = append(list, entry(id, ""))
		changed = true
	}
	if !changed {
		return nil
	}
	if list == nil {
		list = []json.RawMessage{}
	}
	if !isFile(path) {
		b, _ := json.MarshalIndent(map[string]any{"lastUpdated": stamp, "skills": list}, "", "  ")
		return edit.WriteAtomic(path, append(b, '\n'))
	}
	return edit.SetJSON(path, edit.KV{Path: "skills", Value: list}, edit.KV{Path: "lastUpdated", Value: stamp})
}

// syncDesktopSkills gives Claude Desktop the library's skills it is to
// have, in every account's skills-plugin, and takes out the ones magpie
// put there that it no longer is to.
func (l *Library) syncDesktopSkills(t *Target, res *Result) {
	id := t.Agent.ID
	a := l.applied(id)
	prior := a.Skills
	var mine []string
	keep := func(name string) {
		if !slices.Contains(mine, name) {
			mine = append(mine, name)
		}
	}
	for _, root := range t.Desktop {
		dir := filepath.Join(root, "skills")
		raw, _ := edit.Read(filepath.Join(root, desktopManifest))
		put := map[string]bool{}
		var drop []string
		for _, name := range prior {
			if s := l.skill(name); s != nil && slices.Contains(s.Agents, id) {
				continue
			}
			p := filepath.Join(dir, name)
			if !ours(p, name) {
				continue
			}
			var err error
			if desktopEdited(p) {
				// changed in Desktop: kept with the backups, not lost
				_, err = setAside(id, p)
			} else {
				err = unlink(p)
			}
			if err != nil {
				res.fail(id, "skill:"+name, err)
				keep(name)
				continue
			}
			drop = append(drop, name)
			res.changed(id)
		}
		for _, s := range l.Skills {
			if !slices.Contains(s.Agents, id) {
				continue
			}
			p := filepath.Join(dir, s.Name)
			if ours(p, s.Name) {
				keep(s.Name)
				switch {
				case linked(p) || !fresh(p, s.Name) && !desktopEdited(p):
					if err := desktopCopy(p, s.Name); err != nil {
						res.fail(id, "skill:"+s.Name, err)
						continue
					}
					put[s.Name] = true
					res.changed(id)
				case !fresh(p, s.Name):
					res.Problems = append(res.Problems, Problem{Agent: id, What: "skill:" + s.Name,
						Error: fmt.Sprintf("%s was changed in Claude Desktop, so the library's copy isn't put over it: delete it in Desktop to have the library's again", s.Name)})
				}
				if manifestEntry(raw, s.Name) == "" {
					put[s.Name] = true
					res.changed(id)
				}
				continue
			}
			// one of Desktop's own (built in, or made there) by that name
			// stays, the library's left out; magpie's own entry, its folder
			// gone, gets the folder back
			_, err := os.Lstat(p)
			if err == nil || manifestEntry(raw, s.Name) != "" && !slices.Contains(prior, s.Name) {
				res.Problems = append(res.Problems, Problem{Agent: id, What: "skill:" + s.Name,
					Error: fmt.Sprintf("Claude Desktop already has a skill of its own called %s", s.Name)})
				continue
			}
			if err := desktopCopy(p, s.Name); err != nil {
				res.fail(id, "skill:"+s.Name, err)
				continue
			}
			keep(s.Name)
			put[s.Name] = true
			res.changed(id)
		}
		if err := writeManifest(root, put, drop); err != nil {
			res.fail(id, "skills", err)
		}
	}
	a.Skills = mine
}
