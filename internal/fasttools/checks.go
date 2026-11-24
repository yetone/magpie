package fasttools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/proc"
)

const (
	maxChecks = 8
	// what a failed command shows of its output: its end, where the
	// failure is
	failLines = 40
	failBytes = 2400
)

type check struct {
	Cmd     string `json:"cmd"`
	Name    string `json:"name"`
	Timeout int    `json:"timeout"`
}

// UnmarshalJSON takes a command as a plain string too.
func (c *check) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		c.Cmd = s
		return nil
	}
	type plain check
	return json.Unmarshal(b, (*plain)(c))
}

type checkResult struct {
	name    string
	ok      bool
	code    int
	timeout bool
	took    time.Duration
	out     string
	err     string
}

func (r checkResult) text(tail bool) string {
	took := r.took.Round(100 * time.Millisecond)
	switch {
	case r.err != "":
		return fmt.Sprintf("FAIL %s: %s\n", r.name, r.err)
	case r.ok:
		return fmt.Sprintf("ok   %s (%s)\n", r.name, took)
	}
	why := fmt.Sprintf("exit %d", r.code)
	if r.timeout {
		why = "timed out"
	}
	s := fmt.Sprintf("FAIL %s: %s (%s)\n", r.name, why, took)
	if tail && strings.TrimSpace(r.out) != "" {
		s += indent(tailOf(r.out)) + "\n"
	}
	return s
}

func runChecks(raw json.RawMessage) (string, error) {
	var a struct {
		Cwd      string  `json:"cwd"`
		Commands []check `json:"commands"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("arguments: %w", err)
	}
	if len(a.Commands) == 0 {
		return "", errors.New("no commands")
	}
	if len(a.Commands) > maxChecks {
		return "", fmt.Errorf("%d commands: at most %d in one call", len(a.Commands), maxChecks)
	}
	dir, err := root(a.Cwd)
	if err != nil {
		return "", err
	}
	res := make([]checkResult, len(a.Commands))
	var wg sync.WaitGroup
	for i, c := range a.Commands {
		if c.Name == "" {
			c.Name = c.Cmd
			if len(c.Name) > 60 {
				c.Name = c.Name[:57] + "…"
			}
		}
		t := c.Timeout
		if t <= 0 {
			t = 300
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[i] = runOne(dir, c, time.Duration(min(t, 1800))*time.Second)
		}()
	}
	wg.Wait()
	var b strings.Builder
	failed := 0
	for _, r := range res {
		if !r.ok {
			failed++
		}
	}
	fmt.Fprintf(&b, "%d of %d passed\n", len(res)-failed, len(res))
	for _, r := range res {
		b.WriteString(r.text(true))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// runOne runs c's command line in a shell in dir.
func runOne(dir string, c check, timeout time.Duration) checkResult {
	r := checkResult{name: c.Name}
	if strings.TrimSpace(c.Cmd) == "" {
		r.err = "no command"
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	name, args := shellArgs(c.Cmd)
	cmd := proc.CommandContext(ctx, name, args...)
	ownGroup(cmd)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	began := time.Now()
	err := cmd.Run()
	r.took = time.Since(began)
	r.out = out.String()
	var ee *exec.ExitError
	switch {
	case err == nil:
		r.ok = true
	case ctx.Err() == context.DeadlineExceeded:
		r.timeout = true
	case errors.As(err, &ee):
		r.code = ee.ExitCode()
	default:
		r.err = err.Error()
	}
	return r
}

var ansi = lazyre.New(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)

// tailOf is the last lines of out, its colours taken out.
func tailOf(out string) string {
	out = ansi.ReplaceAllString(out, "")
	out = strings.ReplaceAll(out, "\r\n", "\n")
	// a progress line rewritten with \r: its last state
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for i, l := range lines {
		if j := strings.LastIndexByte(l, '\r'); j >= 0 {
			lines[i] = l[j+1:]
		}
	}
	left := 0
	if len(lines) > failLines {
		left = len(lines) - failLines
		lines = lines[left:]
	}
	s := strings.Join(lines, "\n")
	if len(s) > failBytes {
		cut := len(s) - failBytes
		for cut < len(s) && !utf8Start(s[cut]) {
			cut++
		}
		if i := strings.IndexByte(s[cut:], '\n'); i >= 0 && i < 200 {
			cut += i + 1
		}
		s = s[cut:]
		left = -1
	}
	switch {
	case left > 0:
		s = fmt.Sprintf("… %d lines before\n", left) + s
	case left < 0:
		s = "…\n" + s
	}
	return s
}

func indent(s string) string { return "  " + strings.ReplaceAll(s, "\n", "\n  ") }

var (
	shellOnce sync.Once
	shellPath string
)

// lookShell is bash when there is one: commands are written for it more
// often than for a plain sh.
func lookShell() string {
	shellOnce.Do(func() {
		if p, err := proc.Look("bash"); err == nil {
			shellPath = p
		}
	})
	return shellPath
}
