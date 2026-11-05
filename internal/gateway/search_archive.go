package gateway

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// A search magpie runs with a search API or a plan's own search service
// is a request of magpie's own, as a model's search is (searchWith): the
// Routing page lists it, naming the API or service and the key or account
// it went with, Recent calls keeps its request and response, and with the
// request archive on they are kept as a client's are, its usage row naming
// them (axelburks on Discord: 搜索API代搜时 路由页面不显示是请求了哪个API…
// 用量-请求 页面 能看到请求记录 但是无请求存档). Every ask that writes a usage
// row (logSearch) has its exchange in a searchWire, put in its context
// before it is sent, which the place that sends it fills (tapSearch).

type searchWireKey struct{}

// searchWire is one ask's exchange as it went: the request with the
// headers it was signed with and its body, and the answer's status,
// headers and body, as much of it as was read. secret is the key it was
// asked with, which is never kept: it is masked out of all of it.
type searchWire struct {
	mu     sync.Mutex
	sent   *http.Request
	body   []byte
	status int
	head   http.Header
	answer []byte
	cut    bool // more of the answer came than was kept
	limit  int
	secret string
}

// withSearchWire is ctx for one ask of a search, with key the key it is
// asked with ("" for none or an account's sign-in, which goes in headers
// alone). A body is kept up to the archive's size (RequestArchiveMaxMB),
// and at least what Recent calls keeps.
func withSearchWire(ctx context.Context, key string) (context.Context, *searchWire) {
	w := &searchWire{limit: int(max(archiveLimit(), callBodyLimit)) + 1, secret: key}
	return context.WithValue(ctx, searchWireKey{}, w), w
}

func searchWireOf(ctx context.Context) *searchWire {
	w, _ := ctx.Value(searchWireKey{}).(*searchWire)
	return w
}

// tapSearch notes r, sent with body, and res, its answer (nil when none
// came), as ctx's ask's exchange, the last one it sent standing (GLM's
// MCP search sends three: its tools/call is the search); res's body is
// kept as it is read. Without a wire in ctx it does nothing.
func tapSearch(ctx context.Context, r *http.Request, body []byte, res *http.Response) *http.Response {
	w := searchWireOf(ctx)
	if w == nil || r == nil {
		return res
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sent, w.body = r, body[:min(len(body), w.limit)]
	w.status, w.head, w.answer, w.cut = 0, nil, nil, false
	if res != nil {
		w.status, w.head = res.StatusCode, res.Header.Clone()
		res.Body = &searchTee{ReadCloser: res.Body, w: w}
	}
	return res
}

// searchTee keeps what is read of a search's answer in its wire.
type searchTee struct {
	io.ReadCloser
	w *searchWire
}

func (t *searchTee) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	if n > 0 {
		w := t.w
		w.mu.Lock()
		room := w.limit - len(w.answer)
		w.answer = append(w.answer, p[:min(n, max(room, 0))]...)
		w.cut = w.cut || n > room
		w.mu.Unlock()
	}
	return n, err
}

// notePluginSearch notes, as ctx's ask's exchange, an ask of a plugin's
// search API, which the plugin host sends: what magpie handed the plugin
// (asked, pluginSearchAsked) and what it gave back, the pages it found or
// the error it threw, with the status of a refusal. The request the
// plugin itself made goes from the host, out of magpie's sight.
func notePluginSearch(ctx context.Context, spec string, asked map[string]any, got []plugin.SearchPage, err error) {
	w := searchWireOf(ctx)
	if w == nil {
		return
	}
	status, answer := http.StatusOK, any(map[string]any{"results": got})
	if err != nil {
		status = 0
		var r plugin.SearchRefused
		if errors.As(err, &r) {
			status = r.Status
		}
		answer = map[string]any{"error": err.Error(), "status": status}
	}
	body, _ := json.Marshal(asked)
	b, _ := json.Marshal(answer)
	r := &http.Request{Method: "PLUGIN", URL: &url.URL{Path: spec}, Header: http.Header{}}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sent, w.body, w.status, w.head, w.answer, w.cut = r, body, status, http.Header{"Content-Type": {"application/json"}}, b, false
}

// pluginSearchAsked is what magpie hands a plugin's search hook, as its
// archive says it: the provider, the query, how many pages, the address
// set (when one is) and the key masked or the account it signs in as.
func pluginSearchAsked(v provider.SearchVendor, query string, count int, at, key, account string) map[string]any {
	m := map[string]any{"hook": "search", "provider": v.PluginID, "query": query, "count": count}
	if at != "" {
		m["url"] = at
	}
	if key != "" {
		m["key"] = provider.MaskSearchKey(key)
	}
	if account != "" {
		m["account"] = account
	}
	return m
}

// taken is the exchange with the key masked out of everything: the path
// and query, every header (Authorization and the like are scrubbed on
// the way to the archive anyway), and both bodies. ok is false when
// nothing was sent.
func (w *searchWire) taken() (sent *http.Request, body []byte, status int, head http.Header, answer []byte, cut bool, ok bool) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sent == nil {
		return
	}
	mask := func(b []byte) []byte { return b }
	if k := w.secret; len(k) >= 4 {
		hidden := provider.MaskSearchKey(k)
		mask = func(b []byte) []byte { return []byte(strings.ReplaceAll(string(b), k, hidden)) }
	}
	u := *w.sent.URL
	u.Path, u.RawPath = string(mask([]byte(u.Path))), ""
	u.RawQuery = string(mask([]byte(u.RawQuery)))
	sent = &http.Request{Method: w.sent.Method, URL: &u, Header: http.Header{}}
	for k, vs := range w.sent.Header {
		for _, v := range vs {
			sent.Header.Add(k, string(mask([]byte(v))))
		}
	}
	head = http.Header{}
	for k, vs := range w.head {
		for _, v := range vs {
			head.Add(k, string(mask([]byte(v))))
		}
	}
	return sent, mask(w.body), w.status, head, mask(w.answer), w.cut, true
}

// keepSearch puts a search's ask, rec its usage row as logSearch made
// it, on the Routing page and in Recent calls, and with the request
// archive on keeps its exchange there, and gives the row the route and
// the archive's name.
func (s *Server) keepSearch(ctx context.Context, rec *usage.Record, start time.Time) {
	if s == nil {
		return
	}
	// who it went to, as the Routing page names a key or an account: the
	// API or service, and the key, the account or, with neither, where it
	// was asked
	seat := Weighed{ID: cmp.Or(rec.ProviderKeyID, rec.Provider, rec.Requested), Provider: cmp.Or(rec.Provider, rec.Requested),
		Name: rec.Requested, Who: cmp.Or(rec.ProviderKeyName, rec.ProviderAccount, rec.Host), Kind: "key", Model: rec.Requested}
	if rec.ProviderAccount != "" {
		seat.Kind = "account"
	}
	where := cmp.Or(rec.Provider, rec.Host, rec.Requested)
	err := rec.Error
	// magpie asked, as for a model's search; whose search it was is For
	rt := Route{Time: start, Agent: "magpie", Kind: "web_search", For: searchFor(ctx),
		Model: rec.Requested, Provider: where, Order: []Weighed{seat},
		Tries: []Try{{ID: seat.ID, Model: seat.Model, Start: start, Done: true, Status: rec.Status, Millis: rec.Millis, Error: err}}}
	rt.CallerKey, rt.CallerKeyName = callerKeyOf(ctx)
	tr := s.trace.begin(rt)
	rec.RouteID = tr.ID
	c := Call{Route: tr.ID, Time: start, Agent: rt.Agent, Kind: "web_search", For: rt.For, Model: rec.Requested,
		Provider: where, Status: rec.Status, Millis: rec.Millis, Error: err}
	if sent, body, status, head, answer, cut, ok := searchWireOf(ctx).taken(); ok {
		if c.Status == 0 {
			c.Status = status
		}
		rec.Archive = archiveOwn(c, sent, body, status, head, answer)
		c.Archive = rec.Archive
		c.RequestBody, c.RequestTruncated = captureRequestBody(body)
		c.ResponseBody, c.ResponseTruncated = captureRequestBody(answer)
		c.ResponseTruncated = c.ResponseTruncated || cut
		s.record(c)
	}
	s.trace.update(tr, func(r *Route) {
		r.Done, r.Status, r.Error, r.Millis, r.Archive = true, rec.Status, err, rec.Millis, rec.Archive
	})
}
