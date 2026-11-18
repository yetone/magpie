package agent

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// dimPipe is the pipe of the daemon of Dim's native home, as dim-cli's
// connect_inner names it: dim-rust- and the first 32 hex digits of
// sha256("dimcode:" + the home as Rust's canonicalize gives it, \\?\C:\…).
func dimPipe(native string) (string, error) {
	p, err := filepath.Abs(native)
	if err != nil {
		return "", err
	}
	if p, err = filepath.EvalSymlinks(p); err != nil {
		return "", err
	}
	if rest, ok := strings.CutPrefix(p, `\\`); ok {
		p = `\\?\UNC\` + rest
	} else {
		p = `\\?\` + p
	}
	sum := sha256.Sum256([]byte("dimcode:" + p))
	return `\\.\pipe\dim-rust-` + hex.EncodeToString(sum[:])[:32], nil
}

// dimBridge is the pipe bridge process, its stdout and stdin the
// connection.
type dimBridge struct {
	io.Reader
	in  io.WriteCloser
	cmd *exec.Cmd
}

func (b *dimBridge) Write(p []byte) (int, error) { return b.in.Write(p) }

func (b *dimBridge) Close() error {
	b.in.Close()
	b.cmd.Process.Kill()
	b.cmd.Wait()
	return nil
}

// dimDial starts Dim's own bridge to its daemon's pipe (dim-client's
// Client::open): the server binary Dim links into its native home, run
// as `app-server --connect-pipe <pipe>`, which says {"status":"ready"}
// on its stderr once the pipe is authenticated.
func dimDial(dir string) (io.ReadWriteCloser, error) {
	native := filepath.Join(dir, "rust")
	pipe, err := dimPipe(native)
	if err != nil {
		return nil, err
	}
	server := filepath.Join(native, "dimcode-server.exe")
	if _, err := os.Stat(server); err != nil {
		return nil, err
	}
	cmd := proc.Command(server, "app-server", "--connect-pipe", pipe)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	errs, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	b := &dimBridge{Reader: out, in: in, cmd: cmd}
	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(errs).ReadString('\n')
		ready <- line
		io.Copy(io.Discard, errs)
	}()
	var line string
	select {
	case line = <-ready:
	case <-time.After(5 * time.Second):
	}
	var r struct {
		Status string `json:"status"`
		Code   string `json:"code"`
	}
	if json.Unmarshal([]byte(line), &r) != nil || r.Status != "ready" {
		b.Close()
		return nil, errors.New("Dim's pipe bridge isn't ready: " + strings.TrimSpace(line))
	}
	return b, nil
}
