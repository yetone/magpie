package library

import (
	"os"
	"path/filepath"
	"strings"
)

// An old copy of a skill is the one a swap moved aside (copyIn) and
// couldn't remove, tried again on the next sync. Looking for them read the
// whole skills folder once for each skill in it, so a sync read an
// agent's folder as many times as it had skills: 150 skills given to six
// agents took a few hundred milliseconds of folder reading for each tick
// of one skill's agent, more on Windows, and grew with the square of the
// skills (gandli on X). A change reads each folder once now, and drops the
// old copies it found there by skill.

// readOlds is os.ReadDir; a test counts the reads
var readOlds = os.ReadDir

// oldCopies are the old copies in dir, by the skill they are of.
func oldCopies(dir string) map[string][]string {
	es, err := readOlds(dir)
	if err != nil {
		return nil
	}
	var out map[string][]string
	for _, e := range es {
		n := e.Name()
		if name, ok := oldOf(n); ok {
			if out == nil {
				out = map[string][]string{}
			}
			out[name] = append(out[name], n)
		}
	}
	return out
}

// oldOf is the skill an entry by the name n is an old copy of: named by
// oldPrefix, with a number after it or none (oldPath).
func oldOf(n string) (string, bool) {
	const mark = ".magpie-old"
	if !strings.HasPrefix(n, ".") {
		return "", false
	}
	for i := strings.Index(n, mark); i >= 0; {
		name, rest := n[1:i], n[i+len(mark):]
		if name != "" && (rest == "" || rest[0] == '-' && isDigits(rest[1:])) {
			return name, true
		}
		j := strings.Index(n[i+1:], mark)
		if j < 0 {
			break
		}
		i += 1 + j
	}
	return "", false
}

// dropOld is dropOld for a change, which reads each folder for its old
// copies once. One a swap of this change leaves behind is tried on the
// next, as before.
func (l *Library) dropOld(p, name string) {
	dir := filepath.Dir(p)
	if l.olds == nil {
		l.olds = map[string]map[string][]string{}
	}
	o, ok := l.olds[dir]
	if !ok {
		o = oldCopies(dir)
		l.olds[dir] = o
	}
	for _, n := range o[name] {
		os.RemoveAll(filepath.Join(dir, n))
	}
	delete(o, name)
}
