package sessions

import (
	"slices"
	"sync"
	"unsafe"
	"weak"
)

// filesKept is the last list of every agent's session files a pass made,
// held weakly. A Sessions poll lists ~20k files, and while none of them
// changed it made the same list again, ~1.5 MB a poll (the largest share
// of what a poll allocates, and so of its garbage collection). A pass
// that lists the files as the last one did gives that list back instead,
// and allocates nothing for it. The list is held only as long as
// something else holds it or no collection has run since: a magpie with
// the page shut keeps none of it.
type filesKept struct {
	sync.Mutex
	first weak.Pointer[file]
	n     int
}

// allKept are allFiles' and allFilesForTitles' last lists.
var allKept [2]filesKept

func (k *filesKept) last() []file {
	k.Lock()
	defer k.Unlock()
	if k.n == 0 {
		return nil
	}
	p := k.first.Value()
	if p == nil {
		return nil
	}
	return unsafe.Slice(p, k.n)
}

// distinct is distinctOf(lists...), given as the last list it made when
// it is the same, file for file. Its readers only read it (heldFiles
// gives one list to every reader under a hold already), and it is
// clipped, so an append to it is made elsewhere.
func (k *filesKept) distinct(lists ...[]file) []file {
	last := k.last()
	seen := pathSet()
	defer putPathSet(seen)
	var out []file // nil while each file is last's at its place
	i := 0
	for _, fs := range lists {
		for _, f := range fs {
			if _, ok := seen[f.path]; ok {
				continue
			}
			seen[f.path] = struct{}{}
			if out == nil {
				if i < len(last) && last[i] == f {
					i++
					continue
				}
				n := 0
				for _, fs := range lists {
					n += len(fs)
				}
				out = append(make([]file, 0, n), last[:i]...)
			}
			out = append(out, f)
		}
	}
	if out == nil {
		if i == len(last) && i > 0 {
			return last
		}
		// files gone from its end; never nil, which callers read as "not
		// listed" and keep every parse of files that are gone for
		out = append(make([]file, 0, i), last[:i]...)
	}
	out = out[:len(out):len(out)]
	k.Lock()
	k.first, k.n = weak.Pointer[file]{}, len(out)
	if len(out) > 0 {
		k.first = weak.Make(&out[0])
	}
	k.Unlock()
	return out
}

// sameFiles builds a lister's list a file at a time against the list it
// made last, and allocates nothing while each file is the last list's at
// its place. Every 15 s Sessions poll listed every rollout and Claude Code
// file again into lists made new, ~2.5 MB a poll on a real history while
// nothing in them had changed, and that garbage was a third of a poll's
// collections (#2278). Its lists are only read (heldFiles' contract), and
// clipped, so an append to one is made elsewhere.
type sameFiles struct {
	last, out []file
	i         int
	made      bool
	hint      int // the least room out is made with
}

func (s *sameFiles) add(f file) {
	if !s.made {
		if s.i < len(s.last) && s.last[s.i] == f {
			s.i++
			return
		}
		s.made = true
		s.out = append(make([]file, 0, max(len(s.last)+len(s.last)/16, s.hint)), s.last[:s.i]...)
	}
	s.out = append(s.out, f)
}

// n is how many files were added.
func (s *sameFiles) n() int {
	if s.made {
		return len(s.out)
	}
	return s.i
}

// list is the files added, the last list itself when they are all of it,
// and whether it is another list than the last.
func (s *sameFiles) list() ([]file, bool) {
	switch {
	case s.made:
		return slices.Clip(s.out), true
	case s.i == len(s.last):
		return s.last, false
	}
	// files gone from its end
	return append(make([]file, 0, s.i), s.last[:s.i]...), true
}

// listsKept are the listers' last lists, by lister and folder. Unlike
// filesKept's they are held strongly: nothing else holds a lister's list
// from one poll to the next. forgetListsKept lets them go when the cache
// does.
var listsKept sync.Map // key → []file

func keptFiles(key string) []file {
	l, _ := listsKept.Load(key)
	fs, _ := l.([]file)
	return fs
}

// sameAsKept is s's list, kept under key for the next pass when it is new.
func sameAsKept(key string, s *sameFiles) []file {
	l, changed := s.list()
	if changed {
		listsKept.Store(key, l)
	}
	return l
}

// concatKept is lists one after another: the one list itself when the
// others are empty, else the last such list when it is the same.
func concatKept(key string, lists ...[]file) []file {
	var one []file
	n := 0
	for _, l := range lists {
		if len(l) > 0 {
			n++
			one = l
		}
	}
	if n <= 1 {
		return one
	}
	s := sameFiles{last: keptFiles(key)}
	for _, l := range lists {
		for _, f := range l {
			s.add(f)
		}
	}
	return sameAsKept(key, &s)
}

func forgetListsKept() {
	listsKept.Clear()
	ccScratch.Clear()
}
