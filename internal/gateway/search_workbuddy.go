package gateway

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// A WorkBuddy plan, the China build's or WorkBuddy AI's, searches the web
// with its own search service, as WorkBuddy's own client does (its CLI's
// WebSearch tool, app.asar.unpacked/cli/dist/codebuddy.js: WEB_SEARCH_API_PATH):
// a POST to <site>/agenttool/v1/search of
//
//	{"query":"…","type":"text2text","max_results":5}
//
// signed as the plan's chats are, answered with
//
//	{"query","type","provider","results":[{"title","url","snippet","site","favicon"}],"total_results","response_time_ms"}
//
// or a refusal as {"code","msg"} (15001 is its search quota spent). Its
// models have no search tool of their own, so before, a Claude Code turn
// on WorkBuddy with nothing else in magpie that searches had the server
// tool taken away and WebSearch came back empty (#1798). As with a Kimi
// Code plan, a WorkBuddy model's searches are done here first, and only
// for its own models. It is the same for the built-in and for a WorkBuddy
// moved onto its plugin: magpie shapes the request, Sign and Do send it
// (the plugin's fetch signs it there).

// wbSearchPath is where a WorkBuddy site's search service is, and
// wbSearchResults how many results its client asks for.
const (
	wbSearchPath    = "/agenttool/v1/search"
	wbSearchResults = 5
)

// wbSearches is whether p is a WorkBuddy plan that searches for its models.
func wbSearches(p provider.Provider) bool { return drawsWorkBuddy(p) }

// wbSearch asks p's WorkBuddy site to search the web.
func (s *Server) wbSearch(ctx context.Context, p provider.Provider, query string) (string, []Hit, error) {
	origin := p.Origin(ctx)
	if origin == "" {
		return "", nil, fmt.Errorf("%s: no site to search with", p.Name)
	}
	// its client gives the service 20 s
	ctx, cancel := context.WithTimeout(s.metered(p.Via(ctx), p, ""), searchTimeout/2)
	defer cancel()
	q, err := maskedQuery(ctx, query, &p)
	if err != nil {
		return "", nil, err
	}
	body, _ := json.Marshal(map[string]any{"query": q, "type": "text2text", "max_results": wbSearchResults})
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+wbSearchPath, bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	if err := p.Sign(ctx, r, provider.Chat, body); err != nil {
		return "", nil, fmt.Errorf("%s: %w", p.Name, err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	r.Header.Set("X-Requested-With", "XMLHttpRequest")
	if r.Header.Get("X-Request-ID") == "" {
		id := wbSearchID()
		r.Header.Set("X-Request-ID", id)
		r.Header.Set("X-Conversation-Message-ID", id)
	}
	res, err := p.Do(s.client, r) // a moved one's through its plugin
	res = tapSearch(ctx, r, body, res)
	if err != nil {
		if ctx.Err() != nil {
			return "", nil, fmt.Errorf("%s: no answer in time", p.Name)
		}
		return "", nil, fmt.Errorf("%s: %w", p.Name, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode >= 300 {
		return "", nil, errors.New(p.Name + "'s search: " + provider.APIError(b, res.Status))
	}
	var out struct {
		Code    json.RawMessage `json:"code"`
		Msg     string          `json:"msg"`
		Results *[]struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Snippet string `json:"snippet"`
			Site    string `json:"site"`
		} `json:"results"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", nil, fmt.Errorf("%s's search: unexpected reply: %s", p.Name, provider.APIError(b, res.Status))
	}
	if c := strings.Trim(string(out.Code), `"`); c != "" && c != "0" && c != "null" {
		return "", nil, fmt.Errorf("%s's search: %s (%s)", p.Name, cmp.Or(out.Msg, "refused"), c)
	}
	if out.Results == nil {
		return "", nil, fmt.Errorf("%s's search: unexpected reply: %s", p.Name, provider.APIError(b, res.Status))
	}
	var pages []foundPage
	for _, x := range *out.Results {
		if x.URL == "" {
			continue
		}
		title := cmp.Or(searchPlain(x.Title), searchPlain(x.Site), x.URL)
		pages = append(pages, foundPage{Title: title, URL: x.URL, Text: clipText(searchPlain(x.Snippet), pageText)})
		if len(pages) == searchHits {
			break
		}
	}
	if len(pages) == 0 {
		return "", nil, fmt.Errorf("%s found nothing", p.Name)
	}
	said, hits := pagesSaid(pages)
	return said, hits, nil
}

// wbSearchID is a request id as WorkBuddy's client makes them: a UUID's
// hex without its dashes.
func wbSearchID() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
