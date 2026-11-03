// Package codexrelay keeps the Codex threads open when magpie quits
// working until magpie is opened again or they end.
//
// Codex signed in to ChatGPT sends its turns to magpie's gateway while
// magpie fails it over (agent.EndCodexFailover). At quit magpie takes that
// base URL out of Codex's config, but a thread already open, in the Codex
// app above all, keeps the URL it started with, and every turn after went
// to a closed port: "503 Service Unavailable: Unknown error" through a
// system proxy, "Connection refused" without one, until magpie was opened
// again (idkatbro on X). So at quit, with Codex running, magpie starts a
// small process of its own (`magpie codex-relay`) that takes the port up
// once the gateway lets go of it and passes those turns on to the ChatGPT
// backend on Codex's own sign-in (gateway.CodexRelay). It ends once every
// Codex process it was started for has, and leaves when the next magpie
// starts (Leave), finishing the turns it is streaming first: no running
// turn is cut.
package codexrelay

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/settings"
)

// Command is the magpie command a relay runs as.
const Command = "codex-relay"

// LeavePath is where the next magpie tells the relay to leave; with
// TokenHeader holding the token the relay wrote in its state file.
const (
	LeavePath   = "/.magpie-codex-relay/leave"
	TokenHeader = "X-Magpie-Relay-Token"
)

// Timings, vars for tests.
var (
	// bindFor is how long the relay waits for the gateway's port: the
	// quitting magpie holds it until its own requests have finished.
	bindFor = 60 * time.Second
	// watchEvery is how often the relay looks for its Codex processes.
	watchEvery = 15 * time.Second
	// drainFor caps how long a leaving relay finishes its streams.
	drainFor = 30 * time.Minute
	// leaveWait is how long Leave waits for the relay to answer.
	leaveWait = 2 * time.Second
)

// state is what a running relay says of itself, for Leave.
type state struct {
	Pid   int    `json:"pid"`
	Addr  string `json:"addr"`
	Token string `json:"token"`
}

func statePath() string { return filepath.Join(settings.Dir(), "codex-relay.json") }

// Start runs a relay at addr for the Codex processes pids, apart from
// this process so it outlives it.
func Start(addr string, pids []int) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	ids := make([]string, len(pids))
	for i, p := range pids {
		ids[i] = strconv.Itoa(p)
	}
	cmd := proc.Command(exe, Command, "--addr", addr, "--pids", strings.Join(ids, ","))
	detach(cmd)
	// reaped when it ends while magpie runs: a released one stayed
	// <defunct> until magpie quit
	return proc.StartReaped(cmd)
}

// Main is `magpie codex-relay --addr host:port --pids 1,2`, serving h.
func Main(args []string, h http.Handler) error {
	var addr string
	var pids []int
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--addr":
			if i+1 < len(args) {
				addr = args[i+1]
				i++
			}
		case "--pids":
			if i+1 < len(args) {
				for _, s := range strings.Split(args[i+1], ",") {
					if s == "" {
						continue
					}
					n, err := strconv.Atoi(s)
					if err != nil || n <= 0 {
						return fmt.Errorf("codex-relay: bad pid %q", s)
					}
					pids = append(pids, n)
				}
				i++
			}
		default:
			return fmt.Errorf("codex-relay: unknown argument %q (magpie starts this itself when it quits)", args[i])
		}
	}
	if addr == "" || len(pids) == 0 {
		return errors.New("usage: magpie codex-relay --addr host:port --pids pid,… (magpie starts this itself when it quits)")
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	ctx, stop := context.WithCancel(context.Background())
	go func() { <-sig; stop() }()
	return Run(ctx, addr, pids, h)
}

// Run serves h at addr until ctx ends, the next magpie tells it to leave,
// or none of pids runs and nothing is in flight; then it finishes the
// requests it has, for up to drainFor.
func Run(ctx context.Context, addr string, pids []int, h http.Handler) error {
	ln, err := bind(ctx, addr)
	if err != nil {
		return err
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		ln.Close()
		return err
	}
	me := state{Pid: os.Getpid(), Addr: ln.Addr().String(), Token: hex.EncodeToString(b)}
	if err := writeState(me); err != nil {
		ln.Close()
		return err
	}
	defer dropState(me.Token)

	var inFlight atomic.Int64
	left := make(chan struct{})
	var leaving atomic.Bool
	srv := &http.Server{ReadHeaderTimeout: 30 * time.Second}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == LeavePath {
			tok := r.Header.Get(TokenHeader)
			if r.Method != http.MethodPost || r.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(tok), []byte(me.Token)) != 1 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			// the port is free once this is answered: the next magpie
			// binds it at once, while the streams here finish
			srv.SetKeepAlivesEnabled(false)
			ln.Close()
			if leaving.CompareAndSwap(false, true) {
				close(left)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		inFlight.Add(1)
		defer inFlight.Add(-1)
		h.ServeHTTP(w, r)
	})
	go srv.Serve(ln)

	t := time.NewTicker(watchEvery)
	defer t.Stop()
wait:
	for {
		select {
		case <-ctx.Done():
			break wait
		case <-left:
			break wait
		case <-t.C:
			if inFlight.Load() == 0 && !anyAlive(pids) {
				break wait
			}
		}
	}
	drain, cancel := context.WithTimeout(context.Background(), drainFor)
	defer cancel()
	srv.Shutdown(drain)
	return nil
}

// bind listens at addr, waiting up to bindFor for it to be let go of.
func bind(ctx context.Context, addr string) (net.Listener, error) {
	end := time.Now().Add(bindFor)
	for {
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			return ln, nil
		}
		if time.Now().After(end) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func anyAlive(pids []int) bool {
	for _, p := range pids {
		if alive(p) {
			return true
		}
	}
	return false
}

// writeState writes the relay's state where only this user reads it,
// whole or not at all.
func writeState(st state) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	p := statePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".codex-relay-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

func readState() (state, bool) {
	b, err := os.ReadFile(statePath())
	if err != nil {
		return state{}, false
	}
	var st state
	if json.Unmarshal(b, &st) != nil || st.Pid <= 0 || st.Addr == "" || st.Token == "" {
		return state{}, false
	}
	return st, true
}

// dropState removes the state file when it is still this relay's.
func dropState(token string) {
	if st, ok := readState(); ok && st.Token == token {
		os.Remove(statePath())
	}
}

// Leave tells a relay serving at the gateway's port to let go of it,
// for the magpie starting to serve there. It returns once the relay has
// closed its listener (its streams go on finishing), or at once when
// there is none.
func Leave() {
	st, ok := readState()
	if !ok {
		return
	}
	if !alive(st.Pid) {
		os.Remove(statePath())
		return
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+st.Addr+LeavePath, nil)
	if err != nil {
		return
	}
	req.Header.Set(TokenHeader, st.Token)
	c := &http.Client{Timeout: leaveWait, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	if res, err := c.Do(req); err == nil {
		res.Body.Close()
	}
}
