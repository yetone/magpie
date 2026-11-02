package gateway

// A call's headers, both ways (usrname1833 on Discord: 查看原始请求和原始响应的
// http头, for debugging what a capture would show): the client's request
// as it reached magpie, and each request magpie sent upstream for it —
// its method and address, the headers it went with, and the status,
// protocol and headers that came back. Recent calls and the Routing page
// open them beside the bodies, and the request archive keeps them.
//
// They cost a request one map copy each way, at most headerTriesMost of
// them: nothing is masked, sorted or written as it goes. The secrets are
// taken out only when they are read (CallHeaders), by what each header is
// (redact.ScrubHeader): Authorization, x-api-key, cookies, any *-Token,
// *-Secret, *-Key a credential is in, and any value that is a Bearer or a
// key. Nothing is kept past the ring's 40 calls but in the archive, which
// keeps them masked, as it keeps everything.

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/redact"
)

// headerTriesMost is how many upstream requests a call keeps the headers
// of: the first ones, then a count of the rest.
const headerTriesMost = 8

// headerLog is one call's headers, as they went: the client's, and each
// upstream request's.
type headerLog struct {
	mu    sync.Mutex
	in    http.Header
	tries []headerTry
	more  int
}

type headerTry struct {
	method string
	url    *url.URL
	sent   http.Header
	plugin string // the plugin the request was handed to, "" for a vendor's address
	start  time.Time
	took   time.Duration
	status int
	proto  string
	got    http.Header
	err    string
}

type headerLogKey struct{}

// headerLogged gives each request a client makes of the gateway a log of
// its headers, the client's taken now, before a guard takes a key off
// them. A GET (a model list, a page) has none: it is no call.
func headerLogged(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			h.ServeHTTP(w, r)
			return
		}
		if _, ok := r.Context().Value(headerLogKey{}).(*headerLog); ok {
			h.ServeHTTP(w, r) // a socket's turn, or one handed on inside
			return
		}
		l := &headerLog{in: r.Header.Clone()}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), headerLogKey{}, l)))
	})
}

// headerLogOf is the log of the call ctx is for, nil when it keeps none.
func headerLogOf(ctx context.Context) *headerLog {
	l, _ := ctx.Value(headerLogKey{}).(*headerLog)
	return l
}

// upstreamBegins notes req, about to go out for the call its context is
// for, and is to be told what came back for it. plugin names the plugin
// it is handed to, "" for one sent to its address. Its headers are taken
// now, as they go, before a transport under it can add to them.
func upstreamBegins(req *http.Request, plugin string) func(*http.Response, error) {
	l := headerLogOf(req.Context())
	if l == nil {
		return func(*http.Response, error) {}
	}
	t := headerTry{method: req.Method, url: req.URL, sent: req.Header.Clone(), plugin: plugin, start: time.Now()}
	if t.sent == nil {
		t.sent = http.Header{}
	}
	if req.Host != "" && req.URL != nil && req.Host != req.URL.Host {
		t.sent.Set("Host", req.Host)
	}
	return func(res *http.Response, err error) {
		t.took = time.Since(t.start)
		if err != nil {
			t.err = err.Error()
		} else if res != nil {
			t.status, t.proto, t.got = res.StatusCode, res.Proto, res.Header.Clone()
		}
		l.mu.Lock()
		defer l.mu.Unlock()
		if len(l.tries) >= headerTriesMost {
			l.more++
			return
		}
		l.tries = append(l.tries, t)
	}
}

func init() {
	// a plugin's account sends through the plugin, past the gateway's
	// transport (provider.Provider.Do)
	provider.SendsThroughPlugin = func(id string, req *http.Request) func(*http.Response, error) {
		return upstreamBegins(req, id)
	}
}

// CallHeaders is a call's headers both ways, every secret taken out.
type CallHeaders struct {
	// Request: the client's, as it reached magpie
	Request map[string][]string `json:"request"`
	// Upstream: each request magpie sent for it, in order
	Upstream []UpstreamHeaders `json:"upstream"`
	// More: the upstream requests past the first headerTriesMost, not kept
	More int `json:"more,omitempty"`
}

// UpstreamHeaders is one request magpie sent upstream, and its answer's
// head.
type UpstreamHeaders struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	// Plugin: the plugin it was handed to, which sends it on to its
	// vendor itself, with headers of its own magpie doesn't see
	Plugin   string              `json:"plugin,omitempty"`
	Request  map[string][]string `json:"request"`
	Millis   int64               `json:"ms"`
	Status   int                 `json:"status,omitempty"`
	Proto    string              `json:"proto,omitempty"`
	Response map[string][]string `json:"response,omitempty"`
	Error    string              `json:"error,omitempty"`
}

// Headers is c's headers both ways, masked, nil when none were kept (a
// call from before this, or one magpie made for itself).
func (c Call) Headers() *CallHeaders {
	return c.headers.masked()
}

func (l *headerLog) masked() *CallHeaders {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	tries := append([]headerTry(nil), l.tries...)
	more := l.more
	l.mu.Unlock()
	// kept as each was answered: shown as each went
	slices.SortStableFunc(tries, func(a, b headerTry) int { return a.start.Compare(b.start) })
	out := &CallHeaders{Request: scrubHeaders(l.in), Upstream: make([]UpstreamHeaders, 0, len(tries)), More: more}
	for _, t := range tries {
		u := UpstreamHeaders{Method: t.method, URL: scrubURL(t.url), Plugin: t.plugin, Request: scrubHeaders(t.sent),
			Millis: t.took.Milliseconds(), Status: t.status, Proto: t.proto, Error: redact.Scrub(t.err)}
		if t.got != nil {
			u.Response = scrubHeaders(t.got)
		}
		out.Upstream = append(out.Upstream, u)
	}
	return out
}

// scrubURL is an address with what may be a secret in it taken out: a
// password, a key= in its query, a key in its path.
func scrubURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	c := *u
	c.User = nil
	c.Path, c.RawPath = redact.Scrub(c.Path), ""
	if c.RawQuery != "" {
		c.RawQuery = scrubQuery(c.RawQuery)
	}
	c.Fragment, c.RawFragment = "", ""
	return c.String()
}
