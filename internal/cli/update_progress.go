package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/yetone/magpie/internal/update"
)

// updateProgressOut is where `magpie update` draws its download's
// progress, and whether that is a terminal; tests stand in for it.
var updateProgressOut = func() (io.Writer, bool) {
	return os.Stderr, term.IsTerminal(int(os.Stderr.Fd()))
}

// dlProgress draws a download's progress for `magpie update`: after
// "downloading … …" it said nothing until the file was in, however long
// that took (@ahz2ygnhjmIJ0E0 on X). On a terminal it is one line redrawn
// in place about ten times a second ("  42% 18.3/43.6 MB · 5.2 MB/s"),
// finished with a newline once the bytes are in; elsewhere (a pipe, a log)
// a plain line at each quarter, or, with no size known, one at the end.
type dlProgress struct {
	w   io.Writer
	tty bool
	now func() time.Time

	mu        sync.Mutex
	open      bool // a line is drawn and not yet ended
	width     int  // the drawn line's length, to blank what a shorter one leaves
	drawn     time.Time
	done      int64 // the last bytes reported
	total     int64
	marks     []dlMark // what had come when, for the speed
	quarter   int      // plain lines: the last quarter said
	started   time.Time
	firstDone int64
}

type dlMark struct {
	at   time.Time
	done int64
}

// dlEvery is how often the terminal line is redrawn at most.
const dlEvery = 100 * time.Millisecond

// dlSpan is how far back the speed looks.
const dlSpan = 3 * time.Second

// withDownloadProgress has the update downloaded under ctx draw its
// progress; the func returned ends the line, whatever came of it.
func withDownloadProgress(ctx context.Context) (context.Context, func()) {
	w, tty := updateProgressOut()
	p := &dlProgress{w: w, tty: tty, now: time.Now}
	ctx = update.WithProgress(ctx, p.report)
	ctx = update.WithStep(ctx, p.step)
	return ctx, p.end
}

func (p *dlProgress) report(done, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if total > 0 && done > total {
		total = 0 // a size that is off says nothing
	}
	if p.started.IsZero() || done < p.done {
		// the first bytes, or a try that started the file again
		p.started, p.firstDone, p.marks = now, done, nil
	}
	p.done, p.total = done, total
	if n := len(p.marks); n == 0 || now.Sub(p.marks[n-1].at) >= dlEvery {
		i := 0
		for i+1 < len(p.marks) && now.Sub(p.marks[i+1].at) >= dlSpan {
			i++
		}
		p.marks = append(p.marks[i:], dlMark{now, done})
	}
	whole := total > 0 && done == total
	if !p.tty {
		if total <= 0 {
			return // said once, at the end
		}
		if q := int(done * 4 / total); q > p.quarter {
			p.quarter = q
			fmt.Fprintln(p.w, p.line(now))
		}
		return
	}
	if !whole && p.open && now.Sub(p.drawn) < dlEvery {
		return
	}
	p.draw(now)
	if whole {
		p.endLine()
	}
}

// step ends the line as the download does: a retry says so, and what
// comes after the bytes (the checksum, a Mac's unpack) starts below it.
func (p *dlProgress) step(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch s {
	case update.StepRetry:
		if p.tty && p.open {
			fmt.Fprint(p.w, " · the connection broke off, trying again")
			p.endLine()
		} else if !p.tty {
			fmt.Fprintln(p.w, "  the connection broke off at "+dlMB(p.done)+" MB, trying again")
		}
	case update.StepVerify:
		p.finish()
	}
}

func (p *dlProgress) end() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.finish()
}

// finish ends the terminal line, or says a download of no known size
// once it is in.
func (p *dlProgress) finish() {
	if p.tty {
		if p.open {
			p.draw(p.now())
			p.endLine()
		}
		return
	}
	if p.total <= 0 && p.done > 0 && p.quarter == 0 {
		p.quarter = 4
		fmt.Fprintln(p.w, p.line(p.now()))
	}
}

func (p *dlProgress) draw(now time.Time) {
	s := p.line(now)
	pad := ""
	if n := len(s); n < p.width {
		pad = strings.Repeat(" ", p.width-n)
	}
	// \r and spaces, not an escape sequence: a Windows console that
	// isn't taking them prints them as they are
	fmt.Fprint(p.w, "\r"+s+pad)
	p.open, p.width, p.drawn = true, len(s), now
}

func (p *dlProgress) endLine() {
	fmt.Fprintln(p.w)
	p.open, p.width = false, 0
}

// line is "  42% 18.3/43.6 MB · 5.2 MB/s", or with no size known
// "  18.3 MB · 5.2 MB/s"; the speed shows once there is half a second
// of it.
func (p *dlProgress) line(now time.Time) string {
	var b strings.Builder
	b.WriteString("  ")
	if p.total > 0 {
		fmt.Fprintf(&b, "%3d%% %s/%s MB", p.done*100/p.total, dlMB(p.done), dlMB(p.total))
	} else {
		fmt.Fprintf(&b, "%s MB", dlMB(p.done))
	}
	if r, ok := p.speed(now); ok {
		b.WriteString(" · " + r)
	}
	return b.String()
}

// speed is how fast the bytes came over about the last dlSpan; at the
// end, over the whole download.
func (p *dlProgress) speed(now time.Time) (string, bool) {
	from := dlMark{p.started, p.firstDone}
	if !(p.total > 0 && p.done == p.total) {
		for _, m := range p.marks {
			if now.Sub(m.at) <= dlSpan {
				from = m
				break
			}
		}
	}
	d := now.Sub(from.at)
	if d < 500*time.Millisecond || p.done < from.done {
		return "", false
	}
	bps := float64(p.done-from.done) / d.Seconds()
	if bps >= 1e6 {
		return fmt.Sprintf("%.1f MB/s", bps/1e6), true
	}
	return fmt.Sprintf("%.0f KB/s", bps/1e3), true
}

func dlMB(n int64) string { return fmt.Sprintf("%.1f", float64(n)/1e6) }
