package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/edit"
)

// A project's Pi packages (#7249, jinpwng on X): entries of `packages` in the
// project's .pi/settings.json, which Pi reads for the folder and installs
// itself (into .pi/npm and .pi/git) when it next starts there. Three states:
// on (the source alone), off but kept (every filter empty, which loads none
// of it, as `pi config` writes it) and removed (no entry). Project.Pi is what
// the project wants by source, PiRecs what magpie wrote, the only entries it
// changes or takes out, and only while they are still as it wrote them.
//
// An entry the project had before is the user's: it is never rewritten or
// taken out. Adopting it records it (Orig is what it was) so it can be turned
// off and on; letting go of an adopted entry puts Orig back, it is never
// deleted.
//
//	<project>/.pi/settings.json   {"packages": ["npm:pi-demo", {"source": "...", "extensions": [], ...}]}

const piSettingsRel = ".pi/settings.json"

// PiRec is a package entry magpie wrote in a project: Entry as it is now,
// Orig the user's entry it adopted, nil for one magpie added.
type PiRec struct {
	Source string          `json:"source"`
	Entry  json.RawMessage `json:"entry"`
	Orig   json.RawMessage `json:"orig,omitempty"`
}

// ProjectPiView is a package the project has by magpie, for its card.
type ProjectPiView struct {
	Source string `json:"source"`
	Name   string `json:"name"`
	// State is "on" or "off" (kept, loads nothing)
	State string `json:"state"`
	// Adopted: the entry was the project's own, which magpie only switches
	Adopted bool `json:"adopted,omitempty"`
}

func piIDOf(s string) string {
	if id := piSourceID(s); id != "" {
		return id
	}
	return "local:" + filepath.ToSlash(filepath.Clean(s))
}

// piEntryFor is the entry that says state: a package's source alone for on,
// the same with every filter empty for off.
func piEntryFor(source, state string, orig json.RawMessage) json.RawMessage {
	if state == "on" {
		if orig != nil {
			if pk, ok := piPackageOf(orig); ok && pk.On {
				return orig
			}
		}
		b, _ := json.Marshal(source)
		return b
	}
	o := map[string]json.RawMessage{}
	if orig != nil {
		_ = json.Unmarshal(orig, &o)
	}
	b, _ := json.Marshal(source)
	o["source"] = b
	for _, k := range piFilterKeys {
		o[k] = json.RawMessage("[]")
	}
	e, _ := json.Marshal(o)
	return e
}

// projectPiList is the project's packages entries; the file not there is an
// empty list, one that can't be read an error (unknown, never empty).
func projectPiList(abs string) ([]json.RawMessage, error) {
	doc, err := readHooksFile(abs)
	if err != nil {
		return nil, err
	}
	r := gjson.GetBytes(doc, "packages")
	if !r.Exists() || r.Type == gjson.Null {
		return nil, nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal([]byte(r.Raw), &list); err != nil {
		return nil, fmt.Errorf("%s: packages isn't a list: it is left as it is", abs)
	}
	return list, nil
}

func piIndex(list []json.RawMessage, source string) int {
	id := piIDOf(source)
	return slices.IndexFunc(list, func(r json.RawMessage) bool {
		pk, ok := piPackageOf(r)
		return ok && piIDOf(pk.Source) == id
	})
}

// syncProjectPi writes the packages the project wants into its
// .pi/settings.json and takes out what magpie wrote that it no longer does.
func (l *Library) syncProjectPi(p *Project, fail func(what string, err error)) {
	want, viaPlugin := l.projectPiWanted(p)
	if len(want) == 0 && len(p.PiRecs) == 0 {
		return
	}
	abs := filepath.Join(p.Dir, filepath.FromSlash(piSettingsRel))
	if err := p.inside(abs); err != nil {
		fail("pi", err)
		return
	}
	list, err := projectPiList(abs)
	if err != nil {
		fail("pi", err)
		return
	}
	changed := false
	var recs []PiRec
	for _, rec := range p.PiRecs {
		i := piIndex(list, rec.Source)
		if i < 0 {
			continue // the user took it out
		}
		if canonical(list[i]) != canonical(rec.Entry) {
			fail("pi:"+rec.Source, fmt.Errorf("%s has been changed since magpie wrote %s: it's left as it is", piSettingsRel, rec.Source))
			continue
		}
		state, wanted := want[rec.Source]
		switch {
		case !wanted:
			changed = true
			if rec.Orig != nil {
				list[i] = rec.Orig
			} else {
				list = slices.Delete(list, i, i+1)
			}
			continue
		default:
			if cur, _ := piPackageOf(list[i]); cur.On != (state == "on") {
				rec.Entry = piEntryFor(rec.Source, state, rec.Orig)
				list[i] = rec.Entry
				changed = true
			}
		}
		recs = append(recs, rec)
	}
	srcs := make([]string, 0, len(want))
	for s := range want {
		srcs = append(srcs, s)
	}
	sort.Strings(srcs)
	for _, s := range srcs {
		if slices.ContainsFunc(recs, func(r PiRec) bool { return r.Source == s }) {
			continue
		}
		if i := piIndex(list, s); i >= 0 {
			if viaPlugin[s] {
				continue // the project declares it already: it loads as it is
			}
			// another build of magpie declared it (project_shapes.go):
			// adopted, so it can be switched, and letting go puts it back
			if pk, _ := piPackageOf(list[i]); p.piFromOld[s] && !pk.Filtered {
				rec := PiRec{Source: s, Entry: list[i], Orig: list[i]}
				if pk.On != (want[s] == "on") {
					rec.Entry = piEntryFor(s, want[s], rec.Orig)
					list[i] = rec.Entry
					changed = true
				}
				recs = append(recs, rec)
				continue
			}
			fail("pi:"+s, fmt.Errorf("%s has %s already: it's the project's own, left as it is until it is adopted", piSettingsRel, s))
			continue
		}
		e := piEntryFor(s, want[s], nil)
		list = append(list, e)
		recs = append(recs, PiRec{Source: s, Entry: e})
		changed = true
	}
	if changed {
		if !exists(abs) && len(list) > 0 {
			if err := p.mkdirs(filepath.Dir(abs)); err != nil {
				fail("pi", err)
				return
			}
			if !slices.Contains(p.MadeFiles, piSettingsRel) {
				p.MadeFiles = append(p.MadeFiles, piSettingsRel)
			}
		}
		var err error
		if len(list) == 0 {
			err = edit.DelJSON(abs, "packages")
		} else {
			err = edit.SetJSON(abs, edit.KV{Path: "packages", Value: list})
		}
		if err != nil {
			fail("pi", err)
			return
		}
	}
	p.PiRecs = recs
	if len(recs) == 0 {
		p.PiRecs = nil
		p.dropMadeFile(piSettingsRel)
	}
}

func checkPiSource(source string) error {
	if !piSourceOK(source) {
		return fmt.Errorf("%q isn't a Pi package source magpie can give a project: npm:<name>[@version] or a git source", source)
	}
	return nil
}

// ProjectPi sets a package of a project's Pi to state: "on", "off" (kept,
// loads nothing) or "removed". A package the project declares already is
// refused until it is adopted.
func ProjectPi(dir, source, state string) (*Result, error) {
	if !slices.Contains([]string{"on", "off", "removed"}, state) {
		return nil, fmt.Errorf("a package is on, off or removed, not %q", state)
	}
	if state != "removed" {
		if err := checkPiSource(source); err != nil {
			return nil, err
		}
	}
	return change(func(l *Library) error {
		p := l.project(dir)
		if p == nil {
			return fmt.Errorf("no project %s", dir)
		}
		if state == "removed" {
			delete(p.Pi, source)
			if len(p.Pi) == 0 {
				p.Pi = nil
			}
			return nil
		}
		if p.Pi == nil {
			p.Pi = map[string]string{}
		}
		p.Pi[source] = state
		return nil
	})
}

// ProjectPiAdopt takes the package the project declares in its
// .pi/settings.json into magpie's keeping without rewriting it: it can then be
// turned off and on, and letting it go puts the entry back as it was. An entry
// that picks some of the package's resources is refused (`pi config` changes
// those).
func ProjectPiAdopt(dir, source string) (*Result, error) {
	return change(func(l *Library) error {
		p := l.project(dir)
		if p == nil {
			return fmt.Errorf("no project %s", dir)
		}
		abs := filepath.Join(p.Dir, filepath.FromSlash(piSettingsRel))
		if err := p.inside(abs); err != nil {
			return err
		}
		list, err := projectPiList(abs)
		if err != nil {
			return err
		}
		i := piIndex(list, source)
		if i < 0 {
			return fmt.Errorf("%s has no package %s", piSettingsRel, source)
		}
		pk, _ := piPackageOf(list[i])
		if pk.Filtered {
			return fmt.Errorf("%s picks some of %s's resources; change it with `pi config`", piSettingsRel, pk.Name)
		}
		if slices.ContainsFunc(p.PiRecs, func(r PiRec) bool { return r.Source == pk.Source }) {
			return errors.New(pk.Source + " is magpie's already")
		}
		state := "on"
		if !pk.On {
			state = "off"
		}
		if p.Pi == nil {
			p.Pi = map[string]string{}
		}
		p.Pi[pk.Source] = state
		p.PiRecs = append(p.PiRecs, PiRec{Source: pk.Source, Entry: list[i], Orig: list[i]})
		return nil
	})
}

// piViews is the project's packages by magpie, for its card.
func (p *Project) piViews() []ProjectPiView {
	var out []ProjectPiView
	for s, st := range p.Pi {
		name, _ := piPackageName(s)
		if name == "" {
			name = s
		}
		out = append(out, ProjectPiView{Source: s, Name: name, State: st,
			Adopted: slices.ContainsFunc(p.PiRecs, func(r PiRec) bool { return r.Source == s && r.Orig != nil })})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out
}

// piManifest says whether a folder is a Pi package: a package.json with a
// `pi` object (skills, extensions, prompts, themes).
func piManifest(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	return gjson.GetBytes(b, "pi").IsObject()
}

// projectPiWanted are the packages the project has: those it was given by
// source, and, for each library plugin it gives Pi that is a Pi package, the
// plugin's folder (#7243), one entry however often it is applied. viaPlugin
// marks the latter. A source given by hand wins.
func (l *Library) projectPiWanted(p *Project) (want map[string]string, viaPlugin map[string]bool) {
	want = map[string]string{}
	viaPlugin = map[string]bool{}
	for s, st := range p.Pi {
		want[s] = st
	}
	for name, agents := range p.Plugins {
		b := l.plugin(name)
		if b == nil || b.Off || !slices.Contains(agents, "pi") {
			continue
		}
		dir := pluginDir(name)
		if !piManifest(dir) {
			continue
		}
		if _, ok := want[dir]; !ok {
			want[dir] = "on"
			viaPlugin[dir] = true
		}
	}
	return want, viaPlugin
}
