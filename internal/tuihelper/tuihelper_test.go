//go:build darwin || linux

package tuihelper

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// helper runs serve on pipes: what the test writes is the caller's stdin,
// and each line serve writes comes back on the channel.
func helper(t *testing.T) (io.WriteCloser, <-chan out, <-chan error) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	lines := make(chan out, 256)
	done := make(chan error, 1)
	go func() {
		err := serve(inR, outW, make(chan os.Signal))
		outW.Close()
		done <- err
	}()
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 64<<10), 16<<20)
		for sc.Scan() {
			var o out
			if err := json.Unmarshal(sc.Bytes(), &o); err != nil {
				t.Errorf("not JSON: %q", sc.Text())
				continue
			}
			lines <- o
		}
	}()
	t.Cleanup(func() { inW.Close() })
	return inW, lines, done
}

func send(t *testing.T, w io.Writer, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if _, err := w.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

// until reads lines until one passes ok, failing after 10s or when the
// helper says it is done first.
func until(t *testing.T, lines <-chan out, what string, ok func(out) bool) out {
	t.Helper()
	end := time.After(10 * time.Second)
	for {
		select {
		case o, open := <-lines:
			if !open {
				t.Fatalf("%s: the helper ended first", what)
			}
			if ok(o) {
				return o
			}
		case <-end:
			t.Fatalf("%s: not in 10s", what)
		}
	}
}

func shows(s string) func(out) bool {
	return func(o out) bool { return o.Screen != nil && strings.Contains(*o.Screen, s) }
}

// A program started is told on, what it draws comes as screens, what is
// written is typed to it, and its end is the last line.
func TestTypedAndDrawn(t *testing.T) {
	w, lines, done := helper(t)
	dir := t.TempDir()
	send(t, w, map[string]any{"start": map[string]any{
		"path": "/bin/sh",
		"args": []string{"-c", `printf 'ready in %s\n' "$(basename "$PWD")"; read x; printf 'got %s\n' "$x"; exit 3`},
		"env":  []string{"PATH=/usr/bin:/bin", "TERM=xterm-256color"},
		"dir":  dir,
	}})
	until(t, lines, "started", func(o out) bool { return o.Started })
	until(t, lines, "the program's first words", shows("ready in "+dirBase(dir)))
	send(t, w, map[string]any{"write": "hello helper\r"})
	until(t, lines, "the answer to what was typed", shows("got hello helper"))
	e := until(t, lines, "the exit", func(o out) bool { return o.Exit != nil })
	if e.Exit.Code != 3 {
		t.Errorf("exit %+v, want code 3", *e.Exit)
	}
	if err := <-done; err != nil {
		t.Error(err)
	}
}

func dirBase(d string) string { return d[strings.LastIndex(d, "/")+1:] }

// A reply the caller gave is the terminal's answer when the program asks
// it: a program that waits on the answer goes on.
func TestRepliesAnswered(t *testing.T) {
	w, lines, _ := helper(t)
	send(t, w, map[string]any{"start": map[string]any{
		"path":    "/bin/sh",
		"args":    []string{"-c", `printf 'ASK?'; read x; printf '\nanswered %s\n' "$x"; sleep 30`},
		"env":     []string{"PATH=/usr/bin:/bin", "TERM=xterm-256color"},
		"replies": []map[string]string{{"ask": "ASK?", "answer": "yes\r"}},
	}})
	until(t, lines, "the answer the program read", shows("answered yes"))
	send(t, w, map[string]any{"stop": true})
	until(t, lines, "the exit after stop", func(o out) bool { return o.Exit != nil })
}

// The caller going away ends the program with the helper.
func TestCallerGoneEndsProgram(t *testing.T) {
	w, lines, done := helper(t)
	pidFile := t.TempDir() + "/pid"
	send(t, w, map[string]any{"start": map[string]any{
		"path": "/bin/sh",
		"args": []string{"-c", `echo $$ > ` + pidFile + `; echo up; sleep 60`},
		"env":  []string{"PATH=/usr/bin:/bin", "TERM=xterm-256color"},
	}})
	until(t, lines, "up", shows("up"))
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the helper outlived its caller by 10s")
	}
	if pid := strings.TrimSpace(string(b)); alive(pid) {
		t.Errorf("the program (%s) outlived the helper", pid)
	}
}

// A first line that isn't a start is answered with an error, and nothing
// runs.
func TestFirstLineMustStart(t *testing.T) {
	w, lines, done := helper(t)
	send(t, w, map[string]any{"write": "x"})
	o := until(t, lines, "the error", func(o out) bool { return o.Error != "" })
	if !strings.Contains(o.Error, "start") {
		t.Errorf("error %q", o.Error)
	}
	if err := <-done; err != nil {
		t.Error(err)
	}
}

// A program that can't be started is said so, as the last line.
func TestStartFailsSaid(t *testing.T) {
	w, lines, _ := helper(t)
	send(t, w, map[string]any{"start": map[string]any{"path": "/no/such/program", "env": []string{"PATH=/usr/bin:/bin"}}})
	until(t, lines, "the error", func(o out) bool { return o.Error != "" })
}

func alive(pid string) bool {
	p, err := strconv.Atoi(pid)
	if err != nil {
		return false
	}
	return syscall.Kill(p, 0) == nil
}

// With no env, the program has the helper's own environment.
func TestNoEnvIsHelpers(t *testing.T) {
	t.Setenv("TUIHELPER_SEEN", "from the helper")
	w, lines, _ := helper(t)
	send(t, w, map[string]any{"start": map[string]any{
		"path": "/bin/sh",
		"args": []string{"-c", `printf 'env %s\n' "$TUIHELPER_SEEN"; sleep 30`},
	}})
	until(t, lines, "the helper's variable", shows("env from the helper"))
	send(t, w, map[string]any{"stop": true})
	until(t, lines, "the exit after stop", func(o out) bool { return o.Exit != nil })
}
