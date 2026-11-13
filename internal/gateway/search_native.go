package gateway

import (
	"cmp"
	"context"
	"net/http"
	"time"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// searchTally counts the web searches a provider's reply says it ran by
// itself: Anthropic's server_tool_use blocks named web_search (DeepSeek's
// Anthropic API answers with them too, #669) and its usage's
// server_tool_use.web_search_requests, and the Responses API's
// web_search_call items. A model searching with its own provider's tool
// was in no row of the request log (cybercaius on Discord: DeepSeek
// searched and Usage showed nothing of it). A stream names each search
// once as it goes (a block's start, an item done) and may list them all
// again at its end, so what it streamed, what it listed whole and what
// its usage said are counted apart, and the most of them is the count.
type searchTally struct {
	ids    map[string]bool
	anon   int // streamed blocks or items with no id
	listed int // in a whole reply's content or output
	said   int // what the reply's usage counted itself
}

// see reads one event of a reply, or a whole reply, of proto. Only one
// that names a search is parsed: every event of every relayed reply goes
// by here.
func (t *searchTally) see(proto provider.Protocol, b []byte) {
	switch proto {
	case provider.Anthropic:
		if !bodyHas(b, "server_tool_use") {
			return
		}
		v := gjson.ParseBytes(b)
		if c := v.Get("content_block"); isSearchBlock(c) {
			t.streamed(c.Get("id").String())
		}
		t.listed = max(t.listed, countOf(v.Get("content"), isSearchBlock))
		for _, u := range []gjson.Result{v.Get("usage"), v.Get("message.usage")} {
			t.said = max(t.said, int(u.Get("server_tool_use.web_search_requests").Int()))
		}
	case provider.Responses:
		if !bodyHas(b, "web_search_call") {
			return
		}
		v := gjson.ParseBytes(b)
		if it := v.Get("item"); v.Get("type").String() == "response.output_item.done" && isSearchItem(it) {
			t.streamed(it.Get("id").String())
		}
		t.listed = max(t.listed, countOf(v.Get("output"), isSearchItem), countOf(v.Get("response.output"), isSearchItem))
	}
}

func isSearchBlock(c gjson.Result) bool {
	return c.Get("type").String() == "server_tool_use" && c.Get("name").String() == "web_search"
}

func isSearchItem(it gjson.Result) bool { return it.Get("type").String() == "web_search_call" }

func countOf(list gjson.Result, is func(gjson.Result) bool) int {
	n := 0
	for _, x := range list.Array() {
		if is(x) {
			n++
		}
	}
	return n
}

func (t *searchTally) streamed(id string) {
	if id == "" {
		t.anon++
		return
	}
	if t.ids == nil {
		t.ids = map[string]bool{}
	}
	t.ids[id] = true
}

// count is how many searches the reply ran.
func (t *searchTally) count() int {
	return max(len(t.ids)+t.anon, t.listed, t.said)
}

// maxNativeSearchRows bounds the rows one reply writes: a reply that says
// it searched a thousand times is not a thousand rows.
const maxNativeSearchRows = 20

// logNativeSearches writes a row of the request log for each web search
// the provider's model ran with its own provider's tool, beside the
// request's own row: the Web search purpose, under the provider, its
// model, and how it ended (the reply came, so 200). The query is never
// kept, as no search row keeps one, and the row is counted nowhere: its
// tokens are the request's.
func logNativeSearches(ctx context.Context, n int, at time.Time, agent, providerID, host, model string) {
	who := access.Caller(ctx)
	for range min(n, maxNativeSearchRows) {
		budget.Append(usage.Record{Time: at, Agent: cmp.Or(agent, "magpie"), Kind: "web_search", Source: usage.SourceSearch,
			Provider: providerID, Host: host, Requested: model, Model: model, Status: http.StatusOK,
			CallerKeyID: who.KeyID, CallerKeyName: who.KeyName, CallerIssuerID: who.IssuerID()})
	}
}
