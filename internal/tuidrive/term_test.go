package tuidrive

import (
	"fmt"
	"io"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The agent's exit is seen only once what it wrote before it is read:
// seen first, the reason it gave was left out of the error, which had an
// empty screen (#5283, ubuntu CI). Here the reader is given the last
// words late, as a loaded machine schedules it, on a terminal that ends
// (Unix) and on one that stays open (Windows' pseudo console).
func TestEndedAfterLastWordsRead(t *testing.T) {
	for _, closes := range []bool{true, false} {
		t.Run(fmt.Sprintf("terminal ends %v", closes), func(t *testing.T) {
			r, w := io.Pipe()
			t.Cleanup(func() { _ = w.Close() })
			s := New()
			s.Attach(struct {
				io.Reader
				io.Writer
			}{r, io.Discard})
			go s.Ended(fmt.Errorf("exit status 3"), !closes)
			go func() {
				time.Sleep(300 * time.Millisecond)
				_, _ = io.WriteString(w, "something went wrong\r\n")
				if closes {
					_ = w.Close()
				}
			}()
			select {
			case <-s.exited:
			case <-time.After(10 * time.Second):
				t.Fatal("the exit was never recorded")
			}
			if got := s.LastScreen(); !strings.Contains(got, "something went wrong") {
				t.Fatalf("the exit was recorded with the screen %q", got)
			}
		})
	}
}

// eagainOnce answers EAGAIN before its bytes, as a non-blocking terminal
// with nothing written yet does.
type eagainOnce struct {
	again int
	data  []byte
}

func (e *eagainOnce) Read(p []byte) (int, error) {
	if e.again > 0 {
		e.again--
		return 0, syscall.EAGAIN
	}
	if len(e.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, e.data)
	e.data = e.data[n:]
	return n, nil
}

func (e *eagainOnce) Write(p []byte) (int, error) { return len(p), nil }

// TestReadPastEAGAIN: EAGAIN is not the terminal's end; read keeps
// reading what the agent draws after it (#4834).
func TestReadPastEAGAIN(t *testing.T) {
	s := New()
	s.Attach(&eagainOnce{again: 3, data: []byte("Welcome")})
	select {
	case <-s.readDone:
	case <-time.After(5 * time.Second):
		t.Fatal("read never ended")
	}
	if got := string(s.screen); got != "Welcome" {
		t.Fatalf("screen %q, want what was drawn after the EAGAIN", got)
	}
}

// A screen with nothing on it but a box's lines says them, as drawn: it
// is not one where nothing was drawn. One with nothing says so.
func TestLastScreenOfLinesAlone(t *testing.T) {
	if got := New().LastScreen(); got != "nothing on its screen (0 bytes read)" {
		t.Fatalf("nothing drawn: %q", got)
	}
	s := New()
	s.Feed([]byte("\x1b[38;2;136;136;136m" + strings.Repeat("─", 200) + "\r\n╭──╮\r\n╰──╯"))
	if got := s.LastScreen(); !strings.HasSuffix(got, "─ ╭──╮ ╰──╯") || len(got) > 600 {
		t.Fatalf("lines alone: %q", got)
	}
	if rows := s.Rows(); len(rows) != 0 {
		t.Fatalf("rows of lines alone: %q", rows)
	}
}
