package plugin

import (
	"bufio"
	"bytes"
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
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/proc"
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
	Event   string            `json:"event"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Data    string            `json:"data"`
	// raw is Data decoded, filled by push so the body reads it without
	// decoding the same frame a second time. It is never on the wire.
	raw      []byte
	Provider string `json:"provider"`
	Account  string `json:"account"`
	Said     string `json:"said"`
	Level    string `json:"level"`
	Message  string `json:"message"`
	Title    string `json:"title"`
	Variant  string `json:"variant"`
	Count    int    `json:"count"`
	// Loads is a "load" event's models and how busy each is, a percent;
	// -1 clears one (magpie.load in host.js)
	Loads map[string]float64 `json:"loads"`
	// Usage is a "usage" event's request, one the plugin sent its vendor
	// on its own (magpie.usage in host.js)
	Usage *OwnCall `json:"usage"`
}

// OwnCall is a request a plugin sent its vendor on its own, outside any
// request magpie handed it, as it reported it (magpie.usage in host.js):
// Devin's cache keepalive ping (plugins#592). Kind says what it was for
// ("cache_keepalive"), Model is the model's id as magpie handed it to the
// plugin, Input the prompt's tokens without the cache's, Status the
// vendor's (0 for none) and Millis how long it took.
type OwnCall struct {
	Kind       string `json:"kind"`
	Model      string `json:"model"`
	Input      int    `json:"input"`
	Output     int    `json:"output"`
	CacheRead  int    `json:"cacheRead"`
	CacheWrite int    `json:"cacheWrite"`
	Status     int    `json:"status"`
	Millis     int64  `json:"ms"`
	Error      string `json:"error"`
}

type callState uint8

const (
	callSending callState = iota
	callActive
	callDraining
	callAborting
	callReleased
)

type callEvent uint8

const (
	callAccepted callEvent = iota
	callAnswered
	callCancelled
	callConsumed
	callAbortWritten
	callHostFailed
)

type call struct {
	done   chan message // the answer
	closed atomic.Bool  // done has been answered

	// A fetch's head and chunks wait here, in the order they came, until
	// the body's reader takes them: the host's reader queues them without
	// ever waiting on one request's consumer (see push).
	stream      bool
	cancellable bool // a gateway request or sign-in step owns asynchronous I/O, without a stream slot
	mu          sync.Mutex
	queue       []message
	queued      int           // the queued chunks' decoded bytes
	over        error         // why the call was given up on
	stopped     bool          // the call is over for good: nothing more is queued
	wake        chan struct{} // buffered 1: something was queued, or the call is over

	// window is this call's credit in charged bytes (a frame's payload plus
	// frameOverhead) and owed is how much of it the child has spent and the
	// reader has not yet given back: the child sends no more than window-owed,
	// so the queue stays within the window however slow the reader is. A child
	// that sends past it broke the protocol (see push).
	window int
	owed   int
	// headed is whether the reply's head has been queued: a second one is a
	// broken child, not another free line.
	headed bool

	// h and id are how the call is given up: the child is told to stop its
	// fetch only while the call is still the host's.
	h  *host
	id int64

	// The host lock owns lifecycle transitions; the queue lock also protects
	// the persistent answer so notifications never own the vendor outcome.
	state    callState
	terminal *message

	// stopCancel stops the context.AfterFunc that gives the call up when its
	// request's context ends, so a body nothing reads or closes still stops
	// the upstream fetch. It is cleared the once it is called.
	stopCancel func() bool
}

// The credit window bounds one call's queue; the budget bounds every live
// call's windows together, so the host admits at most streamBudget/streamWindow
// streaming calls at once (see begin). Both count charged bytes: a frame's
// payload plus frameOverhead, so the window bounds a backlog's frame metadata
// as well as its bytes (the base64 on the wire, about 4/3 of the payload, is
// held besides). The child splits its frames to maxFrame and spends its window
// frame by frame, so a compliant child never sends past the window. callLimit
// bounds every live or retiring call together, so the control backlog (at most
// one entry a call) is bounded too. Tests lower them.
var (
	streamWindow = 512 << 10
	streamBudget = 64 << 20
	maxFrame     = 64 << 10
	// frameOverhead is what one queued frame costs the window besides its
	// bytes: the message struct, its base64 string header and the decoded
	// slice behind it. Without it a producer of 1-byte frames could keep a
	// window's worth of tiny structs queued — bytes are not the only memory
	// a backlog holds. It is charged in the same units as the window, so both
	// sides count one number.
	frameOverhead = 2048
	rpcReserve    = 64
	// maxWireLine bounds one line the child writes: a frame's base64 is well
	// under it, and so is any RPC result, so a child that never ends a line
	// fails the host rather than making it allocate without bound.
	maxWireLine = 32 << 20
	// streamWait is how long a fetch waits for a stream's window to come
	// free when the budget's are all held, while its request lives. Past
	// the budget a fetch used to fail at once (errTooManyCalls), and the
	// gateway counted that as a 502 and tried again: 192 streams at once
	// through one plugin gave 298 local refusals that never reached the
	// vendor (#3890).
	streamWait = time.Minute
	// maxHeadBytes and maxHeadEntries are magpie's own head-envelope policy,
	// not HTTP's: a fetch's head carries at most maxHeadBytes of header bytes
	// (the UTF-8 bytes of every name and value, plus headEntryCost an entry)
	// and maxHeadEntries entries, counted the same on both sides. It keeps a
	// vendor's unbounded header set from being a free 0-charge line. A head
	// handed to its caller is the caller's to hold besides.
	maxHeadBytes       = 64 << 10
	maxHeadEntries     = 256
	headEntryCost      = 4 // a name/value pair's JSON punctuation
	errTooManyCalls    = errors.New("too many plugin calls are in flight; try again shortly")
	errProtocolOverrun = errors.New("the plugin sent more of a reply than the host credited; that request was given up on")
	errHostGone        = errors.New("the plugin host is gone")
	errLineTooLong     = errors.New("the plugin host sent a line longer than the host reads")
)

// callLimit is the most calls (streaming, RPC or retiring) the host holds at
// once. It follows from the budget: the streaming share of it plus a reserve
// for RPCs, so the control backlog is bounded by it.
func callLimit() int { return streamLimit() + rpcReserve }

// streamLimit is how many streaming calls the budget's windows allow at once:
// each holds one window of credit, so their queues together stay within the
// budget. It is counted apart from the call limit, so the RPC reserve is never
// spent on one more stream.
func streamLimit() int { return streamBudget / streamWindow }

// push queues m for the call's reader, without waiting for it. It is false
// when the call was given up on and m was not taken: a compliant child never
// sends past the call's window, so this is a call already stopped, or a child
// that broke the protocol (see errProtocolOverrun).
func (c *call) push(m message) bool {
	c.h.mu.Lock()
	if c.state != callActive {
		c.h.mu.Unlock()
		return false
	}
	c.mu.Lock()
	if c.stopped || c.over != nil {
		c.mu.Unlock()
		c.h.mu.Unlock()
		return false
	}
	cost := 0
	switch m.Event {
	case "head":
		// exactly one head, and it is free: a repeat would be an unbounded
		// run of zero-cost lines. Its headers are capped by magpie's own
		// envelope, so an unbounded header set is not a free line either.
		if c.headed || len(m.Headers) > maxHeadEntries || headCost(m.Headers) > maxHeadBytes {
			return c.rejectFrameLocked()
		}
		c.headed = true
	case "chunk":
		// a chunk carries the reply's bytes: it must be base64 of one to
		// maxFrame bytes. An empty, malformed or oversized frame is refused
		// before it is queued, so a flood of empty chunks cannot grow the
		// queue without spending the window. The frame is decoded once, here,
		// so the body reads it without decoding the same bytes again.
		raw, err := base64.StdEncoding.DecodeString(m.Data)
		if err != nil || len(raw) == 0 || len(raw) > maxFrame {
			return c.rejectFrameLocked()
		}
		m.raw = raw
		cost = len(raw) + frameOverhead
	default:
		// an event the host does not know: refuse it rather than queue it for
		// free
		return c.rejectFrameLocked()
	}
	if c.window > 0 && c.owed+cost > c.window {
		// more than the window ahead of the reader: the child broke the
		// protocol. The call is given up on and its queue dropped, rather
		// than growing without bound.
		return c.rejectFrameLocked()
	}
	c.queue = append(c.queue, m)
	c.queued += len(m.raw)
	c.owed += cost
	c.mu.Unlock()
	c.h.mu.Unlock()
	c.notify()
	return true
}

func (c *call) rejectFrameLocked() bool {
	c.mu.Unlock()
	stop := c.transitionLocked(callCancelled, errProtocolOverrun, nil)
	c.h.mu.Unlock()
	if stop != nil {
		stop()
	}
	return false
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
	c.queued -= len(m.raw)
	return m, true
}

// credit gives back the bytes the reader took, so the child may send that much
// more of this call's reply. It is what keeps a slow reader's upstream paused
// and a fast reader's flowing, without the host ever cutting a reply short.
func (c *call) credit(n int) {
	if n <= 0 {
		return
	}
	c.mu.Lock()
	if c.stopped || c.over != nil {
		c.mu.Unlock()
		return
	}
	if n > c.owed {
		n = c.owed
	}
	c.owed -= n
	c.mu.Unlock()
	if n > 0 {
		c.h.credit(c, n)
	}
}

// givenUp is why the call's backlog passed its limit, nil for one it didn't.
func (c *call) givenUp() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.over
}

// watchCtx arms the context watcher that gives the call up when its request's
// context ends, so a body nothing reads or closes still stops the upstream
// fetch. The watcher is registered under h.mu and the callback settles outside
// it, so a context already cancelled can fire before stopCancel is set without
// racing it.
func (c *call) watchCtx(ctx context.Context) {
	stop := context.AfterFunc(ctx, func() { c.settle(context.Cause(ctx)) })
	c.h.mu.Lock()
	if c.state == callReleased || c.state == callAborting {
		c.h.mu.Unlock()
		stop()
		return
	}
	c.stopCancel = stop
	c.h.mu.Unlock()
}

// transitionLocked owns cleanup under h.mu, always taking c.mu after it.
// The returned watcher detachment runs outside both locks. A retiring call
// stays registered until its abort write is acknowledged, even when the
// control loop has already taken its work out of the control map.
func (c *call) transitionLocked(event callEvent, why error, answer *message) func() bool {
	if c.state == callReleased {
		return nil
	}
	if event == callAccepted {
		if c.state == callSending {
			c.state = callActive
		}
		return nil
	}
	if event == callAnswered && c.state != callActive ||
		event == callHostFailed && c.state == callDraining ||
		event == callAbortWritten && c.state != callAborting ||
		event == callConsumed && c.state == callAborting ||
		event == callCancelled && c.state == callAborting {
		return nil
	}
	c.mu.Lock()
	if event == callAnswered {
		c.terminal = answer
		c.state = callDraining
	} else {
		if c.state != callDraining && c.over == nil {
			c.over = why
		}
		c.queue, c.queued, c.owed = nil, 0, 0
		if c.terminal == nil && why != nil {
			m := message{Error: &struct {
				Message string `json:"message"`
			}{c.over.Error()}}
			c.terminal = &m
		}
		if event == callCancelled && c.state == callActive && (c.stream || c.cancellable) {
			c.state = callAborting
		} else {
			c.state = callReleased
		}
	}
	c.stopped = true
	if c.terminal != nil && c.closed.CompareAndSwap(false, true) {
		c.done <- *c.terminal
	}
	c.mu.Unlock()
	c.h.ctrlMu.Lock()
	delete(c.h.ctrl, c.id)
	if c.state == callAborting {
		c.h.ctrl[c.id] = &ctrl{c: c, abort: true}
		c.h.wakeCtrl()
	}
	c.h.ctrlMu.Unlock()
	if c.state != callAborting {
		delete(c.h.calls, c.id)
	}
	if c.state == callReleased {
		c.h.releaseSlot(c)
	}
	// Readers may consume wake, so lifecycle changes broadcast to every
	// writer-room waiter rather than competing for a reader notification.
	if c.h.outSpace != nil {
		close(c.h.outSpace)
		c.h.outSpace = nil
	}
	c.notify()
	if c.state == callDraining {
		return nil
	}
	stop := c.stopCancel
	c.stopCancel = nil
	return stop
}

func (c *call) transition(event callEvent, why error, answer *message) {
	c.h.mu.Lock()
	stop := c.transitionLocked(event, why, answer)
	c.h.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (c *call) settle(why error) { c.transition(callCancelled, why, nil) }
func (c *call) release()         { c.transition(callConsumed, nil, nil) }

func (c *call) result() *message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.terminal
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
	cmd *exec.Cmd
	in  io.WriteCloser
	// out carries messages to the single writer goroutine that owns the
	// child's stdin. A caller enqueues and returns, so it is never parked in
	// a blocked write; the queue is bounded by the call limit, so a wedged
	// child applies backpressure instead of growing the backlog.
	out      chan writeReq
	outSpace chan struct{}
	mu       sync.Mutex
	calls    map[int64]*call
	next     int64
	dead     chan struct{}
	err      error
	loaded   []Loaded
	// renewing is how many sign-ins the host is renewing: stop lets them
	// end, their new tokens saved, before it kills the host
	renewing atomic.Int32
	// slots bounds every live or retiring call together (see callLimit), so
	// neither the calls nor the control backlog can grow without bound.
	slots chan struct{}
	// streams bounds the streaming calls to the budget's windows (see
	// streamLimit), counted apart from slots so the RPC reserve is kept.
	streams chan struct{}
	// ctrl is the control work waiting for the child: credit to give back
	// and fetches to abort. One goroutine (ctrlLoop) writes it, so a fetch's
	// reader never blocks on the child's stdin; the backlog is bounded by
	// slots, one entry a call, so no control message is dropped.
	ctrlMu   sync.Mutex
	ctrl     map[int64]*ctrl
	ctrlWake chan struct{}
	// ctx ends when the host does, so a control write waiting on the child
	// gives up rather than parking on a host that is gone.
	ctx    context.Context
	cancel context.CancelFunc
	// maxLine overrides maxWireLine for this host; tests lower it.
	maxLine int
	// failOnce makes the one failure that ends the host run once, whatever
	// noticed it (the stdout reader or the stdin writer).
	failOnce sync.Once
	// proxy is the proxy the host was started with (hostProxy), which
	// host.js keeps for its life; proxyAt is when get last compared it
	// with magpie's now (proxyMoved), zero for a host not started here.
	proxy   string
	proxyAt time.Time
	// addr is the gateway's address the host was told (hostAddr)
	addr string
	// output is the child's stdout and stderr, read apart from its Wait,
	// and waited gives what that Wait said; both are nil in the tests'
	// fake hosts, which have no child
	output *proc.Output
	waited func() error
	// tree is the child with what it starts (Output.StartTree), ended
	// together; nil in the fake hosts
	tree *proc.Tree
}

// wait is the child's end: what Wait said, once it has ended and the
// reader is done with its stdout.
func (h *host) wait() error {
	if h.waited == nil {
		return h.cmd.Wait()
	}
	return h.waited()
}

// reap ends the host the moment its child ends, not once its stdout
// does. A program the host started that kept the host's stdout (a
// plugin's Bun.spawn given a copy of fd 1, a daemon it left behind) held
// the pipe open past the host's end, so the reader never reached EOF,
// the child was never waited for, and every host stopped left a
// <defunct> magpie-bun behind until magpie quit (acezan on Discord: 57
// after 47 hours as a systemd service). proc.Output waits for the child
// at once and closes the pipe a moment later, which ends the reader too.
func (h *host) reap() {
	h.fail(quitErr(h.wait()))
	live.Lock()
	delete(live.hosts, h)
	live.Unlock()
}

// live are the hosts started and not yet reaped, the retired ones on their
// way out among them.
var live = struct {
	sync.Mutex
	hosts map[*host]bool
	// ended is set by EndHosts: no host starts after it
	ended bool
}{hosts: map[*host]bool{}}

// errHostsEnded is a host asked for once EndHosts has run.
var errHostsEnded = errors.New("magpie is restarting; the plugins are back in a moment")

// EndHosts stops every plugin host, waits until each has been waited for,
// and starts none after: what magpie runs before it puts a new version in
// its own place (update.Reexec). A host still running at the exec was one
// nothing waited for after it, so each restart to update (magpie web's
// AutoRestart) left one <defunct> magpie-bun under magpie's pid for as long
// as it ran (#3285, found by skygunner and AAEE86; acezan on Discord saw
// 57 after 47 hours as a systemd service).
func EndHosts() {
	live.Lock()
	live.ended = true
	hs := make([]*host, 0, len(live.hosts))
	for h := range live.hosts {
		hs = append(hs, h)
	}
	live.Unlock()
	hostMu.Lock()
	current = nil
	hostMu.Unlock()
	var wg sync.WaitGroup
	for _, h := range hs {
		wg.Go(func() {
			h.stop()
			_ = h.wait()
		})
	}
	wg.Wait()
}

// quitErr is the error of a host whose child ended with err.
func quitErr(err error) error {
	if err == nil {
		err = errors.New("the plugin host quit")
	}
	return fmt.Errorf("the plugin host quit: %v", err)
}

// lineLimit is the longest line this host reads from the child.
func (h *host) lineLimit() int {
	if h.maxLine > 0 {
		return h.maxLine
	}
	return maxWireLine
}

// headCost is a head's header cost: the UTF-8 bytes of every name and value,
// plus headEntryCost an entry. The child's side counts the same, so neither
// counts JSON escaping the other does not (a Link header's angle brackets, say)
// and a head one side takes is not refused by the other.
func headCost(h map[string]string) int {
	n := 0
	for k, v := range h {
		n += len(k) + len(v) + headEntryCost
	}
	return n
}

// ctrl is a call's pending control work: credit coalesces, an abort is once.
type ctrl struct {
	c      *call
	abort  bool
	credit int
}

// writeReq is one message for the writer: its result goes to done, when the
// sender needs it (a control abort, whose slot comes back once it is written).
type writeReq struct {
	b    []byte
	done chan error
}

// Loaded is how a plugin fared when the host loaded it.
type Loaded struct {
	Spec  string `json:"spec"`
	Error string `json:"error,omitempty"`
	// Provides are the provider ids its auth hooks sign in to
	Provides []string `json:"provides,omitempty"`
	// ServedBy is, for each of them another plugin signs in to and
	// serves, that plugin's spec: the host runs one plugin per provider
	// id (Prefer)
	ServedBy map[string]string `json:"servedBy,omitempty"`
	// Tools are the tools its tool hook gives (tools.go)
	Tools []Tool `json:"tools,omitempty"`
	// Searches are the web search APIs its search hook gives (search.go)
	Searches []SearchDef `json:"searches,omitempty"`
}

// Clash is a provider id more than one plugin signs in to. The host runs
// one of them for it: the one the user picked (Prefer), else the last in
// plugins.json, as OpenCode does. The others' sign-in to it goes unused.
type Clash struct {
	ID string `json:"id"` // the provider id, OpenCode's
	By string `json:"by"` // the spec of the plugin that serves it
	// With are the other plugins that sign in to it
	With []string `json:"with"`
}

// Clashes are the provider ids each loaded plugin shares with another,
// by the plugin's spec.
func Clashes(loaded []Loaded) map[string][]Clash {
	who := map[string][]string{}
	var ids []string
	for _, l := range loaded {
		for _, id := range l.Provides {
			if who[id] == nil {
				ids = append(ids, id)
			}
			who[id] = append(who[id], l.Spec)
		}
	}
	out := map[string][]Clash{}
	for _, id := range ids {
		specs := who[id]
		if len(specs) < 2 {
			continue
		}
		by := ""
		for _, l := range loaded {
			if slices.Contains(specs, l.Spec) && l.ServedBy[id] == "" {
				by = l.Spec
			}
		}
		for _, s := range specs {
			out[s] = append(out[s], Clash{ID: id, By: by, With: slices.DeleteFunc(slices.Clone(specs), func(x string) bool { return x == s })})
		}
	}
	return out
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
	// onChangeNow are run before changed or listed returns (OnChangeNow)
	onChangeNow []func()
)

// telling counts the hooks told of a change or of a sign-in (OnChange,
// OnSignIn) that are still running, for Told. It is a count, not a WaitGroup: a host can tell a change while
// Told waits.
var telling struct {
	sync.Mutex
	*sync.Cond
	n int
}

func init() { telling.Cond = sync.NewCond(&telling.Mutex) }

// OnChange registers f to be told when a plugin sign-in or the plugins
// change (a sign-in saved or refreshed, a plugin added).
func OnChange(f func()) {
	onChangeMu.Lock()
	onChange = append(onChange, f)
	onChangeMu.Unlock()
}

// OnChangeNow registers f to be run at once when a plugin sign-in, the
// plugins or their listing change, before anything after it is answered:
// an "auth" event is read before the reply to the sign-in that saved it,
// so a look made once the sign-in returns doesn't get what was built
// before it (#2449). OnChange's are told in the background, where a busy
// machine may run them after that look. f must be quick: the host's
// replies wait for it.
func OnChangeNow(f func()) {
	onChangeMu.Lock()
	onChangeNow = append(onChangeNow, f)
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

var (
	onUsageMu sync.Mutex
	onUsage   func(provider, account string, u OwnCall)
)

// OnUsage registers f to be told of each request a plugin reported it
// sent on its own (OwnCall): its provider id and the account key it was
// sent for. f is told in the background.
func OnUsage(f func(provider, account string, u OwnCall)) {
	onUsageMu.Lock()
	onUsage = f
	onUsageMu.Unlock()
}

var (
	onListedMu sync.Mutex
	onListed   []func()
)

// OnListed registers f to be told when the plugins have listed their
// providers and models anew and the list is another than the one kept
// before: a plugin signed in to lists its account's models only once the
// host has answered, after OnChange was told of the sign-in.
func OnListed(f func()) {
	onListedMu.Lock()
	onListed = append(onListed, f)
	onListedMu.Unlock()
}

func listed() {
	changedNow()
	onListedMu.Lock()
	fs := append([]func(){}, onListed...)
	onListedMu.Unlock()
	for _, f := range fs {
		tell(f)
	}
}

func changed() {
	forgetProviders()
	changedNow()
	onChangeMu.Lock()
	fs := append([]func(){}, onChange...)
	onChangeMu.Unlock()
	for _, f := range fs {
		tell(f)
	}
}

func changedNow() {
	// the sign-ins are read again, even by a request holding the files it
	// read (filememo.Hold): a sign-in just saved is in what comes next
	filememo.Look(AuthPath())
	onChangeMu.Lock()
	fs := append([]func(){}, onChangeNow...)
	onChangeMu.Unlock()
	for _, f := range fs {
		f()
	}
}

// tell runs a hook in the background, counted in telling until it ends.
func tell(f func()) {
	telling.Lock()
	telling.n++
	telling.Unlock()
	go func() {
		defer func() {
			telling.Lock()
			if telling.n--; telling.n == 0 {
				telling.Broadcast()
			}
			telling.Unlock()
		}()
		f()
	}()
}

// Restart stops the host, so the next call starts one with the plugins
// as they are now. A host answering calls (a reply streaming, a sign-in
// waiting for its browser) finishes them first, while the calls made
// from now on go to the new one: a plugin updating never cuts a reply.
func Restart() {
	// the next host loads the plugins as they are now: what this magpie
	// installed is not another magpie's change (checkList)
	listSeen.Lock()
	listSeen.stamp, listSeen.set = listStamp(), true
	listSeen.Unlock()
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

// busy is whether h is answering a call: a slot is held by every live or
// retiring call, so this counts a call whose body is still draining too.
func (h *host) busy() bool {
	return len(h.slots) > 0
}

// Running is whether a host is up.
func Running() bool {
	hostMu.Lock()
	defer hostMu.Unlock()
	return current != nil && current.alive()
}

// Warm starts the host if none is up and Bun is here. A plugin's page in
// the window whose plugin has a part for the host too (a local service
// its page goes to, as Magiczerowxy/magpie-chat's does) is served by the
// host, which is otherwise started only by a call: at launch, while the
// providers are first read, and after every Restart (a plugin installed,
// updated or set, a Bun update) only once something asks a plugin again.
// Opening such a page is that ask. A host that is up goes through get
// too, so one started before the gateway moved (hostAddr) is started
// again with the address the page's service needs.
func Warm(ctx context.Context) error {
	if !Running() && !HasBun() {
		return nil
	}
	_, err := get(ctx)
	return err
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
	h.kill()
}

// kill ends the host's child and every process it started: a plugin's
// headless browser or daemon outlived a host killed alone, with no parent
// to end it (620 msedge.exe and 57GB of temp profiles in an hour on one
// Windows machine, #5402). The tree is the host's session on Unix and its
// job on Windows (proc.StartTree), so nothing outside it is reached.
func (h *host) kill() {
	if h.tree != nil {
		h.tree.Kill()
		return
	}
	if h.cmd.Process != nil {
		_ = h.cmd.Process.Kill()
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
	return getLocked(ctx)
}

// getLocked requires hostMu. Gateway hooks bound both the lock wait and
// startup with their request-hook budget.
func getLocked(ctx context.Context) (*host, error) {
	if hostStale.Swap(false) && current != nil {
		go current.retire()
		current = nil
	}
	if current != nil && current.alive() && current.proxyMoved() {
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
	if crashed {
		err = whyDied(ctx, bun, err)
	}
	if err == nil || !crashed || os.Getenv("MAGPIE_BUN") != "" {
		// a Bun that can't be started at all isn't fallen back from: the
		// one before is the same build for this system, and setting this
		// one aside would mark a good version bad (#1877)
		return h, cantStart(bun, err)
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

var (
	catalogSyncing  sync.WaitGroup
	catalogCancelMu sync.Mutex
	catalogCancels  = map[int]context.CancelFunc{}
	catalogCancelID int
	syncCatalog     = catalog.SyncIfMissing // a var for tests
)

func cancelCatalogSyncs() {
	catalogCancelMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(catalogCancels))
	for _, c := range catalogCancels {
		cancels = append(cancels, c)
	}
	catalogCancels = map[int]context.CancelFunc{}
	catalogCancelMu.Unlock()
	for _, c := range cancels {
		c()
	}
	catalogSyncing.Wait()
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
		// OpenCode; sync in the background so starting the host never stalls
		// on models.dev (#1922, #1928).
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		catalogCancelMu.Lock()
		catalogCancelID++
		id := catalogCancelID
		catalogCancels[id] = cancel
		catalogCancelMu.Unlock()

		catalogSyncing.Add(1)
		go func() {
			defer catalogSyncing.Done()
			defer func() {
				catalogCancelMu.Lock()
				delete(catalogCancels, id)
				catalogCancelMu.Unlock()
				cancel()
			}()
			if err := syncCatalog(cctx); err == nil {
				forgetProviders()
			}
		}()
	}
	// the host outlives the request that started it
	proxy := hostProxy(env()) // what bunCommand gives it
	cmd := bunCommand(context.Background(), bun, settings.Dir(), "run", js)
	addr := hostAddr(cmd.Env)
	log.Printf("plugin: host starts with %s", netLine(cmd.Env))
	cmd.Env = hostEnv(cmd.Env)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, false, err
	}
	// stdout and stderr are read apart from Wait (proc.Output), so the
	// child is reaped when it ends, whatever holds a copy of them
	output, err := proc.NewOutput(cmd)
	if err != nil {
		return nil, false, err
	}
	// started and counted live under one lock, so EndHosts misses none
	live.Lock()
	if live.ended {
		live.Unlock()
		output.Close()
		return nil, false, errHostsEnded
	}
	// in a tree of its own: what its plugins start is ended with it
	tree, waited, err := output.StartTree(cmd)
	if err != nil {
		live.Unlock()
		return nil, false, err
	}
	hctx, hcancel := context.WithCancel(context.Background())
	h := &host{
		cmd: cmd, in: in,
		calls:    map[int64]*call{},
		dead:     make(chan struct{}),
		slots:    make(chan struct{}, callLimit()),
		streams:  make(chan struct{}, streamLimit()),
		out:      make(chan writeReq, callLimit()),
		ctrl:     map[int64]*ctrl{},
		ctrlWake: make(chan struct{}, 1),
		ctx:      hctx,
		cancel:   hcancel,
		proxy:    proxy,
		proxyAt:  time.Now(),
		addr:     addr,
		output:   output,
		waited:   waited,
		tree:     tree,
	}
	live.hosts[h] = true
	live.Unlock()
	go func() {
		drainStderr(output.Err)
		output.ErrRead()
	}()

	go h.read(output.Out)
	go h.reap()
	go h.writerLoop()
	go h.ctrlLoop()
	generation.Add(1)

	l := Load()
	type item struct {
		Spec    string         `json:"spec"`
		Target  string         `json:"target"`
		Options map[string]any `json:"options,omitempty"`
		Gateway string         `json:"gateway,omitempty"`
	}
	var items []item
	for _, e := range l.Plugins {
		// a plugin that is only gateway middleware or an agent runs in
		// magpie (internal/middleware, internal/agentplug), not here
		if !e.Off && !InMagpieOnly(Target(e.Spec)) {
			items = append(items, item{e.Spec, Target(e.Spec), hostOptions(Target(e.Spec), e.Options), Gateway(Target(e.Spec))})
		}
	}
	var res struct {
		Plugins []Loaded `json:"plugins"`
	}
	ictx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	catalogPath := catalog.Source()
	if catalogPath == "" {
		catalogPath = catalog.CachePath()
	}
	err = h.call(ictx, "init", map[string]any{
		"authPath":      AuthPath(),
		"modelsDevPath": catalogPath,
		"piPath":        pi,
		"directory":     settings.Dir(),
		"config":        l.Config,
		"prefer":        l.Prefer,
		"options":       settings.Load().PluginOptions,
		"plugins":       items,
		"loadMillis":    pluginLoadWait.Milliseconds(),
		"listMillis":    pluginListWait.Milliseconds(),
	}, &res)
	if err != nil {
		crashed := !h.alive()
		h.stop()
		return nil, crashed, fmt.Errorf("starting plugins: %w", err)
	}
	h.loaded = res.Plugins
	noteTools(res.Plugins)
	noteSearches(res.Plugins)
	for _, p := range res.Plugins {
		if p.Error != "" {
			log.Printf("plugin %s didn't load: %s", p.Spec, p.Error)
		}
	}
	return h, false, nil
}

// ServePages sets whether this magpie's host runs the services plugins'
// pages talk to (magpie.page.service), true until it is set. The app,
// magpie web, serve, tray and tui do; a command such as magpie plugin
// update doesn't, and its host may start while the app's host holds the
// service's port: a plugin that waits for its port then holds the
// command up, and goes on trying it in the background, to take it from
// the app's host when that restarts (#5187).
func ServePages(on bool) { noPageServices.Store(!on) }

var noPageServices atomic.Bool

// hostOptions are the options the host hands the plugin at target. In a
// magpie that runs no page services, a service the plugin's option
// moves is moved to a free port of its own, so the plugin's other parts
// load at once beside the app's host holding the real one. One no
// option moves is handed the plugin's options as they are.
func hostOptions(target string, options map[string]any) map[string]any {
	if !noPageServices.Load() {
		return options
	}
	w, _ := Window(target)
	if w == nil || w.Page == nil || w.Page.Service == nil || w.Page.Service.Option == "" {
		return options
	}
	port, err := freePort()
	if err != nil {
		return options
	}
	out := make(map[string]any, len(options)+1)
	maps.Copy(out, options)
	out[w.Page.Service.Option] = float64(port)
	return out
}

// freePort is a TCP port on 127.0.0.1 nothing listens on just now.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// pluginLoadWait bounds one plugin's load in the host, and
// pluginListWait one provider's model list in a listing: a plugin stuck on
// its vendor (a sign-in refused and tried again) is given up on by itself,
// within init's and refreshDeadline's own bounds, rather than holding up
// every plugin loaded or listed with it. Tests shorten them.
var (
	pluginLoadWait = 20 * time.Second
	pluginListWait = 20 * time.Second
)

// drainStderr logs the child's stderr a line at a time. A line over 4 MiB
// stops the scanner; the rest is read and dropped, so the child is never left
// blocked on a pipe nobody reads, and the log says from where.
func drainStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		log.Printf("plugin: %s", sc.Text())
	}
	if errors.Is(sc.Err(), bufio.ErrTooLong) {
		log.Printf("plugin: a stderr line over 4 MiB; the plugin's stderr is dropped from here on")
	}
	io.Copy(io.Discard, r)
}

func (h *host) read(out io.Reader) {
	r := bufio.NewReaderSize(out, 1<<20)
	for {
		line, err := readLine(r, h.lineLimit())
		if len(line) > 0 {
			if m, ok := chunkFrame(line); ok {
				h.dispatch(m)
			} else if json.Unmarshal(line, &m) == nil {
				h.dispatch(m)
			}
		}
		if errors.Is(err, errLineTooLong) {
			// a child that never ends a line: stop it rather than allocate
			h.fail(fmt.Errorf("the plugin host sent a line longer than %d bytes", h.lineLimit()))
			h.kill()
			break
		}
		if err != nil {
			break
		}
	}
	if h.output != nil {
		h.output.OutRead()
	}
	h.fail(quitErr(h.wait()))
}

var (
	chunkHead = []byte(`{"id":`)
	chunkMid  = []byte(`,"event":"chunk","data":"`)
)

// chunkFrame is the chunk frame in line when it has exactly the shape
// host.js's chunkLine writes, {"id":N,"event":"chunk","data":"…"}, with
// nothing in the data JSON would escape: the reply's bytes come in these,
// and encoding/json scanned every base64 byte of each, several times what
// decoding them costs. Any other line is not one, and is unmarshalled; push
// checks the data is base64 of a frame's size either way.
func chunkFrame(line []byte) (message, bool) {
	line = bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
	rest, ok := bytes.CutPrefix(line, chunkHead)
	if !ok {
		return message{}, false
	}
	i := bytes.IndexByte(rest, ',')
	if i <= 0 || i > 18 {
		return message{}, false
	}
	var id int64
	for _, c := range rest[:i] {
		if c < '0' || c > '9' {
			return message{}, false
		}
		id = id*10 + int64(c-'0')
	}
	data, ok := bytes.CutPrefix(rest[i:], chunkMid)
	if !ok {
		return message{}, false
	}
	data, ok = bytes.CutSuffix(data, []byte(`"}`))
	if !ok || bytes.IndexByte(data, '"') >= 0 || bytes.IndexByte(data, '\\') >= 0 {
		return message{}, false
	}
	return message{ID: id, Event: "chunk", Data: string(data)}, true
}

// readLine reads one '\n'-terminated line, at most max bytes, so a child that
// never ends a line cannot make the host allocate without bound. It is
// errLineTooLong once max is passed.
func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		if len(line)+len(part) > max {
			return nil, errLineTooLong
		}
		line = append(line, part...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}

func (h *host) dispatch(m message) {
	if m.ID == 0 {
		switch m.Event {
		case "auth":
			changed()
		case "recovered":
			// the host read plugin-auth.json from its last good
			// generation (#1505): the GUI says so
			lastgood.Noted(AuthPath(), m.Said)
		case "renewing":
			h.renewing.Store(int32(m.Count))
		case "signIn":
			onSignInMu.Lock()
			f := onSignIn
			onSignInMu.Unlock()
			if f != nil {
				// counted, so Told waits for it: it writes the
				// account's lapse into logins.json (#1935)
				tell(func() { f(m.Provider, m.Account, m.Said) })
			}
		case "load":
			noteLoads(m.Provider, m.Loads, time.Now())
		case "usage":
			onUsageMu.Lock()
			f := onUsage
			onUsageMu.Unlock()
			if f != nil && m.Usage != nil && m.Provider != "" {
				u := *m.Usage
				tell(func() { f(m.Provider, m.Account, u) })
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
	h.mu.Unlock()
	if c == nil {
		return
	}
	if m.Event != "" {
		if c.stream {
			c.push(m)
		}
		return
	}
	c.transition(callAnswered, nil, &m)
}

// enqueue hands one message to the writer goroutine. It waits only for room in
// the bounded queue, honoring ctx and the host's own end, so a caller is never
// parked in a blocked write and a host that is gone makes it return. Messages
// keep their order, so an abort is written after the fetch it belongs to.
func (h *host) enqueue(ctx context.Context, v any) error {
	return h.enqueueDone(ctx, v, nil)
}

// enqueueDone is enqueue, asking the writer to report the write's result on
// done (nil for a write that succeeded).
func (h *host) enqueueDone(ctx context.Context, v any, done chan error) error {
	return h.enqueueCall(ctx, v, done, nil)
}

// enqueueCall commits acceptance with the lifecycle while holding h.mu.
// Waiting for writer room owns no lifecycle lock, so cancellation can
// release an unsent call and the next attempt cannot enqueue it.
func (h *host) enqueueCall(ctx context.Context, v any, done chan error, c *call) error {
	return h.enqueueOwned(ctx, v, done, c, callSending)
}

func (h *host) enqueueOwned(ctx context.Context, v any, done chan error, c *call, state callState) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return h.enqueueLine(ctx, append(b, '\n'), done, c, state)
}

// fetchLine is Fetch's call id for r, as the line the host reads, byte
// for byte what json.Marshal wrote of it: its body in base64, written
// once into a line made for it. json.Marshal grew its buffer to the
// encoded body twice over and copied it out twice more, so a 1 MB request
// was some 5 MB more before it reached the plugin (#3472).
func fetchLine(id int64, r FetchRequest) ([]byte, error) {
	head, err := json.Marshal(map[string]any{"id": id, "method": "fetch", "params": r})
	if err != nil {
		return nil, err
	}
	// head ends in params' "}" and the call's: body and window go before
	// them, in the order the params struct gave them
	tail := `,"window":` + strconv.Itoa(streamWindow) + "}}\n"
	n := len(head) - 2 + len(tail)
	if len(r.Body) > 0 {
		n += len(`,"body":""`) + base64.StdEncoding.EncodedLen(len(r.Body))
	}
	b := make([]byte, 0, n)
	b = append(b, head[:len(head)-2]...)
	if len(r.Body) > 0 {
		b = append(b, `,"body":"`...)
		b = base64.StdEncoding.AppendEncode(b, r.Body)
		b = append(b, '"')
	}
	return append(b, tail...), nil
}

// enqueueLine is enqueueOwned for a call already a line.
func (h *host) enqueueLine(ctx context.Context, line []byte, done chan error, c *call, state callState) error {
	req := writeReq{b: line, done: done}
	for {
		h.mu.Lock()
		if !h.alive() {
			h.mu.Unlock()
			return errHostGone
		}
		if err := ctx.Err(); err != nil {
			h.mu.Unlock()
			return err
		}
		if c != nil && c.state != state {
			h.mu.Unlock()
			if err := c.givenUp(); err != nil {
				return err
			}
			return errHostGone
		}
		select {
		case h.out <- req:
			if c != nil && state == callSending {
				c.transitionLocked(callAccepted, nil, nil)
			}
			h.mu.Unlock()
			return nil
		default:
		}
		if h.outSpace == nil {
			h.outSpace = make(chan struct{})
		}
		space := h.outSpace
		h.mu.Unlock()
		select {
		case <-space:
		case <-ctx.Done():
			return ctx.Err()
		case <-h.dead:
			return errHostGone
		}
	}
}

func (h *host) tookWrite() {
	h.mu.Lock()
	if h.outSpace != nil {
		close(h.outSpace)
		h.outSpace = nil
	}
	h.mu.Unlock()
}

// writerLoop is the one goroutine that writes the child's stdin, until the
// host is gone or a write fails. A failed write fails the host, so nobody
// waiting on a write result is left waiting for a host that is already gone.
func (h *host) writerLoop() {
	for {
		select {
		case <-h.dead:
			return
		case req := <-h.out:
			h.tookWrite()
			if !h.alive() {
				if req.done != nil {
					req.done <- errHostGone
				}
				return
			}
			_, err := h.in.Write(req.b)
			if err != nil {
				// End the host before the writer's caller hears of it, so a
				// caller freed by the failure finds the host already dead.
				h.fail(fmt.Errorf("writing to the plugin host: %w", err))
			}
			if req.done != nil {
				req.done <- err
			}
			if err != nil {
				return
			}
		}
	}
}

// fail ends the host once: its context and dead channel, every message still
// queued for the writer answered with err, and every incomplete call let go —
// so no caller waits for a write that will never come, and a body nothing reads
// or closes cannot keep its buffer or slot.
func (h *host) fail(err error) {
	h.failOnce.Do(func() {
		h.mu.Lock()
		if h.err == nil {
			h.err = err
		}
		why := h.err
		close(h.dead)
		var stops []func() bool
		for _, c := range h.calls {
			if stop := c.transitionLocked(callHostFailed, why, nil); stop != nil {
				stops = append(stops, stop)
			}
		}
		h.mu.Unlock()
		for _, stop := range stops {
			stop()
		}
		h.cancel()
		h.dropCtrl()
	drain:
		for {
			select {
			case req := <-h.out:
				if req.done != nil {
					req.done <- err
				}
			default:
				break drain
			}
		}
	})
}

// credit queues the bytes the reader gave back for a call. The control loop
// coalesces them with any already waiting, so a fast reader's many small
// credits become one write.
func (h *host) credit(c *call, n int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ctrlMu.Lock()
	if !h.alive() || c.state != callActive {
		h.ctrlMu.Unlock()
		return
	}
	ct := h.ctrl[c.id]
	if ct == nil {
		ct = &ctrl{c: c}
		h.ctrl[c.id] = ct
	}
	ct.credit += n
	h.ctrlMu.Unlock()
	h.wakeCtrl()
}

// abort queues a fetch to stop. It is not dropped: the call keeps its slot
// until the abort is written (or the host dies), so a child that stopped
// reading its stdin cannot grow the backlog past the call limit.
func (h *host) abort(c *call) {
	c.settle(context.Canceled)
}

func (h *host) wakeCtrl() {
	select {
	case h.ctrlWake <- struct{}{}:
	default:
	}
}

// ctrlLoop is the one goroutine that writes control work to the child, until
// the host is gone.
func (h *host) ctrlLoop() {
	for {
		select {
		case <-h.dead:
			h.dropCtrl()
			return
		case <-h.ctrlWake:
		}
		h.pumpCtrl()
	}
}

// pumpCtrl writes the control work waiting, an abort before a credit, until
// none is left. A call settling with an abort owed keeps its slot until that
// abort's write is known (written, or failed with the host), so a child that
// stopped reading its stdin cannot grow the backlog past the call limit.
func (h *host) pumpCtrl() {
	for {
		h.ctrlMu.Lock()
		var ct *ctrl
		for id, v := range h.ctrl {
			ct = v
			delete(h.ctrl, id)
			break
		}
		h.ctrlMu.Unlock()
		if ct == nil {
			return
		}
		if ct.abort {
			done := make(chan error, 1)
			if err := h.enqueueOwned(h.ctx, map[string]any{"method": "abort", "params": map[string]any{"id": ct.c.id}}, done, ct.c, callAborting); err == nil {
				select {
				case <-done:
				case <-h.dead:
				}
			}
			ct.c.transition(callAbortWritten, nil, nil)
		}
		if ct.credit > 0 {
			_ = h.enqueueOwned(h.ctx, map[string]any{"method": "credit", "params": map[string]any{"id": ct.c.id, "n": ct.credit}}, nil, ct.c, callActive)
		}
	}
}

// dropCtrl drops work after lifecycle transitions have released failed calls.
func (h *host) dropCtrl() {
	h.ctrlMu.Lock()
	defer h.ctrlMu.Unlock()
	for id := range h.ctrl {
		delete(h.ctrl, id)
	}
}

// begin takes a call's admission slot and registers it. It is errTooManyCalls
// when the host already holds its limit of live and retiring calls, so a new
// call fails clearly rather than a legitimate accepted reply being cut short.
func (h *host) begin(stream bool) (int64, *call, error) {
	if stream {
		// a stream holds one window of the budget as well as a call slot, so
		// the RPC reserve is never spent on one more stream
		select {
		case h.streams <- struct{}{}:
		default:
			return 0, nil, errTooManyCalls
		}
	}
	return h.admit(stream)
}

// beginStream is begin for a fetch: while the budget's windows are all held
// it waits for one to come free, for streamWait at most and only while ctx
// lives, rather than failing a request that would have run a moment later.
// The wait is outside h.mu, which the call that frees a window takes.
func (h *host) beginStream(ctx context.Context) (int64, *call, error) {
	select {
	case h.streams <- struct{}{}:
	default:
		t := time.NewTimer(streamWait)
		defer t.Stop()
		select {
		case h.streams <- struct{}{}:
		case <-ctx.Done():
			return 0, nil, context.Cause(ctx)
		case <-h.dead:
			return 0, nil, errHostGone
		case <-t.C:
			return 0, nil, errTooManyCalls
		}
	}
	return h.admit(true)
}

// admit registers a call whose stream window, if it is a stream, is taken.
func (h *host) admit(stream bool) (int64, *call, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.alive() {
		if stream {
			<-h.streams
		}
		return 0, nil, errHostGone
	}
	c := &call{done: make(chan message, 1), stream: stream, wake: make(chan struct{}, 1), state: callSending}
	if stream {
		c.window = streamWindow
	}
	select {
	case h.slots <- struct{}{}:
	default:
		if stream {
			<-h.streams
		}
		return 0, nil, errTooManyCalls
	}
	h.next++
	id := h.next
	c.h, c.id = h, id
	h.calls[id] = c
	return id, c, nil
}

// releaseSlot is called only by the transition that enters released.
func (h *host) releaseSlot(c *call) {
	<-h.slots
	if c.stream {
		<-h.streams
	}
}

// cancellable are the methods whose work the host ends when their caller
// gives up: a gateway request's I/O, and a sign-in's steps, so a sign-in
// cancelled or timed out saves nothing the plugin answers after (#5016).
var cancellable = map[string]bool{"gatewayRequest": true, "authorize": true, "callback": true, "apiKey": true}

// call asks the host method with params and reads its answer into out.
func (h *host) call(ctx context.Context, method string, params, out any) error {
	id, c, err := h.begin(false)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.cancellable = cancellable[method]
	c.mu.Unlock()
	if err := h.enqueueCall(ctx, map[string]any{"id": id, "method": method, "params": params}, nil, c); err != nil {
		c.settle(err)
		return err
	}
	select {
	case m := <-c.done:
		c.release()
		if m.Error != nil {
			return errors.New(m.Error.Message)
		}
		if out != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-ctx.Done():
		c.settle(ctx.Err())
		return ctx.Err()
	case <-h.dead:
		// the host is gone: a waiter must not be left waiting for an answer
		// that will never come, even with a context that never ends
		m := c.result()
		c.release()
		if m != nil {
			if m.Error != nil {
				return errors.New(m.Error.Message)
			}
			if out != nil && len(m.Result) > 0 {
				return json.Unmarshal(m.Result, out)
			}
			return nil
		}
		return errHostGone
	}
}

// Call starts the host if need be and asks it method.
func Call(ctx context.Context, method string, params, out any) error {
	return callWithTimeout(ctx, method, params, out, 0)
}

// Background listings allow the host its own startup budget. The optional
// timeout begins only after initialization and bounds just the requested RPC.
func callWithTimeout(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	// a read the user didn't ask for, held back (#1518): the plugin isn't
	// asked either, nor does its host start for it
	if err := netproxy.Held(ctx); err != nil {
		return err
	}
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

// hostProxy is the proxy env gives a host: its *_PROXY, as netproxy.Env
// put them there from magpie's Settings or the system's.
func hostProxy(env []string) string {
	var b strings.Builder
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		switch strings.ToUpper(k) {
		case "HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY":
			b.WriteString(e)
			b.WriteByte(0)
		}
	}
	return b.String()
}

// proxyLookEvery is how often get asks whether the proxy moved: the
// system's is read again at most every 15 seconds anyway (netproxy.System).
// A host started with none asks every 2 seconds, as netproxy reads the
// system's again then: at login the proxy app comes up after magpie
// (#1518), and the host's requests went direct until it was asked.
var (
	proxyLookEvery = 15 * time.Second
	proxyLookNone  = 2 * time.Second
)

// proxied is whether a hostProxy names a proxy at all.
func proxied(hp string) bool {
	for _, e := range strings.Split(hp, "\x00") {
		k, v, _ := strings.Cut(e, "=")
		if v != "" && !strings.EqualFold(k, "NO_PROXY") {
			return true
		}
	}
	return false
}

// proxyMoved is whether magpie's proxy is no longer the one h was started
// with (#1363): host.js reads it once, so a host started at login before
// the proxy app had set the system's went out with none for good. Grok's
// sign-in, renewed by its CLI through the host, then lapsed after every
// restart of the computer, and only a new host (moving the account back
// to the built-in and on to the plugin again) brought it back. A proxy
// changed in Settings never reached a running host either. Nor did the
// gateway's address (hostAddr): a port changed in Settings is the next
// host's. Called under hostMu.
func (h *host) proxyMoved() bool {
	every := proxyLookEvery
	if !proxied(h.proxy) {
		every = min(every, proxyLookNone)
	}
	if h.proxyAt.IsZero() || time.Since(h.proxyAt) < every {
		return false
	}
	h.proxyAt = time.Now()
	e := env()
	if p := hostProxy(e); p != h.proxy {
		log.Printf("plugin: the proxy moved, from %s to %s; a new host takes over",
			netLine(strings.Split(h.proxy, "\x00")), netLine(strings.Split(p, "\x00")))
		return true
	}
	if a := hostAddr(e); h.addr != "" && a != h.addr {
		log.Printf("plugin: the gateway moved, from %s to %s; a new host takes over", h.addr, a)
		return true
	}
	return false
}

// netLine is a host's network settings in env as a log line says them:
// its proxy, without the user and password (netproxy.Redact), NO_PROXY,
// and what tells Bun which roots to trust. A host's TLS or proxy failure
// ("unable to get issuer certificate" for a chain that verifies outside
// it, youyo.p_10001) is read beside it.
func netLine(env []string) string {
	keys := []string{"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY",
		"NODE_EXTRA_CA_CERTS", "NODE_USE_SYSTEM_CA", "NODE_TLS_REJECT_UNAUTHORIZED", "SSL_CERT_FILE", "SSL_CERT_DIR"}
	got := map[string]string{}
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		k = strings.ToUpper(k)
		if slices.Contains(keys, k) {
			got[k] = v // exec takes the last
		}
	}
	var parts []string
	for _, k := range keys {
		v := strings.TrimSpace(got[k])
		if v == "" {
			continue
		}
		if strings.HasSuffix(k, "_PROXY") && k != "NO_PROXY" {
			if v = netproxy.Redact(v); v == "" {
				v = "(malformed)"
			}
		}
		parts = append(parts, k+"="+v)
	}
	if !slices.ContainsFunc(parts, func(p string) bool { return strings.Contains(p, "_PROXY=") && !strings.HasPrefix(p, "NO_PROXY=") }) {
		parts = append([]string{"no proxy"}, parts...)
	}
	return strings.Join(parts, " ")
}

// hostAddr is the gateway's address a host is told, as MAGPIE_ADDR: the
// variable's own value when magpie runs under one, else Settings' port on
// loopback. A plugin with a service of its own reaches magpie's gateway
// there: Magiczerowxy/magpie-chat's chat server asks MAGPIE_ADDR, then
// 3425, so with the port set in Settings (or a portable magpie's) its
// model list was empty and every message failed, "Unable to connect"
// (yuesh1_ on X, Magiczerowxy/magpie-chat#1). A gateway listening on
// every interface (MAGPIE_ADDR=0.0.0.0:…) is reached on loopback, as
// gateway.URL does.
func hostAddr(env []string) string {
	a := ""
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok && strings.EqualFold(k, "MAGPIE_ADDR") && v != "" {
			a = v
		}
	}
	if a == "" {
		return settings.SavedAddr()
	}
	if h, p, err := net.SplitHostPort(a); err == nil && (h == "" || net.ParseIP(h) != nil && net.ParseIP(h).IsUnspecified()) {
		a = net.JoinHostPort("127.0.0.1", p)
	}
	return a
}

// hostEnv is env for the host, MAGPIE_ADDR the gateway's (hostAddr), its
// *_PROXY named MAGPIE_*_PROXY: Bun
// reads *_PROXY once and puts every fetch through them, so a provider set
// to "direct" couldn't go around them. host.js gives each fetch the proxy
// they name instead. A SOCKS5 one, which Bun can't use, is bridged.
func hostEnv(env []string) []string {
	out := make([]string, 0, len(env)+1)
	addr := hostAddr(env)
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		switch strings.ToUpper(k) {
		case "HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY":
			out = append(out, "MAGPIE_"+k+"="+netproxy.ForBun(v))
		case "NO_PROXY":
			out = append(out, "MAGPIE_"+k+"="+v)
		case "MAGPIE_ADDR":
			// an empty one is Settings' (hostAddr)
		default:
			out = append(out, e)
		}
	}
	return append(out, "MAGPIE_ADDR="+addr)
}

// Fetch sends r through the provider's plugin — its loader's fetch, or
// Bun's own when it gives none — and streams back the reply.
func Fetch(ctx context.Context, r FetchRequest) (*http.Response, error) {
	if err := netproxy.Held(ctx); err != nil {
		return nil, err
	}
	h, err := get(ctx)
	if err != nil {
		return nil, err
	}
	if r.Proxy == "" {
		r.Proxy = proxyOf(ctx)
	}
	id, c, err := h.beginStream(ctx)
	if err != nil {
		return nil, err
	}
	line, err := fetchLine(id, r)
	if err != nil {
		c.settle(err)
		return nil, err
	}
	c.watchCtx(ctx)
	err = h.enqueueLine(ctx, line, nil, c, callSending)
	if err != nil {
		c.settle(err)
		return nil, err
	}
	head, answer, err := c.headRead(ctx)
	if err != nil {
		// Settling either releases a completed answer or stops an active fetch.
		c.settle(err)
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

// bodyIdleWait is how long a reply's body may say nothing — no byte between
// the last and the next — before its read gives the fetch up: a network that
// died mid-stream can leave a local proxy's socket open but soundless, and a
// read waiting on the plugin's next chunk then never ends (a stalled read
// ends only on a chunk, an answer, a close, a cancelled ctx or the host's
// death). The wait is between bytes, not the reply's whole length: every
// chunk resets it, so a stream a slow model feeds goes on however long it
// talks. Thirty minutes, the silence the gateway keeps a translated
// reply's client alive through (translatedQuietLongest): a reply that
// comes in one burst at its end (a model at its highest effort, a relay
// that buffers) is silent for its whole generation, past 10 minutes, and
// is not cut; a dead connection's read ends at the same bound.
var bodyIdleWait = 30 * time.Minute

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
		if m := c.result(); m != nil {
			if m.Error != nil {
				return message{}, m, errors.New(m.Error.Message)
			}
			return message{}, m, errors.New("the plugin answered no response")
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
		case <-c.h.dead:
			// the host is gone: no head is coming, so the fetch gives up
			// rather than waiting on a context that may never end
			continue
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
	frame  int           // the frame in hand's overhead, credited when it is done
	answer *message      // the call's answer, once it came
	closed chan struct{} // closed when the body is closed or given up on
	once   sync.Once
}

func (b *body) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil // a read that asks for nothing reads nothing
	}
	// the idle watchdog: what waits here with no bytes in hand is ended
	// bodyIdleWait after the last one came, so a plugin gone silent mid-reply
	// (its socket half-alive on a dead network, #4934) ends the read instead
	// of holding it for ever. The wait runs only while this Read is the one
	// waiting, and every pass through the loop waits on a fresh window.
	idle := time.NewTimer(bodyIdleWait)
	defer idle.Stop()
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
			// the bytes are the reader's now: give the child that much
			// credit back, so a slow reader pauses its upstream and a fast
			// one keeps it flowing
			b.c.credit(n)
			return n, nil
		}
		if m, ok := b.c.pop(); ok {
			if b.frame > 0 {
				// the frame that was in hand is done: its overhead comes back
				// with the bytes already credited for it
				b.c.credit(b.frame)
			}
			b.buf = m.raw
			b.frame = frameOverhead
			continue
		}
		if err := b.c.givenUp(); err != nil {
			b.giveUp()
			return 0, err
		}
		if b.answer != nil {
			// the answer ends the reply: its error, or the end of it
			b.c.release()
			if b.answer.Error != nil {
				return 0, errors.New(b.answer.Error.Message)
			}
			return 0, io.EOF
		}
		if m := b.c.result(); m != nil {
			b.answer = m
			continue
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
		case <-b.h.dead:
			continue
		case <-idle.C:
			b.giveUp()
			return 0, fmt.Errorf("the plugin sent nothing for %s; that fetch was given up on", bodyIdleWait)
		}
		// whatever the wait answered, the silence counts afresh from here
		if !idle.Stop() {
			select {
			case <-idle.C:
			default:
			}
		}
		idle.Reset(bodyIdleWait)
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
		why := errBodyClosed
		if err := context.Cause(b.ctx); err != nil {
			why = err
		}
		b.c.settle(why)
	})
}
