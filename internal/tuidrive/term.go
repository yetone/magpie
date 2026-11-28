// Package tuidrive runs an agent's interactive terminal UI (TUI) in a
// pseudo-terminal of magpie's own, for a helper that drives it as a
// person at a keyboard does: what the agent draws is read onto a screen
// (Grid) and what is typed is written to it. On macOS and Linux the
// terminal is a pseudo-terminal (term_unix.go), on Windows a pseudo
// console (term_windows.go); no tmux or terminal app is involved.
//
// What is the same for every agent lives here: starting and ending the
// program in its terminal, reading what it draws and answering what it
// asks of the terminal, the screen as drawn since it was last cleared and
// as it stands, and finding a dialog at the foot of the screen (dialog.go).
// What an agent's screens say, how a message is pasted into its prompt and
// where its answers are read from belong to the agent's own driver:
// internal/claudetui for Claude Code.
package tuidrive

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/lazyre"
)

// Cols and Rows are the size of the agent's terminal, the pseudo-terminal
// (term_unix.go) and Windows' pseudo console (term_windows.go) made alike,
// and the grid it is read on. A grid of another size misreads the screen:
// an 80 by 24 one, on macOS and Linux while their pseudo-terminal was 200
// by 50, drew every row under the 24th over the 24th, and Claude Code's
// /usage panel, at the foot of the screen, was told as one window or as a
// figure since drawn again (#4988, shinnqy on X).
const (
	Cols = 200
	Rows = 50
)

// Term is an agent's terminal: the program running in it, what it has
// drawn, and the way to type to it (Write).
type Term struct {
	// pty is the agent's terminal: what it draws is read from it, and what
	// is typed written to it
	pty io.ReadWriter
	child

	mu     sync.Mutex
	screen []byte
	// grid is the screen as a terminal shows it (screen.go), drawn from
	// the start, nil until the agent draws
	grid    *Grid
	changed chan struct{}
	// cleared is when the screen was last cleared (ClearScreen)
	cleared time.Time

	exited chan struct{}
	exit   error
	// readDone closes as read ends; nread counts the bytes it has read
	readDone chan struct{}
	nread    atomic.Int64

	// OnDraw, when set, is called with the grid after each piece the agent
	// draws, and OnClear as the screen is cleared, both under the lock
	// Locked holds: what a driver keeps of the screen changes with it.
	OnDraw  func(g *Grid)
	OnClear func()
	// Undrawn, when set, says why an agent that drew nothing hasn't
	// (LastScreen), "" when it can't say.
	Undrawn func() string
	// Replies answers more of what the agent asks of the terminal than
	// where the cursor is and what terminal it is (read), for an agent
	// that waits for the answer before it draws: agy asks for the
	// secondary device attributes and draws nothing until it has them.
	// Set before Start or Attach.
	Replies []Reply
}

// Reply is an answer the terminal gives: Answer, each time the agent
// draws Ask.
type Reply struct{ Ask, Answer string }

// New is a terminal with no program in it yet: Start runs one, or Attach
// reads one already started.
func New() *Term {
	return &Term{changed: make(chan struct{}, 1), exited: make(chan struct{}), readDone: make(chan struct{})}
}

// Attach reads what is drawn on rw, and writes what is typed to it, as a
// terminal a program already runs in.
func (t *Term) Attach(rw io.ReadWriter) {
	t.pty = rw
	go t.read()
}

// Write types p to the agent.
func (t *Term) Write(p []byte) (int, error) { return t.pty.Write(p) }

// Changed ticks when the agent has drawn something since it was last read.
func (t *Term) Changed() <-chan struct{} {
	if t == nil {
		return nil
	}
	return t.changed
}

// Exited closes when the agent has ended (Exit).
func (t *Term) Exited() <-chan struct{} {
	if t == nil {
		return nil
	}
	return t.exited
}

// Exit is how the agent ended, once Exited is closed.
func (t *Term) Exit() error {
	if t == nil {
		return nil
	}
	return t.exit
}

// BytesRead is how many bytes the agent has drawn.
func (t *Term) BytesRead() int64 { return t.nread.Load() }

// Ended records how the agent ended, err, once read has what it drew
// before it ended: its exit can be seen before the bytes it wrote just
// before are read, and the error said then carried an empty screen, not
// the reason it gave (#5283, a loaded Linux CI runner). A terminal that
// ends with its last writer (Unix) is read to its end; a pseudo console
// that stays open (Windows, open) is read until it has gone quiet after
// its first bytes. Two seconds at most, for a terminal a child of the
// agent keeps open.
func (t *Term) Ended(err error, open bool) {
	if t.readDone != nil {
		limit := time.After(2 * time.Second)
		for last := int64(0); ; {
			var tick <-chan time.Time
			if open {
				tick = time.After(250 * time.Millisecond)
			}
			select {
			case <-t.readDone:
			case <-limit:
			case <-tick:
				if n := t.nread.Load(); n == 0 || n != last {
					last = n
					continue
				}
			}
			break
		}
	}
	t.exit = err
	close(t.exited)
}

// read keeps the tail of what the agent draws, and answers what it asks
// of the terminal: where the cursor is, and what terminal it is.
func (t *Term) read() {
	defer close(t.readDone)
	buf := make([]byte, 64<<10)
	for {
		n, err := t.pty.Read(buf)
		if n > 0 {
			t.nread.Add(int64(n))
			d := buf[:n]
			if bytes.Contains(d, []byte("\x1b[6n")) {
				_, _ = io.WriteString(t.pty, "\x1b[1;1R")
			}
			if bytes.Contains(d, []byte("\x1b[c")) || bytes.Contains(d, []byte("\x1b[0c")) {
				_, _ = io.WriteString(t.pty, "\x1b[?62;c")
			}
			for _, r := range t.Replies {
				if bytes.Contains(d, []byte(r.Ask)) {
					_, _ = io.WriteString(t.pty, r.Answer)
				}
			}
			t.Feed(d)
		}
		if err != nil {
			// a terminal read non-blocking with nothing to read yet is
			// not its end: the agent would fill the terminal and hang
			// waiting for a reader (#4834)
			if errors.Is(err, syscall.EAGAIN) {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			return
		}
	}
}

// Feed takes d as drawn by the agent: kept as drawn since the screen was
// last cleared, and put on the grid. The read loop feeds what it reads;
// a test feeds a recorded screen.
func (t *Term) Feed(d []byte) {
	t.mu.Lock()
	t.screen = append(t.screen, d...)
	if len(t.screen) > 256<<10 {
		t.screen = append([]byte(nil), t.screen[len(t.screen)-128<<10:]...)
	}
	if t.grid == nil {
		t.grid = NewGrid(Cols, Rows)
	}
	t.grid.Draw(d)
	if t.OnDraw != nil {
		t.OnDraw(t.grid)
	}
	t.mu.Unlock()
	select {
	case t.changed <- struct{}{}:
	default:
	}
}

// Locked runs f under the lock the screen is kept under, which OnDraw and
// OnClear run under too.
func (t *Term) Locked(f func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	f()
}

// ANSI matches a terminal escape: CSI, OSC, DCS/PM/APC strings, and the
// two-byte ones.
var ANSI = lazyre.New(`\x1b\[[0-9;?<>=]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[P^_][^\x1b]*\x1b\\|\x1b.`)

// Plain is what the agent drew since the screen was last cleared
// (ClearScreen), its escapes taken out.
func (t *Term) Plain() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	b := string(t.screen)
	t.mu.Unlock()
	return ANSI.ReplaceAllString(b, " ")
}

// ClearScreen forgets what was drawn so far (Plain): what is read after
// it is what the agent draws next. The grid keeps the screen as it stands.
func (t *Term) ClearScreen() {
	t.mu.Lock()
	t.screen = t.screen[:0]
	if t.OnClear != nil {
		t.OnClear()
	}
	t.cleared = time.Now()
	t.mu.Unlock()
}

// redrawWait is how long the screen may stay as it was after a key, nothing
// drawn since it was cleared, before it is read as it stands (Seen): the
// key was lost, and the agent draws nothing again for it.
const redrawWait = 1500 * time.Millisecond

// Seen is what to read the screen by: what the agent drew since it was
// cleared, or, nothing drawn since for redrawWait, the screen as it
// stands. A dialog that turns away a key sent just after it opens (Claude
// Code 2.1.319's Claude in Chrome offer) draws nothing again, and a run
// that read only what was drawn since the key waited out its caller with
// nothing to say (sowhynot).
func (t *Term) Seen() string {
	p := t.Plain()
	if strings.TrimSpace(p) != "" {
		return p
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.grid == nil || time.Since(t.cleared) < redrawWait {
		return p
	}
	return t.grid.Text()
}

// Now is the screen as it stands, "" before the agent draws.
func (t *Term) Now() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.nowLocked()
}

// NowLocked is Now for a caller in Locked.
func (t *Term) NowLocked() string { return t.nowLocked() }

func (t *Term) nowLocked() string {
	if t.grid == nil {
		return ""
	}
	return t.grid.Text()
}

// LastScreen is the end of what the agent drew, for an error to say: with
// nothing drawn since the screen was cleared, the screen as it stands, and
// with nothing on it, that nothing was drawn (and Undrawn's why). An error
// that ended on an empty screen said nothing of what the agent showed
// (sowhynot). The lines and blocks its boxes are drawn with are left out
// (words): Claude Code's prompt box is ruled the screen's width over and
// under, and a tail of 600 bytes was all rule, one long line, not the
// words above it (FremontDango, Windows). A screen of boxes alone is said
// as drawn.
func (t *Term) LastScreen() string {
	if t == nil {
		return "nothing on its screen (0 bytes read)"
	}
	plain, now := t.Plain(), t.Now()
	p := words(plain)
	if p == "" {
		p = words(now)
	}
	if p == "" {
		p = strings.Join(strings.Fields(plain), " ")
	}
	if p == "" {
		p = strings.Join(strings.Fields(now), " ")
	}
	if p == "" {
		n := t.nread.Load()
		why := fmt.Sprintf("nothing on its screen (%d bytes read)", n)
		if n == 0 && t.Undrawn != nil {
			if w := t.Undrawn(); w != "" {
				why += "; " + w
			}
		}
		return why
	}
	if len(p) > 600 {
		p = p[len(p)-600:]
		for i := 0; i < len(p) && i < utf8.UTFMax && !utf8.RuneStart(p[0]); i++ {
			p = p[1:]
		}
	}
	return p
}

// Screen is the screen as it stands, row by row as drawn, for a person
// to look at while it runs (magpie claude-code screen, #8075): the boxes
// and the spaces between words kept, the blank rows under the last drawn
// left out. None before the agent draws.
func (t *Term) Screen() []string {
	if t == nil {
		return nil
	}
	rows := strings.Split(t.Now(), "\n")
	for len(rows) > 0 && strings.TrimSpace(rows[len(rows)-1]) == "" {
		rows = rows[:len(rows)-1]
	}
	if len(rows) == 0 {
		return nil
	}
	return rows
}

// Rows is the screen as it stands, row by row, for a log to keep whole
// (LastScreen keeps its end): each row's words (words), the rows with none
// left out. None before the agent draws.
func (t *Term) Rows() []string {
	if t == nil {
		return nil
	}
	var rows []string
	for _, r := range strings.Split(t.Now(), "\n") {
		if w := words(r); w != "" {
			rows = append(rows, w)
		}
	}
	return rows
}

// words is s's words one space apart, without the lines and blocks a
// terminal draws boxes and rules with (decoration): they say nothing, and
// a screen-wide rule is 600 bytes of them.
func words(s string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if decoration(r) {
			return ' '
		}
		return r
	}, s)), " ")
}

// decoration is whether r is one of the lines, corners and blocks boxes
// are drawn with: Unicode's Box Drawing (─ │ ╭ ╮ ╰ ╯ ═ …) and Block
// Elements (▐ ▛ █ ▜ ▌ ▔ ▁ …), and the horizontal scan lines (⎺ ⎻ ⎼ ⎽).
func decoration(r rune) bool {
	return r >= 0x2500 && r <= 0x259F || r >= 0x23BA && r <= 0x23BD
}
