package coderun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime/metrics"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// Command is the hidden subcommand that runs a script: magpie's own
// binary, started again by Run.
const Command = "run-code-helper"

// Helping is whether this process was started as the helper: before
// anything else runs, `if coderun.Helping() { os.Exit(coderun.Main()) }`.
func Helping() bool {
	return len(os.Args) > 1 && os.Args[1] == Command
}

// grace is how long past its Wait a helper gets to say it was stopped
// before it is ended: a script in a builtin doesn't hear the interrupt.
var grace = 2 * time.Second

// start is what the gateway gives the helper first.
type start struct {
	Code   string   `json:"code"`
	Tools  []string `json:"tools"`
	Limits Limits   `json:"limits"`
}

// said is one message from the helper: a tool call it wants run, its
// result, or why it stopped itself.
type said struct {
	Call    *call           `json:"call,omitempty"`
	Done    json.RawMessage `json:"done,omitempty"`
	Stopped string          `json:"stopped,omitempty"`
}

type call struct {
	ID   int             `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// answer is a tool call's result, for the helper.
type answer struct {
	ID  int             `json:"id"`
	Res json.RawMessage `json:"res,omitempty"`
	Err string          `json:"err,omitempty"`
}

// Run runs code in a helper process, exe started as Command, with names
// the tools it may call, each run here by run. It ends the helper when
// the script is past lim.Wait (and grace), and whatever has gone wrong it
// gives back a result that says so: never an error.
func Run(ctx context.Context, exe, code string, names []string, lim Limits, run Call) any {
	ctx, cancel := context.WithTimeout(ctx, lim.Wait+grace)
	defer cancel()
	cmd := proc.CommandContext(ctx, exe, Command)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Result("code mode couldn't start: "+err.Error(), true)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result("code mode couldn't start: "+err.Error(), true)
	}
	var errs tail
	cmd.Stderr = &errs
	if err := cmd.Start(); err != nil {
		return Result("code mode couldn't start: "+err.Error(), true)
	}
	calls, stopCalls := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() {
		// a call still running is cancelled, and the helper, done or
		// not, is ended and waited for
		stopCalls()
		stdin.Close()
		cmd.Process.Kill()
		wg.Wait()
		cmd.Wait()
	}()
	var wmu sync.Mutex
	enc := json.NewEncoder(stdin)
	send := func(v any) {
		wmu.Lock()
		defer wmu.Unlock()
		enc.Encode(v)
	}
	send(start{Code: code, Tools: names, Limits: lim})
	dec := json.NewDecoder(stdout)
	n := 0
	for {
		var m said
		if err := dec.Decode(&m); err != nil {
			if ctx.Err() != nil {
				return Result(fmt.Sprintf("The code was stopped: it didn't finish in %s.", lim.Wait), true)
			}
			stdin.Close()
			cmd.Wait()
			why := "its runner quit"
			if s := errs.String(); strings.Contains(s, "out of memory") {
				why = "it ran out of memory"
			} else if s != "" {
				why += ": " + lastLine(s)
			}
			return Result("The code was stopped: "+why+".", true)
		}
		switch {
		case m.Stopped != "":
			return Result("The code was stopped: "+m.Stopped+".", true)
		case m.Done != nil:
			var res any
			if err := json.Unmarshal(m.Done, &res); err != nil {
				return Result("code mode's result can't be read: "+err.Error(), true)
			}
			return res
		case m.Call != nil:
			c := *m.Call
			if n++; n > lim.Calls {
				send(answer{ID: c.ID, Err: fmt.Sprintf("more than %d tool calls in one script", lim.Calls)})
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				a := answer{ID: c.ID}
				res, err := run(calls, c.Name, c.Args)
				if err == nil {
					a.Res, err = json.Marshal(res)
				}
				if err != nil {
					a.Err = err.Error()
				}
				send(a)
			}()
		}
	}
}

// Main is the helper: it runs the script the gateway gives it on stdin,
// asking the gateway for each tool call, and ends itself when its heap
// grows past the script's Heap. Its exit code is 0 once it has answered.
func Main() int {
	dec := json.NewDecoder(os.Stdin)
	var s start
	if err := dec.Decode(&s); err != nil {
		fmt.Fprintln(os.Stderr, "magpie run-code-helper:", err)
		return 2
	}
	var wmu sync.Mutex
	enc := json.NewEncoder(os.Stdout)
	send := func(v said) {
		wmu.Lock()
		defer wmu.Unlock()
		enc.Encode(v)
	}
	go func() {
		// a builtin allocating is past the interrupt's reach, so it is the
		// process that ends: its heap is the script's alone
		base := heap()
		tick := time.NewTicker(20 * time.Millisecond)
		for range tick.C {
			if h := heap(); h > base && h-base > s.Limits.Heap {
				if wmu.TryLock() {
					enc.Encode(said{Stopped: fmt.Sprintf("it used more than %d MB of memory", s.Limits.Heap>>20)})
				}
				os.Exit(3)
			}
		}
	}()

	var pmu sync.Mutex
	pending := map[int]chan answer{}
	next := 0
	go func() {
		for {
			var a answer
			if err := dec.Decode(&a); err != nil {
				// the gateway is gone, or ended the script: nothing is
				// left to answer to
				os.Exit(1)
			}
			pmu.Lock()
			ch := pending[a.ID]
			delete(pending, a.ID)
			pmu.Unlock()
			if ch != nil {
				ch <- a
			}
		}
	}()
	ask := func(ctx context.Context, name string, args json.RawMessage) (any, error) {
		ch := make(chan answer, 1)
		pmu.Lock()
		next++
		id := next
		pending[id] = ch
		pmu.Unlock()
		send(said{Call: &call{ID: id, Name: name, Args: args}})
		select {
		case a := <-ch:
			if a.Err != "" {
				return nil, errors.New(a.Err)
			}
			var res any
			if err := json.Unmarshal(a.Res, &res); err != nil {
				return nil, err
			}
			return res, nil
		case <-ctx.Done():
			pmu.Lock()
			delete(pending, id)
			pmu.Unlock()
			return nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.Limits.Wait)
	defer cancel()
	res, err := json.Marshal(script(ctx, s.Code, s.Tools, ask, s.Limits))
	if err != nil {
		res, _ = json.Marshal(Result("code mode's result can't be written: "+err.Error(), true))
	}
	send(said{Done: res})
	return 0
}

// heap is the bytes the Go heap's objects take now.
func heap() uint64 {
	s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(s)
	if s[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return s[0].Value.Uint64()
}

// tail keeps the last few KB written to it: the helper's stderr, for why
// it quit.
type tail struct {
	mu sync.Mutex
	b  []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if over := len(t.b) - 4096; over > 0 {
		t.b = t.b[over:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.b))
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
