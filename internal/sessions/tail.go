package sessions

import "os"

// tail is where a reader of a file another agent appends to left off: the
// offset after the last whole line it handled, and enough of the file as it
// was to know it again. A summary kept with one reads on from Off when the
// file has only grown, instead of reading it whole again (dsh's, Grok
// Build's, Droid's and Kimi Code's sessions are kept so). A file that
// shrank, or whose start or sampled prefix is no longer what was read, is
// read whole again: it was rewritten or replaced, not appended to.
//
// A packed file (frames of zstd, frames.go) is read on from the frame at
// Off, Skip bytes into what that frame holds, and Size there is the end of
// that frame: the bytes hashed always take in what is read again.
type tail struct {
	Off      int64  `json:"off"`
	Skip     int64  `json:"skip,omitempty"`
	Size     int64  `json:"size"`
	Head     string `json:"head"`
	HeadSize int    `json:"head_size"`
	Hash     string `json:"hash"`
}

// markTail is the tail of a file read up to off, its first size bytes the
// ones read and to be found again.
func markTail(path string, off, skip, size int64) *tail {
	head := headOf(path)
	if int64(len(head)) > size {
		head = head[:size]
	}
	h := prefixHash(path, size)
	if h == "" && size > 0 {
		return nil
	}
	return &tail{Off: off, Skip: skip, Size: size, Head: hashHead(head), HeadSize: len(head), Hash: h}
}

// holds says whether the file at path is still the one t was read from,
// grown or as it was, so it can be read on from t.Off.
func (t *tail) holds(path string) bool {
	if t == nil {
		return false
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Size() < t.Size || t.Off > t.Size {
		return false
	}
	return sameHead(headOf(path), t.Head, t.HeadSize) && prefixHash(path, t.Size) == t.Hash
}

// dropUse takes back what use counted, leaving no model or day it made
// with nothing in it: a summary read on from where it was is then the one
// a whole read makes.
func (s *state) dropUse(date, model string, t Tokens) {
	if t.zero() {
		return
	}
	s.unuse(date, model, t)
	if m, ok := s.Models.get(model); ok && m.zero() {
		s.Models.del(model)
	}
	if d := s.Days.of(date); d != nil {
		if m, ok := d.Models.get(model); ok && m.zero() {
			d.Models.del(model)
		}
		if len(d.Models) == 0 && d.Active == 0 && d.Hours == nil && d.Prompts == 0 && d.Replies == 0 && d.Tools == nil && d.Skills == nil {
			s.Days.del(date)
		}
	}
}
