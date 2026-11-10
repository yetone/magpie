package sessions

import (
	"bytes"
	"maps"
	"runtime"
	"sync"
)

// The Sessions page answers before the files it has never read are read
// whole (hey_boyce on X: the page showed its placeholder rows for a long
// time on its first open). A first read of a long history (13 GB of Claude
// Code's and 52 GB of Codex's files on one computer) takes minutes, and
// Manage used to read all of it before it answered: the agent shown, and
// every agent with a gateway session for its ids, Codex's too while Claude
// Code was shown. It still reads at once what is cheap: a file it has a
// parse of that has only grown, read on from where the parse left off. A
// file it would read whole (none kept, an older magpie's index, a file
// written over, or a Claude Code or Codex file whose kept parse can't be
// read on after a restart) is read behind the answer, as Stats and List
// read it, and until then its row stands on what is known now: the parse
// kept of it, or a glance at its first lines (its id, folder, first prompt
// and start), with its last activity the file's time. Such a row says it
// is Pending, and the page asks again until none is.

// glanceBytes is how far into a file a glance reads at most.
const glanceBytes = 256 << 10

// quickBytes is how much reading whole the page still waits for: a store
// that small is read in well under a second, even on a slow disk, and its
// rows come complete the first time.
var quickBytes int64 = 32 << 20

var (
	// glances are the files glanced at, by path, while their whole read
	// is under way; each stands for the size and time it was read at
	glances = map[string]*state{}
	// overlaid are the glances put into the cache for one pass of
	// Manage, never saved with it
	overlaid = map[string]*state{}
	// behind are the paths a read behind an answer has taken, and
	// behindRuns the reads, waited for by Reset
	behind     = map[string]bool{}
	behindRuns sync.WaitGroup
)

// readsWhole says whether bringing f's parse up to date reads the file
// from its start: no parse of it is kept, or the one kept can't be read on.
// Only the agents read line by line can be glanced at; the others are read
// as before.
func readsWhole(f file, old *state) bool {
	p := parserFor(f.agent)
	if p.line == nil {
		return false
	}
	if old == nil || f.size < old.Size || old.Off > f.size {
		return true
	}
	if f.agent == "codex" && old.Codex == nil {
		return true
	}
	return p.claude && old.Claude == nil
}

func current(f file, s *state) bool {
	return s != nil && s.Size == f.size && s.Mod == f.mod.UnixNano()
}

// quickRead is refresh for the Sessions page, under mu: what is cheap is
// read now, and what would be read whole is read behind. pending are the
// paths not read yet, and done takes the glances back out of the cache;
// nothing may let mu go between the two.
func quickRead(want, all []file) (pending map[string]bool, done func()) {
	var now, later []file
	for _, f := range want {
		if f.cold {
			continue
		}
		if old := cache[f.path]; !current(f, old) && readsWhole(f, old) {
			later = append(later, f)
			continue
		}
		now = append(now, f)
	}
	var whole int64
	for _, f := range later {
		whole += f.size
	}
	if whole <= quickBytes {
		now, later = append(now, later...), nil
	}
	refresh(now, all)
	pending = map[string]bool{}
	var start, look []file
	for _, f := range later {
		old := cache[f.path]
		if current(f, old) {
			continue // read meanwhile
		}
		pending[f.path] = true
		if !behind[f.path] {
			start = append(start, f)
		}
		if old == nil && f.main && !current(f, glances[f.path]) {
			look = append(look, f)
		}
	}
	if len(start) > 0 {
		readBehind(start)
	}
	if len(look) > 0 {
		mu.Unlock()
		seen := glanceAll(look)
		mu.Lock()
		for i, f := range look {
			if seen[i] != nil {
				glances[f.path] = seen[i]
			}
		}
	}
	put := map[string]*state{}
	for p := range pending {
		if cache[p] == nil && glances[p] != nil {
			put[p] = glances[p]
		}
	}
	for p, g := range put {
		cache[p], overlaid[p] = g, g
	}
	return pending, func() {
		for p, g := range put {
			if cache[p] == g {
				delete(cache, p)
			}
			delete(overlaid, p)
		}
	}
}

// readBehind reads fs whole after the answer, as refresh does, and lets
// their glances go once read.
func readBehind(fs []file) {
	for _, f := range fs {
		behind[f.path] = true
	}
	behindRuns.Add(1)
	go func() {
		defer behindRuns.Done()
		storeMu.RLock()
		defer storeMu.RUnlock()
		mu.Lock()
		defer mu.Unlock()
		loadCache()
		refresh(fs, nil)
		for _, f := range fs {
			delete(behind, f.path)
			delete(glances, f.path)
		}
	}()
}

// glanceAll glances at each of fs, a few at a time.
func glanceAll(fs []file) []*state {
	out := make([]*state, len(fs))
	ch := make(chan int)
	var wg sync.WaitGroup
	for range min(max(runtime.NumCPU()/2, 1), len(fs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				out[i] = glance(fs[i])
			}
		}()
	}
	for i := range fs {
		ch <- i
	}
	close(ch)
	wg.Wait()
	return out
}

// glance is what f's first lines say of its session: its id, folder,
// title and start, read with its agent's own parser, and none of what it
// spent, which only a whole read can count. Its last activity is the
// file's time.
func glance(f file) *state {
	p := parserFor(f.agent)
	s := &state{}
	var head func([]byte) bool
	var thin func([]byte) (thinner, bool)
	switch {
	case p.claude:
		thin = thinClaude
	case f.agent == "codex":
		head = func(b []byte) bool { return codexHead(s, b, f.main) }
		thin = thinCodex
	}
	idIn := idInFile(f.agent)
	_, err := scanThin(f.path, 0, head, thin, func(b []byte, _, end int64) bool {
		p.line(s, b, f.main)
		if s.Cwd != "" && s.Title != "" && (!idIn || s.ID != "") {
			return false
		}
		return end < glanceBytes
	})
	if err != nil {
		return nil
	}
	if p.claude && f.main {
		claudeTail(s, f)
	}
	if isBuddy(f.agent) && s.Cwd == "" {
		workbuddyMeta(s, f.path)
	}
	g := &state{Size: f.size, Mod: f.mod.UnixNano(), ID: s.ID, Cwd: s.Cwd, Title: s.Title,
		Start: s.Start, Last: f.mod.Time(), CodexIdentity: s.CodexIdentity, Provider: s.Provider,
		StateMore: moreOf(StateMore{Named: s.more().Named, Custom: s.more().Custom, First: s.more().First,
			CodexNickname: s.more().CodexNickname})}
	if g.Start.IsZero() || g.Start.After(g.Last) {
		g.Start = g.Last
	}
	return g
}

// parsed is the parse kept of path: none for a glance put in the cache,
// which a refresh reads whole as though nothing were kept.
func parsed(path string) *state {
	s := cache[path]
	if s != nil && overlaid[path] == s {
		return nil
	}
	return s
}

// tailBytes is how much of a Claude Code file's end a glance reads for its
// titles.
const tailBytes = 64 << 10

// claudeTail reads the titles at the end of a Claude Code file into s: the
// one Claude Code gives a session, and one the user gave it, are lines it
// adds as the session goes on, the last of each the one that stands. So is
// /cd's relocated line, naming the folder the session was moved to.
func claudeTail(s *state, f file) {
	from := max(f.size-tailBytes, 0)
	p := parserFor(f.agent)
	scanThin(f.path, from, nil, nil, func(b []byte, start, _ int64) bool {
		if start == from && from > 0 {
			return true // the end of a line begun before
		}
		if bytes.Contains(b, ccTitle) || bytes.Contains(b, ccCustom) || bytes.Contains(b, ccRelocatedCwd) {
			p.line(s, b, true)
		}
		return true
	})
}

// savable is the cache as it is saved: without the glances put in it.
func savable() map[string]*state {
	c := maps.Clone(cache)
	for p, g := range overlaid {
		if c[p] == g {
			delete(c, p)
		}
	}
	return c
}

// forgetBehind waits for the reads behind answers, and forgets the glances
// (Reset).
func forgetBehind() {
	behindRuns.Wait()
	mu.Lock()
	glances, overlaid, behind = map[string]*state{}, map[string]*state{}, map[string]bool{}
	mu.Unlock()
}
