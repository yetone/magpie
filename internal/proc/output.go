package proc

import (
	"os"
	"os/exec"
	"sync"
	"time"
)

// Output is a command's stdout and stderr, as pipes read apart from its
// Wait. cmd.StdoutPipe's end is closed by Wait, so a command whose output
// is read through it can only be waited for once that output has been
// read to the end, and the end is not the command's own: whatever it
// started kept a copy of its stdout (a program a plugin started, a daemon
// it left behind), so the reader never reached EOF after the command
// ended, the command was never waited for, and it stayed a <defunct>
// child of magpie until magpie quit (acezan on Discord: 57 magpie-bun
// after 47 hours). Output's command is waited for the moment it ends;
// its readers then have OutputGrace to read what it wrote last, and the
// pipes are closed, which ends their reads.
type Output struct {
	// Out and Err are the ends magpie reads.
	Out, Err *os.File
	ws       []*os.File
	outDone  chan struct{}
	errDone  chan struct{}
	outOnce  sync.Once
	errOnce  sync.Once
}

// OutputGrace is how long the readers of a command that has ended have to
// read what it wrote last, before its Output is closed under them.
var OutputGrace = 2 * time.Second

// NewOutput gives cmd's stdout and stderr each a pipe of an Output.
func NewOutput(cmd *exec.Cmd) (*Output, error) {
	or, ow, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	er, ew, err := os.Pipe()
	if err != nil {
		or.Close()
		ow.Close()
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = ow, ew
	return &Output{Out: or, Err: er, ws: []*os.File{ow, ew},
		outDone: make(chan struct{}), errDone: make(chan struct{})}, nil
}

// OutRead says Out's reader has read to its end.
func (o *Output) OutRead() { o.outOnce.Do(func() { close(o.outDone) }) }

// ErrRead says Err's reader has read to its end.
func (o *Output) ErrRead() { o.errOnce.Do(func() { close(o.errDone) }) }

// Close closes the ends magpie reads, for an Output whose command
// isn't started.
func (o *Output) Close() {
	for _, f := range append(o.ws, o.Out, o.Err) {
		f.Close()
	}
}

// Start starts cmd, whose output o is, and waits for it the moment it
// ends. wait gives what Wait said once the command has ended and its
// output has been read or closed; any number of goroutines can call it,
// any number of times. When cmd doesn't start, o is closed.
func (o *Output) Start(cmd *exec.Cmd) (wait func() error, err error) {
	return o.start(cmd, cmd.Start, cmd.Wait)
}

// StartTree is Start for a command started as a Tree (StartTree): what
// it starts in turn is ended with it by the Tree's Kill, and, once it has
// ended on its own, by the wait. A plugin host ended alone left the
// headless browser its plugin had started running with no parent, one
// more for every host ended (620 msedge.exe on one Windows machine,
// #5402).
func (o *Output) StartTree(cmd *exec.Cmd) (*Tree, func() error, error) {
	var t *Tree
	wait, err := o.start(cmd, func() (err error) {
		t, err = StartTree(cmd)
		return err
	}, func() error { return t.Wait() })
	if err != nil {
		return nil, nil, err
	}
	return t, wait, nil
}

func (o *Output) start(cmd *exec.Cmd, start, waitFor func() error) (wait func() error, err error) {
	err = start()
	for _, w := range o.ws {
		w.Close() // the command has its own copies
	}
	if err != nil {
		o.Out.Close()
		o.Err.Close()
		return nil, err
	}
	ended := make(chan struct{})
	var werr error
	go func() {
		werr = waitFor()
		grace := time.NewTimer(OutputGrace)
		defer grace.Stop()
		for _, s := range []struct {
			done chan struct{}
			f    *os.File
		}{{o.outDone, o.Out}, {o.errDone, o.Err}} {
			select {
			case <-s.done:
			case <-grace.C:
				// something the command started still holds this
				// pipe: its reader is stopped rather than left waiting
				grace.Reset(0)
			}
			s.f.Close()
		}
		close(ended)
	}()
	return func() error {
		<-ended
		return werr
	}, nil
}

// StartReaped starts cmd, which magpie leaves running (it outlives magpie
// or ends on its own), and waits for it in the background, so it is
// reaped whenever it ends. Process.Release doesn't reap on Unix: a child
// released and ended stays <defunct> until magpie quits.
func StartReaped(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
