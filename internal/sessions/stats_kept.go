package sessions

import (
	"reflect"
	"slices"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// keptStats are the ranges statsReadAll last read, and what they were read
// from. The Usage page asks for its range every 10 s and the Sessions page
// for 7, 30 and every day every 15 s; between two polls the files mostly
// haven't changed, and each read put every session of every range together
// again: ~1.1 GB a minute and a third, a fifth of all magpie allocated
// (#2278). Guarded by mu.
var keptStats struct {
	files  []keptFile
	today  time.Time
	gen    uint64
	ranges map[int]Stats
	// native is every listed session's agent|id, nil when no read asked
	// for it; shared, so read only
	native map[string]bool
	// the answers the reads got from the prices, Codex's names and Claude
	// Desktop's, asked again before the ranges are given out
	prices  map[string]*catalog.Price
	titles  titleAnswers
	desktop map[string]string
}

// titleAnswers are the answers a titles func gave, to be asked again
// (keptStill). Nearly every id has no title, and a map of every session's
// id held 3MB for a history of 18k files (#2278): an id answered "" is
// only listed, sorted and without repeats once settled.
type titleAnswers struct {
	named map[string]string
	none  []string
}

func (a *titleAnswers) note(id, t string) {
	if t == "" {
		a.none = append(a.none, id)
		return
	}
	if a.named == nil {
		a.named = map[string]string{}
	}
	a.named[id] = t
}

// settle sorts none and drops its repeats, and its slack.
func (a *titleAnswers) settle() {
	slices.Sort(a.none)
	a.none = slices.Clip(slices.Compact(a.none))
}

// still reports whether titles answers each id as it did.
func (a *titleAnswers) still(titles func(string) string) bool {
	for id, t := range a.named {
		if titles(id) != t {
			return false
		}
	}
	for i, id := range a.none {
		if (i == 0 || id != a.none[i-1]) && titles(id) != "" {
			return false
		}
	}
	return true
}

// keptFile is what of a listed file a range's stats are made from: its
// parse (a published parse is never written, a changed file gets a new
// one), and what the listing says of it.
type keptFile struct {
	path, key, agent, wsl string
	size                  int64
	mod                   int64
	main, readOnly        bool
	st                    *state
}

func keptFileOf(f file) keptFile {
	return keptFile{path: f.path, key: f.key, agent: f.agent, wsl: f.m().wsl, size: f.size, mod: f.mod.UnixNano(),
		main: f.main, readOnly: f.readOnly, st: cache[f.path]}
}

// keptFor reports whether the kept ranges were read from files as they
// are now, today, with the prices and names they got still the same. When
// they weren't, what was kept is let go.
func keptFor(files []file, today time.Time, price func(string) *catalog.Price, titles func(string) string) bool {
	k := &keptStats
	same := k.ranges != nil && k.gen == cacheGeneration && k.today.Equal(today) &&
		keptStill(k.files, files, k.prices, &k.titles, k.desktop, price, titles)
	if !same {
		forgetKeptStats()
		k.files, k.today, k.gen = make([]keptFile, len(files)), today, cacheGeneration
		for i, f := range files {
			k.files[i] = keptFileOf(f)
		}
		k.ranges, k.prices, k.titles, k.desktop = map[int]Stats{}, map[string]*catalog.Price{}, titleAnswers{}, map[string]string{}
	}
	return same
}

// keptStill reports whether kept was taken of files as they are now, and
// prices, titles and desktop, the answers what was made from them got, are
// still what price, titles and Claude Desktop's names answer.
func keptStill(kept []keptFile, files []file, prices map[string]*catalog.Price, names *titleAnswers, desktop map[string]string,
	price func(string) *catalog.Price, titles func(string) string) bool {
	if len(kept) != len(files) {
		return false
	}
	for i := range files {
		if kept[i] != keptFileOf(files[i]) {
			return false
		}
	}
	for model, p := range prices {
		if !reflect.DeepEqual(price(model), p) {
			return false
		}
	}
	if !names.still(titles) {
		return false
	}
	if len(desktop) > 0 {
		ids := make([]string, 0, len(desktop))
		for id := range desktop {
			ids = append(ids, id)
		}
		return reflect.DeepEqual(DesktopTitles(ids), desktopKnown(desktop))
	}
	return true
}

// keptRanges is how many ranges are kept: the page asks for three, and a
// days= of any other number is not kept beside them for ever.
const keptRanges = 8

// keepRange keeps st as days' Stats, letting go of a range asked is not
// among when there are keptRanges already.
func keepRange(days int, st Stats, asked []int) {
	if len(keptStats.ranges) >= keptRanges {
		for d := range keptStats.ranges {
			if !slices.Contains(asked, d) {
				delete(keptStats.ranges, d)
				break
			}
		}
	}
	keptStats.ranges[days] = st
}

// desktopKnown is names without the ids that have none, as DesktopTitles
// gives them.
func desktopKnown(names map[string]string) map[string]string {
	out := map[string]string{}
	for id, n := range names {
		if n != "" {
			out[id] = n
		}
	}
	return out
}

// keptPrice and keptTitle answer as price and titles do, and note the
// answer for keptFor.
func keptPrice(price func(string) *catalog.Price) func(string) *catalog.Price {
	return func(model string) *catalog.Price {
		p := price(model)
		keptStats.prices[model] = p
		return p
	}
}

func keptTitle(titles func(string) string) func(string) string {
	return func(id string) string {
		t := titles(id)
		keptStats.titles.note(id, t)
		return t
	}
}

// keepDesktop notes the Claude Desktop names a range's sessions got.
func keepDesktop(st Stats) {
	var ids []string
	for _, s := range st.Sessions {
		if s.Agent == "claude-desktop" {
			ids = append(ids, s.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	names := DesktopTitles(ids)
	for _, id := range ids {
		keptStats.desktop[id] = names[id]
	}
}

func forgetKeptStats() {
	keptStats.files, keptStats.ranges, keptStats.native = nil, nil, nil
	keptStats.prices, keptStats.titles, keptStats.desktop = nil, titleAnswers{}, nil
}

// ForgetKeptRanges lets go of the ranges' Stats kept for the next read,
// and what they were checked against, once no page has asked for a while.
// The parsed files stay: a page that asks again sums them up again
// (~0.1 s for a long history) and reads no file.
func ForgetKeptRanges() {
	mu.Lock()
	defer mu.Unlock()
	forgetKeptStats()
}
