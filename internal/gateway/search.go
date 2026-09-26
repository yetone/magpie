package gateway

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// A client may offer its model the web search of the vendor it was made
// for: Claude Code's WebSearch, Codex's web_search, a Chat client's
// web_search_options, Gemini's googleSearch. A provider that searches by
// itself is asked to; any other model is given a web_search tool of
// magpie's, which a model that can search — the Claude subscription, a
// Codex account, OpenAI, Anthropic's API, OpenRouter — answers for it.

// searchRounds is how many times a reply may go back to its model with
// what a search found.
const searchRounds = 6

const searchTimeout = 2 * time.Minute

// searchingKey marks a search magpie asks for itself, which must not
// search by way of another model again.
type searchingKey struct{}

func searching(ctx context.Context) bool {
	v, _ := ctx.Value(searchingKey{}).(bool)
	return v
}

// searchesItself says whether the provider searches the web by itself when
// asked on this API.
func searchesItself(p provider.Provider, proto provider.Protocol) bool {
	if p.Account != nil {
		return p.Account.Agent == "codex" && proto == provider.Responses
	}
	return slices.Contains(searchHosts[proto], provider.HostOf(p.Base(proto)))
}

// searchHosts are the APIs that search by themselves: OpenAI's and xAI's
// web_search tool, Anthropic's web_search_20250305, OpenRouter's web
// plugin, Gemini's googleSearch.
var searchHosts = map[provider.Protocol][]string{
	provider.Responses: {"api.openai.com", "api.x.ai"},
	provider.Anthropic: {"api.anthropic.com"},
	provider.Chat:      {"openrouter.ai"},
	provider.Gemini:    {"generativelanguage.googleapis.com"},
}

// searchAsked is whether a body offers the model web search, looked for
// cheaply before it is parsed.
func searchAsked(proto provider.Protocol, body []byte) bool {
	if !strings.Contains(string(body), "web_search") && !strings.Contains(string(body), "oogle_search") && !strings.Contains(string(body), "googleSearch") {
		return false
	}
	req, err := parse(proto, body)
	return err == nil && req.WebSearch
}

// searcher is the model magpie searches with: the first of the providers
// that search by themselves, with a small model of theirs, as searching
// needs no more.
func searcher() (provider.Provider, string, bool) {
	rank := func(p provider.Provider) int {
		switch {
		case p.Account != nil && p.Account.Agent == "claude":
			return 0
		case p.Account != nil && p.Account.Agent == "codex":
			return 1
		case searchesItself(p, provider.Anthropic):
			return 2
		case searchesItself(p, provider.Responses):
			return 3
		case searchesItself(p, provider.Chat):
			return 4
		}
		return -1
	}
	var best provider.Provider
	var model string
	top := -1
	for _, p := range provider.All() {
		r := rank(p)
		if r < 0 || !p.Ready() || (top >= 0 && r >= top) {
			continue
		}
		if m := smallModel(p); m != "" {
			best, model, top = p, m, r
		}
	}
	return best, model, top >= 0
}

// smallModel is the provider's cheapest small model of a vendor that
// searches well, or its cheapest, or its first.
func smallModel(p provider.Provider) string {
	var ms []catalog.Model
	for _, m := range p.Available() {
		l := strings.ToLower(m.ID)
		// a subscription's list can have what magpie wrote into its agent
		if strings.HasPrefix(m.ID, provider.GroupPrefix) || (p.Account != nil && strings.Contains(m.ID, "/")) ||
			strings.ContainsAny(l, ":~") || slices.ContainsFunc([]string{"nano", "lite", "image", "audio", "tts", "embed", "free"}, func(w string) bool { return strings.Contains(l, w) }) {
			continue
		}
		ms = append(ms, m)
	}
	// on a router, a model of the big vendors
	if big := slices.DeleteFunc(slices.Clone(ms), func(m catalog.Model) bool {
		v, _, ok := strings.Cut(m.ID, "/")
		return ok && !slices.Contains([]string{"anthropic", "openai", "google", "x-ai"}, v)
	}); len(big) > 0 {
		ms = big
	}
	cost := func(m catalog.Model) float64 {
		if m.Price == nil {
			return 1e9
		}
		return m.Price.Input + m.Price.Output
	}
	cheapest := func(ms []catalog.Model) string {
		if len(ms) == 0 {
			return ""
		}
		return slices.MinFunc(ms, func(a, b catalog.Model) int { return cmp.Compare(cost(a), cost(b)) }).ID
	}
	small := slices.DeleteFunc(slices.Clone(ms), func(m catalog.Model) bool {
		l := strings.ToLower(m.ID)
		return !strings.Contains(l, "haiku") && !strings.Contains(l, "mini") && !strings.Contains(l, "flash")
	})
	if id := cheapest(small); id != "" {
		return id
	}
	return cheapest(ms)
}

// searchTool is the tool a model that can't search is given, under a name
// the client's own tools leave free.
func searchTool(tools []Tool) Tool {
	name := "web_search"
	for slices.ContainsFunc(tools, func(t Tool) bool { return t.Name == name }) {
		name = "magpie_" + name
	}
	return Tool{Name: name,
		Description: "Search the web for current information: news, releases, documentation, prices, anything after your training or that you are unsure of. Returns what the pages found say, with their URLs. Cite the URLs you use.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"What to search for"}},"required":["query"]}`)}
}

const searchSystem = "You are a web search tool. Search the web for what is asked and report what the results say: the facts that answer it, as specifically as they are given (numbers, dates, versions, names), each with the title and URL of its page. Report only what the pages say; don't answer from memory, don't add advice. Be concise."

// webSearch searches the web with the searcher's model and says what it
// found.
func (s *Server) webSearch(ctx context.Context, query string) (string, error) {
	p, model, ok := searcher()
	if !ok {
		return "", errors.New("no provider that can search the web is set up in magpie")
	}
	ctx, cancel := context.WithTimeout(context.WithValue(ctx, searchingKey{}, true), searchTimeout)
	defer cancel()
	body, _ := json.Marshal(map[string]any{
		"model": p.ID + "/" + model, "max_tokens": 4096, "stream": false, "system": searchSystem,
		"messages": []map[string]any{{"role": "user", "content": "Search the web for: " + query}},
		"tools":    []map[string]any{{"type": "web_search_20250305", "name": "web_search", "max_uses": 3}},
	})
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://magpie/v1/messages", nil)
	if err != nil {
		return "", err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("User-Agent", SearchAgent)
	w := httptest.NewRecorder()
	s.serve(w, r, provider.Anthropic, body)
	var out struct {
		Content []struct {
			Type    string          `json:"type"`
			Text    string          `json:"text"`
			Content json.RawMessage `json:"content"`
		} `json:"content"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code >= 300 || out.Error != nil {
		msg := http.StatusText(w.Code)
		if out.Error != nil && out.Error.Message != "" {
			msg = out.Error.Message
		}
		if ctx.Err() != nil {
			msg = "no answer in " + searchTimeout.String()
		}
		return "", fmt.Errorf("%s/%s: %s", p.ID, model, msg)
	}
	var text strings.Builder
	var sources []string
	for _, b := range out.Content {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "web_search_tool_result":
			var hits []struct {
				Title string `json:"title"`
				URL   string `json:"url"`
			}
			json.Unmarshal(b.Content, &hits)
			for _, h := range hits {
				if h.URL != "" {
					sources = append(sources, "- "+h.Title+" — "+h.URL)
				}
			}
		}
	}
	found := strings.TrimSpace(text.String())
	if found == "" {
		return "", fmt.Errorf("%s/%s found nothing", p.ID, model)
	}
	if len(sources) > 0 && len(sources) <= 20 {
		found += "\n\nSources:\n" + strings.Join(sources, "\n")
	}
	return found, nil
}

// SearchAgent is the User-Agent of the searches magpie makes for a model.
const SearchAgent = "magpie-search/1"

// round asks the model once and gives its reply's events, or the status
// and message of a failure.
type round func(ctx context.Context, req *Request) (<-chan Event, int, string)

// searchReply answers req through ask, running the searches its model
// asks for and asking it again with what they found; the client sees one
// reply. The first round's failure is a status, as another provider may
// take over.
func (s *Server) searchReply(w http.ResponseWriter, r *http.Request, from provider.Protocol, name string, req *Request, usage *Usage, ask round) (int, string) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	q := *req
	q.WebSearch = false
	tool := searchTool(q.Tools)
	q.Tools = append(slices.Clone(q.Tools), tool)
	q.Messages = slices.Clone(q.Messages)
	first, status, msg := ask(ctx, &q)
	if first == nil {
		return writeError(w, from, status, msg), msg
	}
	events := make(chan Event, 16)
	go func() {
		defer close(events)
		s.searchRounds(ctx, &q, tool.Name, first, ask, events)
	}()
	return relay(w, r, from, name, req, events, usage, cancel, func(string, string, bool) {})
}

func (s *Server) searchRounds(ctx context.Context, q *Request, tool string, in <-chan Event, ask round, out chan<- Event) {
	send := func(ev Event) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
	var before, this Usage
	started, said := false, false
	for n := 1; ; n++ {
		var col collector
		mine, theirs, gap := false, false, said
		var stop string
		for ev := range in {
			col.add(ev)
			switch ev.Kind {
			case KStart:
				this = ev.Usage
				ev.Usage = ev.Usage.plus(before, false)
				if started {
					ev = Event{Kind: KUsage, Usage: ev.Usage}
				}
				started = true
			case KUsage:
				this.add(ev.Usage)
				ev.Usage = ev.Usage.plus(before, true)
			case KText:
				if gap && ev.Text != "" {
					// what it says after a search goes on from what it
					// said before it
					ev.Text = "\n\n" + strings.TrimLeft(ev.Text, "\n")
					gap = false
				}
				said = said || ev.Text != ""
			case KToolStart:
				mine = ev.Name == tool
				theirs = theirs || !mine
				if mine {
					continue
				}
			case KToolArgs:
				if mine {
					continue
				}
			case KStop:
				stop = ev.Stop
				continue
			case KError:
				send(ev)
				return
			}
			if !send(ev) {
				return
			}
		}
		res := col.finish()
		var calls []Part
		for i, p := range res.Parts {
			if p.Kind == ToolCall && p.Name == tool {
				if p.ID == "" {
					res.Parts[i].ID = fmt.Sprintf("call_search_%d_%d", n, i)
				}
				calls = append(calls, res.Parts[i])
			}
		}
		// a reply that calls the client's tools goes to the client: it
		// can't answer a search, and doesn't see the ones left out
		if len(calls) == 0 || theirs || n > searchRounds+1 {
			if stop == "" || (len(calls) > 0 && !theirs) {
				stop = "stop"
			}
			send(Event{Kind: KStop, Stop: stop})
			return
		}
		results := make([]Part, len(calls))
		var wg sync.WaitGroup
		for i, c := range calls {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = Part{Kind: ToolResult, CallID: c.ID}
				if n > searchRounds {
					results[i].Text, results[i].IsError = "No more searches: answer with what was found.", true
					return
				}
				var a struct {
					Query string `json:"query"`
				}
				json.Unmarshal(c.Args, &a)
				if strings.TrimSpace(a.Query) == "" {
					results[i].Text, results[i].IsError = "query is empty", true
					return
				}
				found, err := s.webSearch(ctx, a.Query)
				if err != nil {
					results[i].Text, results[i].IsError = "search failed: "+err.Error(), true
					return
				}
				results[i].Text = found
			}()
		}
		wg.Wait()
		if ctx.Err() != nil {
			return
		}
		q.Messages = append(q.Messages, Message{Role: "assistant", Parts: res.Parts}, Message{Role: "user", Parts: results})
		before, this = before.plus(this, false), Usage{}
		var status int
		var msg string
		if in, status, msg = ask(ctx, q); in == nil {
			send(Event{Kind: KError, Text: fmt.Sprintf("%s (%d)", msg, status)})
			return
		}
	}
}

// askTranslated is a round for a provider asked on to.
func (s *Server) askTranslated(p provider.Provider, to provider.Protocol, model string, in http.Header, reply http.Header) round {
	first := true
	return func(ctx context.Context, req *Request) (<-chan Event, int, string) {
		q := *req
		q.Stream = true
		res, actual, err := s.forwardTranslated(ctx, p, to, &q, model, in)
		if err != nil {
			return nil, 502, p.Name + ": " + err.Error()
		}
		if res.StatusCode >= 400 {
			b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			res.Body.Close()
			if first {
				keepRetry(reply, res.Header, b)
			}
			return nil, res.StatusCode, p.Name + ": " + provider.APIError(b, res.Status)
		}
		first = false
		to = actual
		rd, sse := eventStream(res)
		if !sse {
			b, _ := io.ReadAll(io.LimitReader(rd, 1<<20))
			res.Body.Close()
			return nil, 502, p.Name + " did not stream: " + provider.APIError(b, "unexpected reply")
		}
		events := make(chan Event, 16)
		go func() {
			defer close(events)
			defer res.Body.Close()
			dec := decoder(actual)
			send := func(ev Event) error {
				select {
				case events <- ev:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			var failed error
			err := readSSE(rd, func(_, data string) error {
				if err := dec(data, func(ev Event) {
					if failed == nil {
						failed = send(ev)
					}
				}); err != nil {
					return err
				}
				return failed
			})
			if err != nil && failed == nil && ctx.Err() == nil {
				send(Event{Kind: KError, Text: p.Name + ": " + err.Error()})
			}
		}()
		return events, 0, ""
	}
}
