package library

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// A skill from GitHub or another git repository whose folder is still
// the files fetched from there goes to the other computers as its source
// (#5715, JJasonsun on Discord), as a plugin does (plugin_carry.go): each
// computer fetches it itself, so a skill's size never counts against a
// sync's (a sync reads at most 64 MiB, maxCarriedAll). It still carries
// what of its files fit in the room the skills carried as files leave, so
// a magpie from before this, which knows nothing of Fetch, writes the
// skill as it did; on this one they only stand in until it is fetched.
//
// A skill changed here since it was fetched, one from a folder, and one
// from a link holding a token (whose link must not be synced) go as their
// files, as before.

// bySource reports whether Collect carries s as its source.
func (l *Library) bySource(s *Skill) bool {
	if !s.Source.remote() || s.Hash == "" || signedIn(s.Source.String()) {
		return false
	}
	if s.Pending { // still the version the other computer has, to fetch
		return true
	}
	edited, known := l.editState(s, nil)
	return known && !edited
}

// fromSource is what Put does with a skill carried as its source.
type fromSource struct {
	keep              bool // the folder here stays as it is
	hash, own, commit string
	pending, fetch    bool
	changed           bool // edited here while the other computer's is another version
}

// fromSourceHere decides how a skill carried as its source is put here:
// nil for one carried as its files. The folder here stays when it is the
// same version, or changed here (an update asks before replacing that),
// or the same source at another version, which is fetched after. One new
// here is written from the files carried and fetched after, unless they
// are the whole of it already.
func (l *Library) fromSourceHere(c *CarriedSkill) *fromSource {
	if !c.Fetch || !c.Source.remote() || c.Hash == "" || signedIn(c.Source.String()) {
		return nil
	}
	cur := l.skill(c.Name)
	if cur == nil || !sameSource(cur.Source, c.Source) {
		return &fromSource{hash: c.Hash, commit: c.Commit, pending: true, fetch: true}
	}
	if cur.Pending {
		return &fromSource{keep: true, hash: c.Hash, commit: c.Commit, pending: true, fetch: true}
	}
	edited, known := l.editState(cur, nil)
	switch {
	case edited:
		// this computer's changes stay; a skill the other computer has
		// at the version this one was fetched at is no conflict
		return &fromSource{keep: true, hash: cur.Hash, own: cur.Own, commit: cur.Commit, changed: cur.Hash != c.Hash}
	case known && cur.Hash == c.Hash:
		return &fromSource{keep: true, hash: cur.Hash, own: cur.Own, commit: cur.Commit}
	}
	// another version, or one not known unchanged: fetched, what is here
	// now kept with the backups as an update keeps it
	return &fromSource{keep: true, hash: c.Hash, commit: c.Commit, pending: true, fetch: true}
}

// whole is whether the files carried for a skill new here, written at
// dir, are all of the version it was carried at: then nothing is fetched.
func (p *fromSource) whole(dir string, c *CarriedSkill) {
	if len(c.Left) == 0 && hashDir(dir) == c.Hash {
		p.pending, p.fetch, p.own = false, false, ownHash(dir)
	}
}

// fetchSkills fetches from their sources the skills Put brought by them,
// after the library's lock is let go, as UpdateSomeSkills does. One that
// can't be fetched stays Pending with a problem; the next sync, or
// Update, tries again.
func fetchSkills(names []string, res *Result) {
	if len(names) == 0 {
		return
	}
	r, err := updateSkills(names)
	if err != nil {
		for _, n := range names {
			res.Problems = append(res.Problems, notFetched(n, err.Error()))
		}
		return
	}
	for _, p := range r.Unupdated {
		res.Problems = append(res.Problems, notFetched(strings.TrimPrefix(p.What, "skill:"), p.Error))
	}
	for _, n := range r.Gone {
		res.Problems = append(res.Problems, notFetched(n, "its source doesn't have it any more"))
	}
	for _, a := range r.Changed {
		if !slices.Contains(res.Changed, a) {
			res.Changed = append(res.Changed, a)
		}
	}
	res.Problems = append(res.Problems, r.Problems...)
}

func notFetched(name, why string) Problem {
	return Problem{What: "skill:" + name, Error: "not fetched from its source on this computer yet (Update tries again): " + why}
}

// changedHere is the problem of a skill kept as it was changed here while
// the other computer has another version of it.
func changedHere(name string) Problem {
	return Problem{What: "skill:" + name, Error: fmt.Sprintf("%s was changed on this computer, and another computer has another version from its source: "+
		"the changes here are kept; Update replaces them with its source's version", name)}
}

// Unsettled is the problems a sync's Put said that still hold: a skill's
// while it is still to be fetched (Pending) or changed here, and none
// else (a plugin's is said on its row). A library that can't be read
// keeps them all.
func Unsettled(probs []Problem) []Problem {
	if len(probs) == 0 {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	l, err := load()
	if err != nil {
		return probs
	}
	var out []Problem
	for _, p := range probs {
		name, ok := strings.CutPrefix(p.What, "skill:")
		if !ok {
			continue
		}
		s := l.skill(name)
		if s == nil {
			continue
		}
		if edited, _ := l.editState(s, nil); s.Pending || edited {
			out = append(out, p)
		}
	}
	return out
}

// notIn is the files of rels that the folder dir doesn't have.
func notIn(dir string, rels []string) []string {
	var out []string
	for _, rel := range rels {
		if fi, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil || !fi.Mode().IsRegular() {
			out = append(out, rel)
		}
	}
	return out
}

// missing adds to what Collect carries of s the files a sync brought it
// without that are still not here (Skill.Missing): left out, as on the
// computer that has them.
func missing(c *CarriedSkill, s *Skill) {
	if c == nil || len(s.Missing) == 0 {
		return
	}
	for _, rel := range notIn(realDir(skillDir(s.Name)), s.Missing) {
		// An older bundle could leave out installed dependencies as too
		// large, but these are no longer part of the carried skill
		parts := strings.Split(rel, "/")
		installed := false
		for i, name := range parts {
			if fromInstall(name, i < len(parts)-1, false) {
				installed = true
				break
			}
		}
		if installed {
			continue
		}
		if _, ok := c.Files[rel]; !ok && !slices.Contains(c.Left, rel) {
			c.Left = append(c.Left, rel)
		}
	}
	slices.Sort(c.Left)
}
