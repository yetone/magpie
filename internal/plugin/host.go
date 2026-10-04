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

// piJS loads pi's extensions as plugins; host.js imports it when a plugin
// is pi's.
//
//go:embed pi.js
var piJS []byte

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
	Count    int               `json:"count"`
}

type call struct {
	done   chan message // the answer
	closed atomic.Bool  // done has been answered

	// A fetch's head and chunks wait here, in the order they came, until
	// the body's reader takes them: the host's reader queues them without
	// ever waiting on one request's consumer (see push).
	stream  bool
	mu      sync.Mutex
	queue   []message
	bytes   int           // the queued chunks' data, as they came (base64)
	over    error         // why the call's backlog passed its limit
	stopped bool          // the call is over for good: nothing more is queued
	wake    chan struct{} // buffered 1: something was queued, or the call is over

	// h and id are how the stall watchdog gives the call up: the child is
	// told to stop its fetch only while the call is still the host's.
	h  *host
	id int64

	// watch is the stall watchdog: it is armed when the backlog first
	// passes the soft watermark, and gives the call up only after stallIdle
	// without the reader taking anything. lastRead is the last read that
	// returned data (the zero time for none yet); before the first read the
	// grace runs from armAt, so a plugin thinking for a long while before
	// its reply begins is not taken for a stalled reader. watchGen makes a
	// callback from a timer that was stopped or re-armed a no-op.
	watch    *time.Timer
	watchGen int64
	armAt    time.Time
	lastRead time.Time
}

// A fetch's backlog is watched, not cut, at the soft watermark: past it the
// call's reader has stallIdle to take something, and only a reader that took
// nothing for that long is given up on. A reply larger than the watermark
// that is read, however slowly, still arrives whole; a consumer that stopped
// is let go of. The hard caps are the memory safety net: past either, however
// fast the reader, the call is given up on, one reply the plugin sends whole
// included. The byte caps bound the queued chunks (base64), not the process,
// and one frame already decoded is held besides. Tests lower them.
var (
	softQueuedBytes  = 4 << 20
	softQueuedEvents = 4096
	hardQueuedBytes  = 64 << 20
	hardQueuedEvents = 65536
	stallIdle        = 10 * time.Second
	errStalledReader = errors.New("the plugin kept sending a reply whose reader stopped reading (nothing was read for a while); that request was given up on")
	errTooMuchQueued = errors.New("the plugin sent more of a reply than the host queues (too much queued); that request was given up on")
)

// push queues m for the call's reader, without waiting for it. It is false
// when the call's backlog passed its hard limit and m was not taken: the call
// is given up on, and its reader is told why rather than a chunk going
// missing unnoticed.
func (c *call) push(m message) bool {
	c.mu.Lock()
	taken := false
	switch {
	case c.stopped || c.over != nil:
		// a call given up on takes nothing more
	default:
		// the backlog the queue would hold with this chunk in it, not the
		// chunk alone: the memory safety net counts the whole reply
		nbytes, nevents := c.bytes+len(m.Data), len(c.queue)+1
		if nbytes > hardQueuedBytes || nevents > hardQueuedEvents {
			c.over = errTooMuchQueued
			// the resource cap is terminal: the encoding is dropped now, not
			// held for a reader that may never come
			c.queue, c.bytes = nil, 0
			c.stopWatchLocked()
		} else {
			c.queue = append(c.queue, m)
			c.bytes = nbytes
			taken = true
			c.armWatchLocked()
		}
	}
	c.mu.Unlock()
	c.notify()
	return taken
}

// pop takes the next chunk queued for the call; false when none is queued.
func (c *call) pop() (message, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queue) == 0 {
		return message{}, false
	}
	m := c.queue[0]
	c.queue[0] = message{} // let the chunk's data go with the chunk
	c.queue = c.queue[1:]
	if len(c.queue) == 0 {
		c.queue = nil
	}
	c.bytes -= len(m.Data)
	if !c.overSoftLocked() {
		// the backlog is back under the watermark: nothing left to watch
		c.stopWatchLocked()
	}
	return m, true
}

// overSoftLocked is whether the backlog passed the soft watermark.
func (c *call) overSoftLocked() bool {
	return c.bytes > softQueuedBytes || len(c.queue) > softQueuedEvents
}

// armWatchLocked starts the stall watchdog when the backlog first passes the
// soft watermark; from then on the call's reader has stallIdle to take
// something. The callback carries the generation, so one left from a timer
// the call has since drained, closed or given up on does nothing.
func (c *call) armWatchLocked() {
	if c.watch != nil || c.stopped || c.over != nil || !c.overSoftLocked() {
		return
	}
	c.watchGen++
	gen := c.watchGen
	c.armAt = time.Now()
	c.watch = time.AfterFunc(stallIdle, func() { c.onStall(gen) })
}

// stopWatchLocked drops the stall watchdog: the call was drained, closed,
// cancelled or given up on, so there is nothing left to watch. Bumping the
// generation makes a callback already under way a no-op.
func (c *call) stopWatchLocked() {
	if c.watch != nil {
		c.watch.Stop()
		c.watch = nil
	}
	c.watchGen++
	c.armAt = time.Time{}
}

// noteRead is the reader saying it took data: real progress, unlike a chunk
// arriving or a read that asked for nothing. It only stamps the time under
// the call's own mutex; the watchdog reads it when it fires, so a read
// allocates no timer.
func (c *call) noteRead() {
	c.mu.Lock()
	c.lastRead = time.Now()
	c.mu.Unlock()
}

// onStall is the stall watchdog firing: the backlog passed the soft watermark
// and the reader took nothing for stallIdle, so the call is given up on. What
// it queued is dropped and its reader is told why; the child is told to stop
// only while the call is still the host's to give up (an answered call the
// host has already forgotten is left alone). gen is the generation this
// callback was armed with.
func (c *call) onStall(gen int64) {
	c.mu.Lock()
	if c.watchGen != gen || c.stopped || c.over != nil || !c.overSoftLocked() {
		c.mu.Unlock()
		return
	}
	ref := c.armAt
	if c.lastRead.After(ref) {
		ref = c.lastRead
	}
	if wait := stallIdle - time.Since(ref); wait > 0 {
		// the reader took something since the grace began: wait the rest
		c.watchGen++
		gen = c.watchGen
		c.watch = time.AfterFunc(wait, func() { c.onStall(gen) })
		c.mu.Unlock()
		return
	}
	// nothing was read for stallIdle: the call is given up on. Its own body
	// ends here, whether or not the host still holds the call.
	c.stopWatchLocked()
	c.over = errStalledReader
	c.queue, c.bytes = nil, 0
	c.mu.Unlock()
	c.notify()
	// Whether this call is still the host's is what tells the child to stop:
	// forget takes h.mu, so it is taken after c.mu is let go, and an answered
	// call the host already forgot is left alone.
	if c.h.forget(c.id) {
		c.h.abort(c.id)
	}
}

// givenUp is why the call's backlog passed its limit, nil for one it didn't.
func (c *call) givenUp() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.over
}

// release ends the call for good: nothing more is queued for it, and what it
// held is dropped, so a body given up on holds nothing. The stall watchdog
// goes with it.
func (c *call) release() {
	c.mu.Lock()
	c.stopped = true
	c.queue, c.bytes = nil, 0
	c.stopWatchLocked()
	c.mu.Unlock()
}

// notify wakes the call's reader, without waiting for it.
func (c *call) notify() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
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
	// renewing is how many sign-ins the host is renewing: stop lets them
	// end, their new tokens saved, before it kills the host
	renewing atomic.Int32
	// aborts are the fetch ids to tell the child to stop, taken by one
	// goroutine (abortLoop): the child's stdin can be full when it stopped
	// reading it, and no caller may wait on that.
	aborts chan int64
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

// stopWait is how long a stopped host has to finish its calls; while it
// is renewing a sign-in it has up to stopRenewing in all, since a vendor
// that rotates its refresh token has already spent the old one, and the
// account is signed out unless the new one is saved.
var (
	stopWait     = 2 * time.Second
	stopRenewing = 15 * time.Second
)

func (h *host) stop() {
	h.in.Close()
	start := time.Now()
	wait := time.NewTimer(stopWait)
	defer wait.Stop()
	select {
	case <-h.dead:
		return
	case <-wait.C:
	}
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for h.renewing.Load() > 0 && time.Since(start) < stopRenewing {
		select {
		case <-h.dead:
			return
		case <-tick.C:
		}
	}
	if h.cmd.Process != nil {
		h.cmd.Process.Kill()
	}
}

// hostFile is host.js written where Bun can run it.
func hostFile() (string, error) { return hostScript("host", hostJS) }

// hostScript is a script of the host written where Bun can run it, named
// by what it holds.
func hostScript(name string, js []byte) (string, error) {
	sum := sha256.Sum256(js)
	dir := filepath.Join(filepath.Dir(catalog.CachePath()), "plugin-host")
	p := filepath.Join(dir, name+"-"+hex.EncodeToString(sum[:6])+".js")
	if b, err := os.ReadFile(p); err == nil && string(b) == string(js) {
		return p, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, js, 0o644); err != nil {
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
	h, crashed, err := startOn(ctx, bun)
	if err == nil || !crashed || os.Getenv("MAGPIE_BUN") != "" {
		return h, err
	}
	// the Bun magpie last took died starting the host: the one before it,
	// and the new one set aside when that one starts it
	prev, ok := fallBack()
	if !ok {
		return nil, err
	}
	h, _, perr := startOn(ctx, prev)
	if perr != nil {
		return nil, err
	}
	setAside(filepath.Base(filepath.Dir(bun)), fmt.Sprintf("the plugin host died on it: %s", err))
	return h, nil
}

// startOn starts the host on the bun given; crashed is whether it died
// before it started.
func startOn(ctx context.Context, bun string) (*host, bool, error) {
	js, err := hostFile()
	if err != nil {
		return nil, false, err
	}
	pi, err := hostScript("pi", piJS)
	if err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		return nil, false, err
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
		return nil, false, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, false, err
	}
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}
	h := &host{cmd: cmd, in: in, calls: map[int64]*call{}, dead: make(chan struct{}), aborts: make(chan int64, 64)}
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		for sc.Scan() {
			log.Printf("plugin: %s", sc.Text())
		}
	}()
	go h.read(out)
	go h.abortLoop()
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
		"piPath":        pi,
		"directory":     settings.Dir(),
		"config":        l.Config,
		"plugins":       items,
	}, &res)
	if err != nil {
		crashed := !h.alive()
		h.stop()
		return nil, crashed, fmt.Errorf("starting plugins: %w", err)
	}
	h.loaded = res.Plugins
	for _, p := range res.Plugins {
		if p.Error != "" {
			log.Printf("plugin %s didn't load: %s", p.Spec, p.Error)
		}
	}
	return h, false, nil
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
		case "renewing":
			h.renewing.Store(int32(m.Count))
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
		if c.stream && !c.push(m) {
			// its backlog passed its limit: the call is over. Whoever
			// takes it out of the host's hands tells the child, once.
			if h.forget(m.ID) {
				h.abort(m.ID)
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

// abort tells the child to stop a fetch, without waiting for it: the child's
// stdin can be full when it stopped reading it, so the id is queued for
// abortLoop instead. Aborts are best-effort: a full queue drops the id, so
// its upstream fetch may keep running, including an endless stream.
func (h *host) abort(id int64) {
	select {
	case h.aborts <- id:
	default:
	}
}

// abortLoop is the one goroutine that writes the aborts abort queued, until
// the host is gone.
func (h *host) abortLoop() {
	for {
		select {
		case <-h.dead:
			return
		case id := <-h.aborts:
			_ = h.send(map[string]any{"method": "abort", "params": map[string]any{"id": id}})
		}
	}
}

func (h *host) begin(stream bool) (int64, *call) {
	c := &call{done: make(chan message, 1), stream: stream}
	if stream {
		c.wake = make(chan struct{}, 1)
	}
	h.mu.Lock()
	h.next++
	id := h.next
	c.h, c.id = h, id
	h.calls[id] = c
	h.mu.Unlock()
	return id, c
}

// forget takes the call out of the host's hands, and is true when it was
// still there: the one that takes it out is the one that tells the child.
func (h *host) forget(id int64) bool {
	h.mu.Lock()
	_, ok := h.calls[id]
	delete(h.calls, id)
	h.mu.Unlock()
	return ok
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
	return callWithTimeout(ctx, method, params, out, 0)
}

// Background listings allow the host its own startup budget. The optional
// timeout begins only after initialization and bounds just the requested RPC.
func callWithTimeout(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	startup := ctx
	if timeout > 0 {
		startup = context.WithoutCancel(ctx)
	}
	h, err := get(startup)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
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
// "direct", or the proxy's URL (a SOCKS5 one bridged, as Bun can't use it).
func proxyOf(ctx context.Context) string { return forHost(netproxy.Choice(ctx)) }

// forHost is a proxy choice (netproxy.With's) as the host takes it.
func forHost(choice string) string {
	switch c := strings.TrimSpace(choice); c {
	case "", "direct":
		return c
	default:
		if u, err := netproxy.Parse(c); err == nil {
			return netproxy.ForBun(u.String())
		}
		return c
	}
}

// hostEnv is env for the host, its *_PROXY named MAGPIE_*_PROXY: Bun
// reads *_PROXY once and puts every fetch through them, so a provider set
// to "direct" couldn't go around them. host.js gives each fetch the proxy
// they name instead. A SOCKS5 one, which Bun can't use, is bridged.
func hostEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		switch strings.ToUpper(k) {
		case "HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY":
			out = append(out, "MAGPIE_"+k+"="+netproxy.ForBun(v))
		case "NO_PROXY":
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
		Body []byte `json:"body,omitempty"`
	}{r, r.Body}
	if err := h.send(map[string]any{"id": id, "method": "fetch", "params": params}); err != nil {
		h.forget(id)
		return nil, err
	}
	head, answer, err := c.headRead(ctx)
	if err != nil {
		// no answer: the call is still the host's to give up, and its
		// taker is the one that tells the child
		if answer == nil && h.forget(id) {
			h.abort(id)
		}
		return nil, err
	}
	b := &body{h: h, id: id, c: c, ctx: ctx, answer: answer, closed: make(chan struct{})}
	res := &http.Response{
		StatusCode: head.Status,
		Status:     fmt.Sprintf("%d %s", head.Status, http.StatusText(head.Status)),
		Header:     http.Header{},
		Body:       b,
		Proto:      "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
	}
	for k, v := range head.Headers {
		res.Header.Set(k, v)
	}
	// the body is decoded already: fetch takes the Content-Encoding off
	res.Header.Del("Content-Encoding")
	res.Header.Del("Content-Length")
	return res, nil
}

// errBodyClosed is what a read after the body was closed gets.
var errBodyClosed = errors.New("the reply's body was closed")

// headRead waits for the call's reply head, giving back the answer it read on
// the way: the body has to end with that answer too, error and all.
func (c *call) headRead(ctx context.Context) (head message, answer *message, err error) {
	for {
		if m, ok := c.pop(); ok {
			return m, nil, nil
		}
		if err := c.givenUp(); err != nil {
			return message{}, nil, err
		}
		select {
		case <-c.wake:
		case m := <-c.done:
			// the answer came with the head: keep it for the body
			if hm, ok := c.pop(); ok {
				return hm, &m, nil
			}
			if m.Error != nil {
				return message{}, &m, errors.New(m.Error.Message)
			}
			return message{}, &m, errors.New("the plugin answered no response")
		case <-ctx.Done():
			return message{}, nil, ctx.Err()
		}
	}
}

// body is a fetch's reply body. Its reader takes the chunks the call queued,
// in the order they came; whoever reads it does the waiting, so nothing of
// magpie's is parked while a consumer that stopped reading holds one. A
// cancelled ctx is noticed in Read and Close; a consumer that neither reads
// nor closes is not waited on, and the stall watchdog gives it up when its
// backlog passes the watermark.
type body struct {
	h      *host
	id     int64
	c      *call
	ctx    context.Context
	buf    []byte        // the chunk in hand, not yet read
	answer *message      // the call's answer, once it came
	closed chan struct{} // closed when the body is closed or given up on
	once   sync.Once
}

func (b *body) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil // a read that asks for nothing reads nothing
	}
	for {
		// the end comes first, every time round: a closed body reads
		// nothing more, and a cancel is not left behind what is queued
		select {
		case <-b.closed:
			return 0, errBodyClosed
		default:
		}
		if err := b.ctx.Err(); err != nil {
			b.giveUp()
			return 0, err
		}
		if len(b.buf) > 0 {
			n := copy(p, b.buf)
			b.buf = b.buf[n:]
			if n > 0 {
				// real progress: the reader is taking the reply, so the
				// stall watchdog's grace starts over
				b.c.noteRead()
			}
			return n, nil
		}
		if m, ok := b.c.pop(); ok {
			chunk, err := base64.StdEncoding.DecodeString(m.Data)
			if err != nil {
				b.giveUp()
				return 0, err
			}
			b.buf = chunk
			continue
		}
		if err := b.c.givenUp(); err != nil {
			b.giveUp()
			return 0, err
		}
		if b.answer != nil {
			// the answer ends the reply: its error, or the end of it
			if b.answer.Error != nil {
				return 0, errors.New(b.answer.Error.Message)
			}
			return 0, io.EOF
		}
		select {
		case <-b.c.wake:
		case m := <-b.c.done:
			// the chunks came before the answer: what is still queued is
			// read first, at the top of the loop
			b.answer = &m
		case <-b.closed:
			return 0, errBodyClosed
		case <-b.ctx.Done():
			b.giveUp()
			return 0, b.ctx.Err()
		}
	}
}

// Close gives the call up: what a consumer that stopped reading, or is
// through, calls. It never waits on the child.
func (b *body) Close() error {
	b.giveUp()
	return nil
}

// giveUp ends the call once, however many of the body's paths get there: what
// it queued is dropped, and the child is told to stop the fetch if this is
// what took the call out of the host's hands.
func (b *body) giveUp() {
	b.once.Do(func() {
		close(b.closed)
		b.c.release()
		if b.h.forget(b.id) {
			b.h.abort(b.id)
		}
	})
}
