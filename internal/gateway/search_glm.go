package gateway

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A GLM Coding Plan, Zhipu's or Z.ai's, searches the web with its own Web
// Search MCP server, which the plan's key is given for (young02036 on
// Discord): the Streamable HTTP server at the plan's host's
// provider.ZhipuSearchPath, the key as a Bearer, its tool webSearchPrime
// asked with {"search_query": "…"} (docs.bigmodel.cn/cn/coding-plan/mcp/search-mcp-server,
// docs.z.ai/devpack/mcp/search-mcp-server). magpie opens a session
// (initialize, notifications/initialized), calls the tool once and ends
// the session. The tool answers in a text block; the docs say only that
// each result has a title, address, summary, site name and icon, so the
// text is read as Zhipu's Web Search API gives its results
// ({title, link, content, media, icon, refer, publish_date}), as a list, a
// list in a JSON string, or under search_result/results/items/data.
//
// As with a Kimi Code plan, a model of the plan has its searches done
// there first, so a GLM user's searches spend the plan's search allowance
// rather than another subscription's, and a plan with nothing else set up
// in magpie searches at all; but only while Settings names no searcher:
// one the user named still searches for every model. For another
// provider's models the plan searches only when Settings names it. A
// provider with several keys asks each of its plan's keys in turn, on to
// the next when one fails.

// glmSearchTool is the plan's search tool, and glmSearchProtocol the MCP
// version magpie asks for.
const (
	glmSearchTool     = "webSearchPrime"
	glmSearchProtocol = "2025-03-26"
)

// glmSearches is whether p is a GLM Coding Plan that searches for its own
// models first: one with a plan's key, while Settings names no searcher.
func glmSearches(p provider.Provider) bool {
	return provider.ZhipuSearches(p) && strings.TrimSpace(settings.Load().Searcher) == ""
}

// glmSearch asks p's GLM Coding Plan's search MCP server, with each of its
// plan's keys in turn until one answers, each ask a row of the request log.
func (s *Server) glmSearch(ctx context.Context, p provider.Provider, query string) (string, []Hit, error) {
	keys := provider.ZhipuSearchKeys(p)
	if len(keys) == 0 {
		return "", nil, fmt.Errorf("%s: no GLM Coding Plan key to search with", p.Name)
	}
	var errs []error
	for _, k := range keys {
		ctx, _ := withSearchWire(ctx, k.Key)
		start := time.Now()
		pages, err := s.glmSearchKey(ctx, k, query)
		s.logOwnSearch(ctx, k, provider.ZhipuCodingSearch(k), query, start, len(pages), err)
		if err == nil {
			said, hits := pagesSaid(pages)
			return said, hits, nil
		}
		if len(keys) > 1 {
			err = fmt.Errorf("%s: %w", provider.Mask(k.Key), err)
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return "", nil, fmt.Errorf("%s's web search: %w", p.Name, errors.Join(errs...))
}

// glmSearchKey asks the MCP server with p's key.
func (s *Server) glmSearchKey(ctx context.Context, p provider.Provider, query string) ([]foundPage, error) {
	at := provider.ZhipuCodingSearch(p)
	ctx, cancel := context.WithTimeout(s.metered(p.Via(ctx), p, ""), searchTimeout/2)
	defer cancel()
	q, err := maskedQuery(ctx, query, &p)
	if err != nil {
		return nil, err
	}
	c := glmMCP{s: s, at: at, key: p.Key}
	if _, err := c.call(ctx, 1, "initialize", map[string]any{"protocolVersion": glmSearchProtocol, "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "magpie-search", "version": "1"}}); err != nil {
		return nil, c.said(ctx, err)
	}
	defer c.end()
	c.note(ctx, "notifications/initialized")
	res, err := c.call(ctx, 2, "tools/call", map[string]any{"name": glmSearchTool, "arguments": map[string]any{"search_query": q}})
	if err != nil {
		return nil, c.said(ctx, err)
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, fmt.Errorf("unexpected reply: %s", clipText(string(res), 300))
	}
	var texts []string
	for _, b := range out.Content {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			texts = append(texts, b.Text)
		}
	}
	if out.IsError {
		return nil, errors.New(c.masked(clipText(cmp.Or(strings.Join(texts, " "), "the search tool failed"), 500)))
	}
	var pages []foundPage
	n := searchCount(ctx)
	for _, t := range texts {
		pages = append(pages, readGLMSearch(t)...)
		if len(pages) >= n {
			pages = pages[:n]
			break
		}
	}
	if len(pages) == 0 {
		if len(texts) > 0 && !strings.HasPrefix(strings.TrimSpace(texts[0]), "[") && !strings.HasPrefix(strings.TrimSpace(texts[0]), "{") &&
			!strings.HasPrefix(strings.TrimSpace(texts[0]), `"`) {
			// words, not results: what the tool said instead (a spent
			// allowance, a refusal)
			return nil, errors.New(c.masked(clipText(texts[0], 500)))
		}
		return nil, errFoundNothing
	}
	return pages, nil
}

// readGLMSearch reads the pages out of the search tool's text (see the
// top of this file); text that is no such JSON gives none.
func readGLMSearch(text string) []foundPage {
	var v any = text
	// a JSON string holding the JSON, as the tool may give it
	for range 3 {
		s, ok := v.(string)
		if !ok {
			break
		}
		if json.Unmarshal([]byte(s), &v) != nil {
			return nil
		}
	}
	if o, ok := v.(map[string]any); ok {
		v = nil
		for _, k := range []string{"search_result", "results", "items", "data"} {
			if a, ok := o[k].([]any); ok {
				v = a
				break
			}
		}
	}
	list, _ := v.([]any)
	str := func(o map[string]any, keys ...string) string {
		for _, k := range keys {
			if s, ok := o[k].(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
		return ""
	}
	var pages []foundPage
	for _, x := range list {
		o, ok := x.(map[string]any)
		if !ok {
			continue
		}
		u := str(o, "link", "url", "href")
		if u == "" {
			continue
		}
		title := cmp.Or(searchPlain(str(o, "title", "name")), searchPlain(str(o, "media", "website", "site_name")), u)
		text := str(o, "content", "summary", "snippet", "description")
		pages = append(pages, foundPage{Title: title, URL: u, Text: clipText(searchPlain(text), pageText), Age: str(o, "publish_date", "date")})
	}
	return pages
}

// glmMCP is one session with a plan's search MCP server.
type glmMCP struct {
	s                *Server
	at, key, session string
}

// glmMCPError is an MCP server's refusal: its HTTP status, or the
// JSON-RPC error it answered.
type glmMCPError struct {
	status int
	msg    string
}

func (e glmMCPError) Error() string { return e.msg }

func (c *glmMCP) post(ctx context.Context, body []byte) (*http.Response, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.at, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("User-Agent", SearchAgent)
	r.Header.Set("Authorization", "Bearer "+c.key)
	if c.session != "" {
		r.Header.Set("Mcp-Session-Id", c.session)
		r.Header.Set("MCP-Protocol-Version", glmSearchProtocol)
	}
	res, err := c.s.client.Do(r)
	return tapSearch(ctx, r, body, res), err
}

// call asks the server method and gives its result.
func (c *glmMCP) call(ctx context.Context, id int, method string, params any) (json.RawMessage, error) {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	res, err := c.post(ctx, body)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if sid := res.Header.Get("Mcp-Session-Id"); sid != "" {
		c.session = sid
	}
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		return nil, glmMCPError{res.StatusCode, c.masked(provider.APIError(b, res.Status))}
	}
	m, err := glmReply(res, id)
	if err != nil {
		return nil, err
	}
	if len(m.Error) > 0 && string(m.Error) != "null" {
		var e struct {
			Message string `json:"message"`
		}
		json.Unmarshal(m.Error, &e)
		return nil, glmMCPError{0, c.masked(cmp.Or(e.Message, string(m.Error)))}
	}
	return m.Result, nil
}

// note sends a notification, whose answer is no matter.
func (c *glmMCP) note(ctx context.Context, method string) {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	if res, err := c.post(ctx, body); err == nil {
		io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		res.Body.Close()
	}
}

// end tells the server the session is done with, when it gave one.
func (c *glmMCP) end() {
	if c.session == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.at, nil)
	if err != nil {
		return
	}
	r.Header.Set("Authorization", "Bearer "+c.key)
	r.Header.Set("Mcp-Session-Id", c.session)
	r.Header.Set("User-Agent", SearchAgent)
	if res, err := c.s.client.Do(r); err == nil {
		res.Body.Close()
	}
}

// masked is msg with the key in it masked.
func (c *glmMCP) masked(msg string) string {
	return strings.ReplaceAll(msg, c.key, provider.Mask(c.key))
}

// said is err as a search's error says it: "no answer in time" for a
// deadline, the key masked.
func (c *glmMCP) said(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errors.New("no answer in time")
	}
	var e glmMCPError
	if errors.As(err, &e) {
		return e
	}
	return errors.New(c.masked(err.Error()))
}

// glmReply is the reply to id in a response: its JSON body, or the event
// stream's message that carries it.
func glmReply(res *http.Response, id int) (*mcpMessage, error) {
	want := fmt.Sprint(id)
	body := io.LimitReader(res.Body, 8<<20)
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		b, err := io.ReadAll(body)
		if err != nil {
			return nil, err
		}
		var m mcpMessage
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("not an MCP reply: %s", clipText(string(b), 300))
		}
		return &m, nil
	}
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	var data []string
	flush := func() *mcpMessage {
		defer func() { data = nil }()
		var m mcpMessage
		if len(data) > 0 && json.Unmarshal([]byte(strings.Join(data, "\n")), &m) == nil && string(m.ID) == want {
			return &m
		}
		return nil
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if m := flush(); m != nil {
				return m, nil
			}
			continue
		}
		if d, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(d, " "))
		}
	}
	if m := flush(); m != nil {
		return m, nil
	}
	return nil, errors.New("the event stream ended without a reply")
}
