package sessions

import "hash/fnv"

// uuidSet is a set of message uuids, each kept as its 16 bytes rather
// than its 36-character text: Claude Code's sessions that start alike
// keep every message's uuid (ccUUIDMemo), and as strings in a
// map[string]bool they were ~5 MB of an idle heap of ~40. An id that is
// not a uuid is kept by a 128-bit hash of it.
type uuidSet map[[16]byte]struct{}

func (s uuidSet) add(id string)      { s[uuidKey(id)] = struct{}{} }
func (s uuidSet) has(id string) bool { _, ok := s[uuidKey(id)]; return ok }

// uuidKey is id's 16 bytes when it is a uuid (8-4-4-4-12 hex digits, any
// case), else an FNV-128a hash of it, which a uuid's bytes are told
// apart from by nothing but chance.
func uuidKey(id string) (k [16]byte) {
	if len(id) == 36 && id[8] == '-' && id[13] == '-' && id[18] == '-' && id[23] == '-' {
		j, ok := 0, true
		for i := 0; i < 36 && ok; i++ {
			if i == 8 || i == 13 || i == 18 || i == 23 {
				continue
			}
			h, okh := unhex(id[i])
			l, okl := unhex(id[i+1])
			ok = okh && okl
			k[j] = h<<4 | l
			j++
			i++
		}
		if ok {
			return k
		}
		k = [16]byte{}
	}
	h := fnv.New128a()
	h.Write([]byte(id))
	h.Sum(k[:0])
	return k
}

func unhex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
