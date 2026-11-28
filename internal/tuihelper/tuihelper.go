// Package tuihelper is `magpie tui-helper`: internal/tuidrive's terminal
// for a program that isn't Go, a plugin that drives an agent's interactive
// mode (the agy-tui plugin drives agy's). The helper runs one program in a
// terminal of its own and speaks JSON, one object a line:
//
// It reads, on stdin:
//
//	{"start": {"path": …, "args": […], "env": […], "dir": …,
//	           "replies": [{"ask": …, "answer": …}]}}  first, once
//	{"write": "…"}                                     typed to the program
//	{"stop": true}                                     ends it
//
// and writes, on stdout:
//
//	{"started": true}
//	{"screen": "…"}   the screen as it stands (tuidrive's Now), each time
//	                  it changed, no more often than every screenWait
//	{"exit": {"code": n, "error": "…"}}   last: the program ended
//	{"error": "…"}    last: it couldn't be started
//
// The environment is the one given, whole (tuidrive adds nothing); with
// none ("env" left out), the helper's own, as its caller started it. The
// program ends with the helper: on stdin closing (its caller gone), on
// "stop", and on the signals tuidrive names (EndSignals).
package tuihelper

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/tuidrive"
)

// Command is the hidden subcommand that runs it.
const Command = "tui-helper"

// screenWait is the least time between two screens told: an agent redraws
// a spinner many times a second, and its caller wants what is on the
// screen, not each frame.
var screenWait = 50 * time.Millisecond

type start struct {
	Path    string   `json:"path"`
	Args    []string `json:"args"`
	Env     []string `json:"env"`
	Dir     string   `json:"dir"`
	Replies []struct {
		Ask    string `json:"ask"`
		Answer string `json:"answer"`
	} `json:"replies"`
}

type in struct {
	Start *start  `json:"start"`
	Write *string `json:"write"`
	Stop  bool    `json:"stop"`
}

type exitInfo struct {
	Code  int    `json:"code"`
	Error string `json:"error,omitempty"`
}

type out struct {
	Started bool      `json:"started,omitempty"`
	Screen  *string   `json:"screen,omitempty"`
	Exit    *exitInfo `json:"exit,omitempty"`
	Error   string    `json:"error,omitempty"`
}

// Run is the helper, on os.Stdin and os.Stdout.
func Run(args []string) error {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: magpie tui-helper (JSON lines on stdin; see internal/tuihelper)")
		return errors.New("tui-helper takes no arguments")
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, tuidrive.EndSignals...)
	return serve(os.Stdin, os.Stdout, sigs)
}

func serve(r io.Reader, w io.Writer, sigs <-chan os.Signal) error {
	var mu sync.Mutex
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	say := func(o out) {
		mu.Lock()
		defer mu.Unlock()
		_ = enc.Encode(o)
	}
	lines := bufio.NewScanner(r)
	lines.Buffer(make([]byte, 64<<10), 64<<20)
	var first in
	if !lines.Scan() {
		return lines.Err()
	}
	if err := json.Unmarshal(lines.Bytes(), &first); err != nil || first.Start == nil || first.Start.Path == "" {
		say(out{Error: "the first line is {\"start\": {\"path\": …}}"})
		return nil
	}
	s := first.Start
	if s.Dir != "" {
		// the helper is the program's alone: it starts in the folder given
		if err := os.Chdir(s.Dir); err != nil {
			say(out{Error: err.Error()})
			return nil
		}
	}
	t := tuidrive.New()
	for _, r := range s.Replies {
		if r.Ask != "" {
			t.Replies = append(t.Replies, tuidrive.Reply{Ask: r.Ask, Answer: r.Answer})
		}
	}
	env := s.Env
	if env == nil {
		env = os.Environ()
	}
	if err := t.Start(s.Path, s.Args, env); err != nil {
		say(out{Error: err.Error()})
		return nil
	}
	say(out{Started: true})

	// what the caller sends, read beside the screen
	input := make(chan in)
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		for lines.Scan() {
			var m in
			if json.Unmarshal(lines.Bytes(), &m) == nil {
				input <- m
			}
		}
	}()

	last := ""
	tell := func() {
		if now := t.Now(); now != last {
			last = now
			say(out{Screen: &now})
		}
	}
	var wait <-chan time.Time
	for {
		select {
		case <-t.Changed():
			if wait == nil {
				wait = time.After(screenWait)
			}
		case <-wait:
			wait = nil
			tell()
		case m := <-input:
			if m.Write != nil {
				if _, err := t.Write([]byte(*m.Write)); err != nil {
					say(out{Error: "write: " + err.Error()})
				}
			}
			if m.Stop {
				t.Stop()
			}
		case <-gone:
			// the caller is gone: nothing reads what the program does
			t.Stop()
			<-t.Exited()
			return nil
		case <-sigs:
			t.Stop()
			<-t.Exited()
			return nil
		case <-t.Exited():
			tell()
			e := exitInfo{}
			if err := t.Exit(); err != nil {
				e.Code = -1
				var ee *exec.ExitError
				if errors.As(err, &ee) {
					e.Code = ee.ExitCode()
				}
				e.Error = err.Error()
			}
			say(out{Exit: &e})
			return nil
		}
	}
}
