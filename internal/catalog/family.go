package catalog

import (
	"slices"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
)

// A model's family is its id with the version numbers taken out, so that
// claude-opus-4-8 and claude-opus-5-5 are claude-opus, o3 and o1 are o, and
// o4-mini is o-mini. Nothing about a vendor is compiled in: what is read
// off the id alone is
//
//   - a version: a number (4, 4.5, the 4 and 5 of claude-opus-4-5), the
//     digits a word ends in (qwen3, o4, k2), a v and its digits (v3,
//     v1:0), or the digits a word starts with that aren't a size (gpt-4o
//     is gpt-o, glm-4.5v glm-v);
//   - a size, which stays in the name: 8b, 70b, 1.5b, a22b, 8x7b, 128e,
//     32k, so qwen3-32b and qwen3-8b are two families;
//   - a snapshot: a date or revision after the version (20251101, 0528,
//     2507, 2024-08-06, 05-20, 001), read as the alias it was taken of;
//   - a preview (preview, exp, beta): the same model before its release,
//     so gemini-3-pro-preview is a gemini-pro;
//   - latest, an alias of the newest, which is the alias too.
//
// Every other word stays in the name: -mini, -pro, -nano, -flash,
// -flash-lite, -codex, -thinking, -instruct, :free, and a provider prefix
// (anthropic/, us.anthropic.).

// Family is what an id's name says about it: the family it belongs to and
// where it stands in it.
type Family struct {
	Name     string // the id without its version, snapshot or preview
	Version  []int  // every version number, in the order the id has them
	Snapshot string // its dates or revision, "" for an alias
	Preview  bool
}

var (
	familySep = lazyre.New(`[-_/:@\[\]\s]+`)
	// a number: 4, 4.5, 2.0
	familyNum = lazyre.New(`^\d+(?:\.\d+)*$`)
	// a word that ends in its version: qwen3, qwen2.5, o4, k2, v1, llama3.1
	familyTail = lazyre.New(`^([a-z][a-z.]*?[a-z])(\d+(?:\.\d+)*)$|^([a-z])(\d+(?:\.\d+)*)$`)
	// a word that starts with its version: 4o, 4.5v, 3n
	familyHead = lazyre.New(`^(\d+(?:\.\d+)*)([a-z]+)$`)
	// a size, kept in the name: 8b, 1.5b, 70b, 128e, 32k, 8x7b, a22b, e4b
	familySize = lazyre.New(`^(?:[a-z]?\d+(?:\.\d+)?|\d+x\d+(?:\.\d+)?)[bmkte]$`)
	familyYear = lazyre.New(`^(?:19|20)\d\d$`)
)

var familyPreview = map[string]bool{"preview": true, "exp": true, "experimental": true, "beta": true, "alpha": true}

// FamilyOf reads id's family off its name.
func FamilyOf(id string) Family {
	s := strings.ToLower(strings.TrimSpace(id))
	words := familySep.Split(s, -1)
	seps := familySep.FindAllString(s, -1)
	// which purely numeric words are a snapshot rather than a version
	snap := make([]bool, len(words))
	for i, w := range words {
		if !familyNum.MatchString(w) || strings.Contains(w, ".") {
			continue
		}
		// a date or revision: 0528, 2507, 20251101, 2024, 001, 05
		if len(w) >= 4 || len(w) >= 2 && w[0] == '0' {
			snap[i] = true
		}
	}
	for i, w := range words {
		if snap[i] || !familyNum.MatchString(w) || len(w) != 2 {
			continue
		}
		// the day after a month (05-20), the month before a year (12-2024)
		if i > 0 && snap[i-1] || i+1 < len(words) && familyYear.MatchString(words[i+1]) {
			snap[i] = true
		}
	}
	var f Family
	var name, snaps strings.Builder
	keep := func(i int, w string) {
		if name.Len() > 0 && i > 0 {
			name.WriteString(seps[i-1])
		}
		name.WriteString(w)
	}
	addVersion := func(v string) {
		for _, n := range strings.Split(v, ".") {
			x, err := strconv.Atoi(n)
			if err != nil {
				x = 0
			}
			f.Version = append(f.Version, x)
		}
	}
	for i, w := range words {
		switch {
		case w == "":
		case snap[i]:
			if snaps.Len() > 0 {
				snaps.WriteString("-")
			}
			snaps.WriteString(w)
		case familyPreview[w]:
			f.Preview = true
		case w == "latest":
		case familyNum.MatchString(w):
			addVersion(w)
		case familySize.MatchString(w):
			keep(i, w)
		default:
			if m := familyTail.FindStringSubmatch(w); m != nil {
				word, v := m[1], m[2]
				if word == "" {
					word, v = m[3], m[4]
				}
				if word != "v" { // v3, v1: the version alone
					keep(i, word)
				}
				addVersion(v)
			} else if m := familyHead.FindStringSubmatch(w); m != nil {
				addVersion(m[1])
				keep(i, m[2])
			} else {
				keep(i, w)
			}
		}
	}
	f.Name, f.Snapshot = name.String(), snaps.String()
	if f.Name == "" {
		f.Name = s // all numbers: a family of its own
	}
	return f
}

// newerInFamily says a, of the same family as b, is the one to serve over
// it: the higher version; at the same version a release over a preview,
// then the alias over a dated snapshot (the alias names the vendor's
// current one), then the later release date, then the later snapshot.
func newerInFamily(a, b Model, fa, fb Family) bool {
	if c := slices.Compare(fa.Version, fb.Version); c != 0 {
		return c > 0
	}
	if fa.Preview != fb.Preview {
		return fb.Preview
	}
	if (fa.Snapshot == "") != (fb.Snapshot == "") {
		return fa.Snapshot == ""
	}
	if a.Released != b.Released {
		return a.Released > b.Released
	}
	return fa.Snapshot > fb.Snapshot
}

// BestInFamily is ms with only the newest model of each family left
// (FamilyOf, newerInFamily), in the order ms has them. A family's first
// listed wins a tie.
func BestInFamily(ms []Model) []Model {
	fams := make([]Family, len(ms))
	best := map[string]int{}
	for i, m := range ms {
		fams[i] = FamilyOf(m.ID)
		j, ok := best[fams[i].Name]
		if !ok || newerInFamily(m, ms[j], fams[i], fams[j]) {
			best[fams[i].Name] = i
		}
	}
	out := make([]Model, 0, len(best))
	for i, m := range ms {
		if best[fams[i].Name] == i {
			out = append(out, m)
		}
	}
	return out
}
