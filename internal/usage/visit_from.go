package usage

import "time"

// Cursor is where a reader stopped in the log: the full blocks it has
// read, which the log keeps as they are while it only grows. The zero
// Cursor starts at the beginning.
type Cursor struct {
	path string
	n    int
	last *rowChunk
}

// VisitFrom is Visit for a reader that keeps what it counted of the rows
// before cur: settled is called with each record of the full blocks after
// cur, which the reader keeps and the next call starts after; tail is
// called with the rows of the last, partial block, which it counts again
// each call. A cur the log has left behind (rewritten, cleared, another
// file) starts over: reset is called first, and settled gets every record. A
// reader of the Gateway page's key limits read a month of the log, row by
// row, for each poll.
func VisitFrom(since time.Time, cur Cursor, reset func(), settled, tail func(Record)) Cursor {
	s := readLogSnapshot()
	n := cur.n
	full := len(s.blocks)
	if full > 0 && s.blocks[full-1].Count < logBlockRows {
		full--
	}
	if n > full || n > 0 && (cur.path != s.path || s.blocks[n-1] != cur.last) {
		n = 0
		reset()
	}
	visitBlocks(s.blocks[n:full], since, settled)
	if full < len(s.blocks) {
		visitBlocks(s.blocks[full:], since, tail)
	}
	next := Cursor{path: s.path, n: full}
	if full > 0 {
		next.last = s.blocks[full-1]
	}
	return next
}
