package plugin

import (
	"bufio"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/settings"
)

//go:embed host.js
var hostJS []byte

// message is a line the host writes.
type message struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
	Event    string            `json:"event"`
	Status   int               `json:"status"`
	Headers  map[string]string `json:"headers"`
	Data     string            `json:"data"`
	Provider string            `json:"provider"`
	Account  string            `json:"account"`
	Said     string            `json:"said"`
	Level    string            `json:"level"`
	Message  string            `json:"message"`
	Title    string            `json:"title"`
	Variant  string            `json:"variant"`
}

type call struct {
	done   chan message  // the answer
	events chan message  // a fetch's head and chunks, before its answer
	gone   chan struct{} // closed when no one reads events any more
	closed atomic.Bool   // done has been answered
}

// host is one Bun process running host.js.
type host struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	wmu    sync.Mutex
	mu     sync.Mutex
	calls  map[int64]*call
	next   int64
	dead   chan struct{}
	err    error
	loaded []Loaded
}

// Loaded is how a plugin fared when the host loaded it.
type Loaded struct {
	Spec  string `json:"spec"`
	Error string `json:"error,omitempty"`
}

var (
	hostMu  sync.Mutex
	current *host
	// generation counts the hosts started, so a caller can tell a
	// restart happened (the provider cache is then stale)
	generation atomic.Int64
	// onChange are told a sign-in or the plugins changed.
	onChange   []func()
	onChangeMu sync.Mutex
)

// OnChange registers f to be told when a plugin sign-in or the plugins
// change (a sign-in saved or refreshed, a plugin added).
func OnChange(f func()) {
	onChangeMu.Lock()
	onChange = append(onChange, f)
	onChangeMu.Unlock()
}

var (
	onSignInMu sync.Mutex
	onSignIn   func(provider, account, said string)
)

// OnSignIn registers f to be told what a plugin said, outside an answer,
// its account's sign-in is: "expired", "kept" or "renewed" (a models
// hook's error saying it).
func OnSignIn(f func(provider, account, said string)) {
	onSignInMu.Lock()
	onSignIn = f
	onSignInMu.Unlock()
}

func changed() {
	forgetProviders()
	onChangeMu.Lock()
	fs := append([]func(){}, onChange...)
	onChangeMu.Unlock()
	for _, f := range fs {
		go f()
	}
}

// Restart stops the host, so the next call starts one with the plugins
// as they are now. A host answering calls (a reply streaming, a sign-in
// waiting for its browser) finishes them first, while the calls made
// from now on go to the new one: a plugin updating never cuts a reply.
func Restart() {
	hostMu.Lock()
	h := current
	current = nil
	hostMu.Unlock()
	if h != nil {
		h.retire()
	}
	changed()
}

// retireWait is how long a retired host may go on answering its calls.
var retireWait = 10 * time.Minute

// retiring are the hosts finishing their calls before they stop.
var retiring = map[*host]bool{}

// retire stops h now when it answers no call, else once it has answered
// them (retireWait at most), in the background.
func (h *host) retire() {
	if !h.busy() {
		h.stop()
		return
	}
	hostMu.Lock()
	retiring[h] = true
	hostMu.Unlock()
	go func() {
		defer func() {
			hostMu.Lock()
			delete(retiring, h)
			hostMu.Unlock()
		}()
		end := time.Now().Add(retireWait)
		for h.busy() && time.Now().Before(end) {
			select {
			case <-h.dead:
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
		h.stop()
	}()
}

// busy is whether h is answering a call.
func (h *host) busy() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls) > 0
}

// Running is whether a host is up.
func Running() bool {
	hostMu.Lock()
	defer hostMu.Unlock()
	return current != nil && current.alive()
}

func (h *host) alive() bool {
	select {
	case <-h.dead:
		return false
	default:
		return true
	}
}

func (h *host) stop() {
	h.in.Close()
	select {
	case <-h.dead:
	case <-time.After(2 * time.Second):
		if h.cmd.Process != nil {
			h.cmd.Process.Kill()
		}
	}
}

// hostFile is host.js written where Bun can run it.
func hostFile() (string, error) {
	sum := sha256.Sum256(hostJS)
	dir := filepath.Join(filepath.Dir(catalog.CachePath()), "plugin-host")
	p := filepath.Join(dir, "host-"+hex.EncodeToString(sum[:6])+".js")
	if b, err := os.ReadFile(p); err == nil && string(b) == string(hostJS) {
		return p, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, hostJS, 0o644); err != nil {
		return "", err
	}
	return p, os.Rename(tmp, p)
}

// get is the running host, started (Bun downloaded, the plugins loaded)
// when there is none.
func get(ctx context.Context) (*host, error) {
	checkList()
	hostMu.Lock()
	defer hostMu.Unlock()
	if hostStale.Swap(false) && current != nil {
		go current.retire()
		current = nil
	}
	if current != nil && current.alive() {
		return current, nil
	}
	h, err := start(ctx)
	if err != nil {
		return nil, err
	}
	current = h
	return h, nil
}

func start(ctx context.Context) (*host, error) {
	bun, err := Bun(ctx)
	if err != nil {
		return nil, err
	}
	js, err := hostFile()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		return nil, err
	}
	if catalog.Source() == "" {
		// a plugin's provider has the models models.dev lists for it, as in
		// OpenCode; a magpie that has never fetched them does first
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_ = catalog.Sync(cctx)
		cancel()
	}
	// the host outlives the request that started it
	cmd := bunCommand(context.Background(), bun, settings.Dir(), "run", js)
	cmd.Env = hostEnv(cmd.Env)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	h := &host{cmd: cmd, in: in, calls: map[int64]*call{}, dead: make(chan struct{})}
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		for sc.Scan() {
			log.Printf("plugin: %s", sc.Text())
		}
	}()
	go h.read(out)
	generation.Add(1)

	l := Load()
	type item struct {
		Spec    string         `json:"spec"`
		Target  string         `json:"target"`
		Options map[string]any `json:"options,omitempty"`
	}
	var items []item
	for _, e := range l.Plugins {
		if !e.Off {
			items = append(items, item{e.Spec, Target(e.Spec), e.Options})
		}
	}
	var res struct {
		Plugins []Loaded `json:"plugins"`
	}
	ictx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	err = h.call(ictx, "init", map[string]any{
		"authPath":      AuthPath(),
		"modelsDevPath": catalog.Source(),
		"directory":     settings.Dir(),
		"config":        l.Config,
		"plugins":       items,
	}, &res)
	if err != nil {
		h.stop()
		return nil, fmt.Errorf("starting plugins: %w", err)
	}
	h.loaded = res.Plugins
	for _, p := range res.Plugins {
		if p.Error != "" {
			log.Printf("plugin %s didn't load: %s", p.Spec, p.Error)
		}
	}
	return h, nil
}

func (h *host) read(out io.Reader) {
	r := bufio.NewReaderSize(out, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var m message
			if json.Unmarshal(line, &m) == nil {
				h.dispatch(m)
			}
		}
		if err != nil {
			break
		}
	}
	err := h.cmd.Wait()
	h.mu.Lock()
	if err == nil {
		err = errors.New("the plugin host quit")
	}
	h.err = fmt.Errorf("the plugin host quit: %v", err)
	calls := h.calls
	h.calls = map[int64]*call{}
	h.mu.Unlock()
	close(h.dead)
	for _, c := range calls {
		if c.closed.CompareAndSwap(false, true) {
			c.done <- message{Error: &struct {
				Message string `json:"message"`
			}{h.err.Error()}}
		}
	}
}

func (h *host) dispatch(m message) {
	if m.ID == 0 {
		switch m.Event {
		case "auth":
			changed()
		case "signIn":
			onSignInMu.Lock()
			f := onSignIn
			onSignInMu.Unlock()
			if f != nil {
				go f(m.Provider, m.Account, m.Said)
			}
		case "toast":
			log.Printf("plugin: %s %s", m.Title, m.Message)
		case "log":
			log.Printf("plugin [%s]: %s", m.Level, m.Message)
		}
		return
	}
	h.mu.Lock()
	c := h.calls[m.ID]
	if m.Event == "" {
		delete(h.calls, m.ID)
	}
	h.mu.Unlock()
	if c == nil {
		return
	}
	if m.Event != "" {
		if c.events != nil {
			select {
			case c.events <- m:
			case <-c.gone:
			}
		}
		return
	}
	if c.closed.CompareAndSwap(false, true) {
		c.done <- m
	}
}

func (h *host) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	h.wmu.Lock()
	defer h.wmu.Unlock()
	_, err = h.in.Write(append(b, '\n'))
	return err
}

func (h *host) begin(events bool) (int64, *call) {
	c := &call{done: make(chan message, 1), gone: make(chan struct{})}
	if events {
		c.events = make(chan message, 64)
	}
	h.mu.Lock()
	h.next++
	id := h.next
	h.calls[id] = c
	h.mu.Unlock()
	return id, c
}

func (h *host) forget(id int64) {
	h.mu.Lock()
	delete(h.calls, id)
	h.mu.Unlock()
}

// call asks the host method with params and reads its answer into out.
func (h *host) call(ctx context.Context, method string, params, out any) error {
	id, c := h.begin(false)
	if err := h.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		h.forget(id)
		return err
	}
	select {
	case m := <-c.done:
		if m.Error != nil {
			return errors.New(m.Error.Message)
		}
		if out != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-ctx.Done():
		h.forget(id)
		return ctx.Err()
	}
}

// Call starts the host if need be and asks it method.
func Call(ctx context.Context, method string, params, out any) error {
	h, err := get(ctx)
	if err != nil {
		return err
	}
	return h.call(ctx, method, params, out)
}

// Plugins is how each plugin fared when the host loaded it, starting the
// host if need be.
func Plugins(ctx context.Context) ([]Loaded, error) {
	h, err := get(ctx)
	if err != nil {
		return nil, err
	}
	return h.loaded, nil
}

// FetchRequest is a request a provider's plugin makes.
type FetchRequest struct {
	Provider string            `json:"provider"`
	Account  string            `json:"account,omitempty"` // the provider's first when ""
	Model    string            `json:"model"`
	NPM      string            `json:"npm"`
	URL      string            `json:"url"`
	Method   string            `json:"method"`
	Headers  map[string]string `json:"headers"`
	Body     []byte            `json:"-"`
	Session  string            `json:"session,omitempty"`
	// Proxy is the provider's or the account's own proxy (netproxy.With):
	// "direct", or its URL; "" follows magpie's. Fetch takes it from ctx
	// when it isn't set.
	Proxy string `json:"proxy,omitempty"`
}

// proxyOf is the proxy ctx names for the host: "" when it names none,
// "direct", or the proxy's URL.
func proxyOf(ctx context.Context) string {
	switch c := strings.TrimSpace(netproxy.Choice(ctx)); c {
	case "", "direct":
		return c
	default:
		if u, err := netproxy.Parse(c); err == nil {
			return u.String()
		}
		return c
	}
}

// hostEnv is env for the host, its *_PROXY named MAGPIE_*_PROXY: Bun
// reads *_PROXY once and puts every fetch through them, so a provider set
// to "direct" couldn't go around them. host.js gives each fetch the proxy
// they name instead.
func hostEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		switch strings.ToUpper(k) {
		case "HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY":
			out = append(out, "MAGPIE_"+k+"="+v)
		default:
			out = append(out, e)
		}
	}
	return out
}

// Fetch sends r through the provider's plugin — its loader's fetch, or
// Bun's own when it gives none — and streams back the reply.
func Fetch(ctx context.Context, r FetchRequest) (*http.Response, error) {
	h, err := get(ctx)
	if err != nil {
		return nil, err
	}
	if r.Proxy == "" {
		r.Proxy = proxyOf(ctx)
	}
	id, c := h.begin(true)
	params := struct {
		FetchRequest
		Body string `json:"body,omitempty"`
	}{r, base64.StdEncoding.EncodeToString(r.Body)}
	if err := h.send(map[string]any{"id": id, "method": "fetch", "params": params}); err != nil {
		h.forget(id)
		return nil, err
	}
	abort := func() { _ = h.send(map[string]any{"method": "abort", "params": map[string]any{"id": id}}) }
	var head message
	select {
	case head = <-c.events:
	case m := <-c.done:
		if m.Error != nil {
			return nil, errors.New(m.Error.Message)
		}
		return nil, errors.New("the plugin answered no response")
	case <-ctx.Done():
		abort()
		h.forget(id)
		return nil, ctx.Err()
	}
	pr, pw := io.Pipe()
	res := &http.Response{
		StatusCode: head.Status,
		Status:     fmt.Sprintf("%d %s", head.Status, http.StatusText(head.Status)),
		Header:     http.Header{},
		Body:       pr,
		Proto:      "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
	}
	for k, v := range head.Headers {
		res.Header.Set(k, v)
	}
	// the body is decoded already: fetch takes the Content-Encoding off
	res.Header.Del("Content-Encoding")
	res.Header.Del("Content-Length")
	go func() {
		defer close(c.gone)
		write := func(data string) error {
			b, err := base64.StdEncoding.DecodeString(data)
			if err != nil {
				return err
			}
			_, err = pw.Write(b)
			return err
		}
		for {
			select {
			case m := <-c.events:
				if err := write(m.Data); err != nil {
					// the reader went away, or the chunk is garbled
					abort()
					h.forget(id)
					pw.CloseWithError(err)
					return
				}
			case m := <-c.done:
				// the chunks came before the answer: any still queued
				// are written first
			drain:
				for {
					select {
					case e := <-c.events:
						if write(e.Data) != nil {
							break drain
						}
					default:
						break drain
					}
				}
				if m.Error != nil {
					pw.CloseWithError(errors.New(m.Error.Message))
				} else {
					pw.Close()
				}
				return
			case <-ctx.Done():
				abort()
				h.forget(id)
				pw.CloseWithError(ctx.Err())
				return
			}
		}
	}()
	return res, nil
}
