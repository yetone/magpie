package claudetui

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/width"
)

// grid is Claude Code's screen as a terminal shows it: what it draws is
// put in its cells, where the cursor is moved to. Read as a stream, with
// its escapes taken out (plain), a screen drawn again only where it
// changed is misread: Windows' pseudo console sends only the cells that
// changed, a cell left as it was skipped by a move of the cursor
// ("Settin\x1b[1Cs" for "Settings", whose g was there already), and a
// number redrawn in place is a digit or two after the old one, which the
// stream still holds. The grid holds the screen as it is.
type grid struct {
	cells      [][]rune
	cols, rows int
	row, col   int
	// pending is the start of an escape or a character a read ended in
	// the middle of
	pending []byte
}

func newGrid(cols, rows int) *grid {
	g := &grid{cols: cols, rows: rows}
	g.cells = make([][]rune, rows)
	for i := range g.cells {
		g.cells[i] = blankRow(cols)
	}
	return g
}

func blankRow(cols int) []rune {
	r := make([]rune, cols)
	for i := range r {
		r[i] = ' '
	}
	return r
}

// text is the screen, a line a row, each without its trailing spaces.
func (g *grid) text() string {
	var b strings.Builder
	for i, r := range g.cells {
		if i > 0 {
			b.WriteByte('\n')
		}
		var line []rune
		for _, c := range r {
			if c != 0 { // the second cell of a wide character
				line = append(line, c)
			}
		}
		b.WriteString(strings.TrimRight(string(line), " "))
	}
	return b.String()
}

func (g *grid) clamp() {
	g.row = min(max(g.row, 0), g.rows-1)
	g.col = min(max(g.col, 0), g.cols)
}

func (g *grid) lineFeed() {
	if g.row < g.rows-1 {
		g.row++
		return
	}
	copy(g.cells, g.cells[1:])
	g.cells[g.rows-1] = blankRow(g.cols)
}

func (g *grid) put(r rune) {
	w := 1
	if k := width.LookupRune(r).Kind(); k == width.EastAsianWide || k == width.EastAsianFullwidth {
		w = 2
	}
	if g.col+w > g.cols {
		g.col = 0
		g.lineFeed()
	}
	g.cells[g.row][g.col] = r
	if w == 2 && g.col+1 < g.cols {
		g.cells[g.row][g.col+1] = 0
	}
	g.col += w
}

// erase blanks the cells from..to (not included) of row.
func (g *grid) erase(row, from, to int) {
	for i := max(from, 0); i < min(to, g.cols); i++ {
		g.cells[row][i] = ' '
	}
}

// write draws b. An escape or character cut off at its end is kept for
// the next write.
func (g *grid) write(b []byte) {
	if len(g.pending) > 0 {
		b = append(g.pending, b...)
		g.pending = nil
	}
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == 0x1b:
			n := g.escape(b[i:])
			if n == 0 { // cut off
				if len(b)-i < 4096 {
					g.pending = append([]byte(nil), b[i:]...)
				}
				return
			}
			i += n
			continue
		case c == '\r':
			g.col = 0
		case c == '\n', c == '\v', c == '\f':
			g.lineFeed()
		case c == '\b':
			g.col = max(g.col-1, 0)
		case c == '\t':
			g.col = min((g.col/8+1)*8, g.cols-1)
		case c < 0x20 || c == 0x7f:
		default:
			if !utf8.FullRune(b[i:]) {
				g.pending = append([]byte(nil), b[i:]...)
				return
			}
			r, n := utf8.DecodeRune(b[i:])
			g.put(r)
			i += n
			continue
		}
		i++
	}
}

// escape acts on the escape at the start of b and is its length, 0 when
// b ends before it does.
func (g *grid) escape(b []byte) int {
	if len(b) < 2 {
		return 0
	}
	switch b[1] {
	case '[':
		i := 2
		for i < len(b) && b[i] >= 0x20 && b[i] <= 0x3f {
			i++
		}
		params := string(b[2:i])
		for i < len(b) && b[i] >= 0x20 && b[i] <= 0x2f {
			i++
		}
		if i >= len(b) {
			return 0
		}
		g.csi(params, b[i])
		return i + 1
	case ']', 'P', '^', '_':
		// a string, ended by BEL (an OSC's) or ST
		for i := 2; i < len(b); i++ {
			if b[i] == 0x07 && b[1] == ']' {
				return i + 1
			}
			if b[i] == 0x1b && i+1 < len(b) && b[i+1] == '\\' {
				return i + 2
			}
		}
		return 0
	case '(', ')', '*', '+', '#', '%':
		if len(b) < 3 {
			return 0
		}
		return 3
	case 'D':
		g.lineFeed()
	case 'E':
		g.col = 0
		g.lineFeed()
	case 'M':
		g.row = max(g.row-1, 0)
	}
	return 2
}

func (g *grid) csi(params string, final byte) {
	if strings.HasPrefix(params, "?") || strings.HasPrefix(params, ">") || strings.HasPrefix(params, "<") || strings.HasPrefix(params, "=") {
		return // modes, keyboard protocols: nothing drawn
	}
	var ps []int
	for _, p := range strings.Split(params, ";") {
		n, _ := strconv.Atoi(p)
		ps = append(ps, n)
	}
	arg := func(i, def int) int {
		if i < len(ps) && ps[i] > 0 {
			return ps[i]
		}
		return def
	}
	switch final {
	case 'A':
		g.row -= arg(0, 1)
	case 'B', 'e':
		g.row += arg(0, 1)
	case 'C', 'a':
		g.col += arg(0, 1)
	case 'D':
		g.col -= arg(0, 1)
	case 'E':
		g.row, g.col = g.row+arg(0, 1), 0
	case 'F':
		g.row, g.col = g.row-arg(0, 1), 0
	case 'G', '`':
		g.col = arg(0, 1) - 1
	case 'd':
		g.row = arg(0, 1) - 1
	case 'H', 'f':
		g.row, g.col = arg(0, 1)-1, arg(1, 1)-1
	case 'K':
		if g.row >= 0 && g.row < g.rows {
			switch arg(0, 0) {
			case 0:
				g.erase(g.row, g.col, g.cols)
			case 1:
				g.erase(g.row, 0, g.col+1)
			case 2:
				g.erase(g.row, 0, g.cols)
			}
		}
	case 'J':
		switch arg(0, 0) {
		case 0:
			g.erase(g.row, g.col, g.cols)
			for r := g.row + 1; r < g.rows; r++ {
				g.erase(r, 0, g.cols)
			}
		case 1:
			for r := 0; r < g.row; r++ {
				g.erase(r, 0, g.cols)
			}
			g.erase(g.row, 0, g.col+1)
		case 2, 3:
			for r := range g.rows {
				g.erase(r, 0, g.cols)
			}
		}
	case 'X':
		g.erase(g.row, g.col, g.col+arg(0, 1))
	}
	g.clamp()
}
