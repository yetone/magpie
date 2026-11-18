// Package gwmw is the gateway's middleware point (P3 of
// docs/subsystems/plugin-runtime.md): what changes a request on its way
// through the gateway and its reply on the way back. It has two places,
// each with its own failure policy:
//
//   - Agent: a request an agent sent, in the agent's own API, before the
//     gateway routes it, and its reply as the agent gets it. The JS gateway
//     middleware (internal/middleware) is the one there, registered by
//     plugins/middleware. It fails open: a middleware that fails leaves the
//     request as it was. A Run that is also Tried changes the body again
//     for each provider the gateway tries the request on, and one that is
//     also Accounts can have an account passed over for the request.
//   - Masker: what magpie sends to a vendor, masked as the settings say,
//     and what the vendor answers with the values back. The redact plugin
//     registers it. It fails closed: when the settings ask for masking and
//     no masker is served, or the masker fails, nothing is sent.
package gwmw

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Info is what an agent middleware is told of a request.
type Info struct {
	Protocol string `json:"protocol"` // the agent's API: anthropic, chat, responses, gemini
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Path     string `json:"path"`
	Agent    string `json:"agent,omitempty"`
}

// Reject is a request a middleware turned away: the agent gets Status with
// Message in its API's error shape.
type Reject struct {
	Status  int
	Message string
}

// Agent is a middleware on agents' requests.
type Agent interface {
	// Begin is asked as a request starts; nil leaves it alone
	Begin(Info) Run
}

// Run is an agent middleware on one request.
type Run interface {
	// Request is the body the gateway, or the next middleware, gets; a
	// Reject ends the request there
	Request(body []byte) ([]byte, *Reject)
	// Wrap is the writer the reply goes through; done writes what it holds
	Wrap(w http.ResponseWriter) (http.ResponseWriter, func())
	// End is the request's end, after done
	End()
}

// To is the provider a request is tried on, as a middleware is told it.
type To struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Account string `json:"account,omitempty"` // the subscription's kind (claude, antigravity, …); empty for a key
	Model   string `json:"model"`             // the model asked of it, by its id there
}

// Tried is a Run that also changes the body each provider the request is
// tried on is sent: Tried is that try's body, in the agent's API, and
// changes no other try's.
type Tried interface {
	Tried(body []byte, to To) []byte
}

// Account is the subscription account a request is about to be tried on,
// as the gateway knows it. Agent, User and Plan are who it is
// (provider.Account) and never reach a middleware: a middleware is told a
// ref made from them (internal/middleware's accountRef). A zero Agent is
// a try on a key, no account.
type Account struct {
	Agent, User, Plan string
	ID, Name, Model   string // the provider, and the model asked of it there
}

// Accounts is a Run asked, before each try, whether the account it goes
// to may be used for this request: skip says it may not, and the gateway
// goes on to the next as it does past an account that can't be used,
// resting nothing. by names the middleware that said so.
type Accounts interface {
	Account(a Account) (skip bool, by string)
}

// Masker masks what magpie sends to a vendor and puts the values back in
// what comes back. It reads its settings itself; Wanted is the core's
// reading of whether any are asked for.
type Masker interface {
	// JSON is body masked, and how many values it swapped
	JSON(body []byte) ([]byte, int)
	// Text is s masked, and how many values it swapped
	Text(s string) (string, int)
	// Restore is s with the values masked before back
	Restore(s string, escaped bool) string
	// Writer wraps w so what is written has the values back; done writes
	// what it still holds
	Writer(w http.ResponseWriter) (http.ResponseWriter, func())
}

// Hit is one value a masker found, told without the value: its kind,
// the finder that found it (rule, yours, word, model or known), its
// placeholder, what was done with it (Action: "" masked, "warn" sent as
// written, "block" masked and the request to be held back), and as much
// of it as may be kept where it may not (Head, its first character or a
// key's prefix, and Len, how many it has).
type Hit struct {
	Kind, By, Placeholder, Action, Head string
	Len                                 int
}

// Blocks says hits have a value the user asked to hold a request back
// for.
func Blocks(hits []Hit) bool {
	for _, h := range hits {
		if h.Action == "block" {
			return true
		}
	}
	return false
}

// Teller is a Masker that says what it masked too, for the record of
// what was masked; one that doesn't is counted only.
type Teller interface {
	// JSONHits is JSON, with each value it swapped, once for each place
	JSONHits(body []byte) ([]byte, []Hit)
	// TextHits is Text, with each value it swapped
	TextHits(s string) (string, []Hit)
	// Value is the value behind a placeholder, while the masker remembers
	// it
	Value(placeholder string) (string, bool)
}

// ContextTeller is a Teller whose masking can wait (for a local model's
// turn) and stops waiting when ctx ends: the client went away, or the
// gateway gave up on the request. It then fails, as a masker that can't
// mask does, and nothing is sent.
type ContextTeller interface {
	JSONHitsContext(ctx context.Context, body []byte) ([]byte, []Hit)
	TextHitsContext(ctx context.Context, s string) (string, []Hit)
}

var (
	mu      sync.Mutex
	agents  []*Agent
	maskers []*Masker
	refresh func()
)

func add[T any](l *[]*T, v T) (remove func()) {
	e := &v
	mu.Lock()
	*l = append(*l, e)
	mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			mu.Lock()
			defer mu.Unlock()
			*l = slices.DeleteFunc(slices.Clone(*l), func(x *T) bool { return x == e })
		})
	}
}

// Use registers an agent middleware until remove; those registered run in
// that order on the request, and the reply goes back through them the
// other way.
func Use(a Agent) (remove func()) { return add(&agents, a) }

// Agents are the agent middlewares registered now, in their order.
func Agents() []Agent {
	mu.Lock()
	defer mu.Unlock()
	out := make([]Agent, len(agents))
	for i, a := range agents {
		out[i] = *a
	}
	return out
}

// Mask serves m until remove. Registering one while another is served
// stacks: the last serves until it is removed.
func Mask(m Masker) (remove func()) { return add(&maskers, m) }

// Refresh sets what the gateway asks when masking is wanted and no masker
// is served: pluginrt's refresh, which turns the plugin on when the
// settings ask for masking. stop takes it away.
func Refresh(f func()) (stop func()) {
	mu.Lock()
	refresh = f
	mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			mu.Lock()
			refresh = nil
			mu.Unlock()
		})
	}
}

// throttle asks the refresh at most once a second, never from inside one.
type throttle struct {
	busy atomic.Bool
	last atomic.Int64 // when the last ask began, UnixNano
}

// ask runs the refresh when it is due and want (nil: always) says so.
func (t *throttle) ask(f func(), want func() bool) {
	now := time.Now().UnixNano()
	if now-t.last.Load() < int64(time.Second) || !t.busy.CompareAndSwap(false, true) {
		return
	}
	defer t.busy.Store(false)
	t.last.Store(now)
	if want == nil || want() {
		f()
	}
}

var forMasking, forAgents throttle

func refreshFunc() func() {
	mu.Lock()
	defer mu.Unlock()
	return refresh
}

// AgentsWanted is Agents; with none, when want says some are (plugins.json
// has a middleware on), one refresh is asked first, at most once a second:
// a middleware added a moment ago turns its plugin on for the request that
// comes before the gateway's next look at the files.
func AgentsWanted(want func() bool) []Agent {
	if a := Agents(); len(a) > 0 {
		return a
	}
	if f := refreshFunc(); f != nil {
		forAgents.ask(f, want)
	}
	return Agents()
}

func served() (Masker, func()) {
	mu.Lock()
	defer mu.Unlock()
	if n := len(maskers); n > 0 {
		return *maskers[n-1], nil
	}
	return nil, refresh
}

// ForgetRefreshForTest lets the next Served refresh at once, as it would a
// second after the last one.
func ForgetRefreshForTest() {
	forMasking.last.Store(0)
	forAgents.last.Store(0)
}

// ErrUnmasked is the error a request gets when masking is wanted and
// nothing can mask it.
var ErrUnmasked = fmt.Errorf("masking is on in magpie's Settings › Privacy, but the Masking built-in isn't serving it, so nothing was sent; turn it on in Plugins › Built-in")

// Served is the masker that serves now. With none, one refresh is asked
// (at most once a second, never from inside one) and it is looked for
// again: the settings asking for masking turn the plugin on.
func Served() (Masker, bool) {
	m, f := served()
	if m != nil || f == nil {
		return m, m != nil
	}
	forMasking.ask(f, nil)
	m, _ = served()
	return m, m != nil
}

// Masking is the masker for a request: nil, nil when masking isn't wanted;
// ErrUnmasked when it is and none serves. Fail closed.
func Masking(wanted bool) (Masker, error) {
	if !wanted {
		return nil, nil
	}
	if m, ok := Served(); ok {
		return m, nil
	}
	return nil, ErrUnmasked
}

// Safely runs a masker's call: one that panics is an error, not a request
// sent as it is.
func Safely[T any](f func() T) (out T, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("masking failed, so nothing was sent: %v", r)
		}
	}()
	return f(), nil
}
