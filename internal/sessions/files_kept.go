package sessions

import (
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
