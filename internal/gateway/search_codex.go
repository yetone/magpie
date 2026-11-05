package gateway

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// A ChatGPT account searches the web for a model that can't by the ChatGPT
// backend's own search, the one Codex's web.run tool runs (AlphaSearchPath),
// not by a model of the account asked to search: the search is answered by
// itself, with no model turn, as Codex asks it (codex-rs ext/web-search,
// tool.rs: a SearchRequest, the response's output given to the model as the
// tool's result). On a real Plus account (2026-11-03) 151 such searches
// left the 5-hour window where it was, each answered in about 2s, while 51
// searches by gpt-6-luna through /responses (about 9k tokens in each) took
// 1% of it, each in 7-15s (lucy_85908 on Discord). The account's model
// searches only when the backend's search fails, or when Settings names a
// model of it.

// codexSearchModel is the "model" a ChatGPT account searches with when it
// searches by the backend's search: "<provider>/@search" in Settings'
// Searcher. No model id starts with "@".
const codexSearchModel = "@search"

// codexSearchBudget is the max_output_tokens Codex sends with a search: the
// truncation budget of its models' tool output (10,000 tokens). The backend
// answered the same whatever it was (1,000 to 10,000 tried).
const codexSearchBudget = 10000

// codexSearches is whether p searches by the ChatGPT backend's search: a
// ChatGPT account (Codex's sign-in).
func codexSearches(p provider.Provider) bool {
	return p.Account != nil && p.Account.Agent == "codex"
}

// webAccessKey holds the client's web search mode, as Codex sends it in
// external_web_access: false for "cached", true for "live", an object for
// "indexed".
type webAccessKey struct{}

// withWebAccess is ctx whose searches are made in the mode v says, as the
// client sent it; none sent leaves ctx as it is.
func withWebAccess(ctx context.Context, v json.RawMessage) context.Context {
	if len(bytes.TrimSpace(v)) == 0 || string(bytes.TrimSpace(v)) == "null" {
		return ctx
	}
	return context.WithValue(ctx, webAccessKey{}, v)
}

// webAccess is the external_web_access a search with ctx is sent with: the
// client's (#5018: Codex's web_search = "cached" was dropped, and every
// search went live), else true, a live search, as Codex's "live" sends.
func webAccess(ctx context.Context) json.RawMessage {
	if v, ok := ctx.Value(webAccessKey{}).(json.RawMessage); ok {
		return v
	}
	return json.RawMessage("true")
}

// codexSearchReply is the ChatGPT backend's SearchResponse: output is what
// Codex shows the model, results the pages it names.
type codexSearchReply struct {
	Output  *string `json:"output"`
	Results []struct {
		RefID   string `json:"ref_id"`
		Title   string `json:"title"`
		URL     string `json:"url"`
		Snippet string `json:"snippet"`
	} `json:"results"`
}

// codexSearchRule is the line between the pages of a search's output.
var codexSearchRule = "\n" + strings.Repeat("-", 80) + "\n"

// codexSearchBy searches with a ChatGPT account: by the backend's search,
// then, when it fails, by the account's small model.
func (s *Server) codexSearchBy(ctx context.Context, p provider.Provider, query string) (string, []Hit, error) {
	said, hits, err := s.askOwnSearch(ctx, p, provider.CodexBase, query, s.codexSearch)
	if err == nil || ctx.Err() != nil {
		return said, hits, err
	}
	m := searcherModel(p)
	if m == "" {
		return "", nil, err
	}
	log.Printf("web search on ChatGPT (%s) failed, searching with %s: %v", p.Name, m, err)
	said, hits, next := s.searchWith(ctx, p, m, query, searchEffort(p, m))
	if next != nil {
		return "", nil, errors.Join(err, next)
	}
	return said, hits, nil
}

// codexSearch asks the ChatGPT backend's search, as Codex's web.run does.
func (s *Server) codexSearch(ctx context.Context, p provider.Provider, query string) (string, []Hit, error) {
	ctx, cancel := context.WithTimeout(s.metered(p.Via(ctx), p, ""), searchTimeout/2)
	defer cancel()
	q, err := maskedQuery(ctx, query, &p)
	if err != nil {
		return "", nil, err
	}
	// the model a search names is the turn's in Codex; the backend answers
	// the same whichever it is, so the account's own small model
	model := searcherModel(p)
	if model == "" {
		model = "gpt-5"
	}
	body, _ := json.Marshal(map[string]any{
		"id":       newUUID(),
		"model":    model,
		"commands": map[string]any{"search_query": []map[string]any{{"q": q}}},
		"settings": map[string]any{
			"allowed_callers":     []string{"direct"},
			"external_web_access": webAccess(ctx),
		},
		"max_output_tokens": codexSearchBudget,
	})
	who := p.Name + ": "
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.CodexBase+AlphaSearchPath, bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	// a nil body: the search is answered in JSON, not streamed
	if err := p.Sign(ctx, r, provider.Responses, nil); err != nil {
		return "", nil, errors.New(who + err.Error())
	}
	if why, revoked := provider.RevokedSignIn(p.Account, r); revoked {
		return "", nil, errors.New(who + why)
	}
	res, err := s.client.Do(r)
	res = tapSearch(ctx, r, body, res)
	if err != nil {
		if ctx.Err() != nil {
			return "", nil, fmt.Errorf("%sno answer in time", who)
		}
		return "", nil, fmt.Errorf("%s%w", who, err)
	}
	defer res.Body.Close()
	noteSignIn(ctx, p.Account, r, res)
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode >= 300 {
		err := errors.New(who + provider.APIError(b, res.Status))
		if res.StatusCode == http.StatusTooManyRequests {
			err = cantServe{err}
		}
		return "", nil, err
	}
	var out codexSearchReply
	if err := json.Unmarshal(b, &out); err != nil || out.Output == nil {
		return "", nil, fmt.Errorf("%sunexpected reply: %s", who, provider.APIError(b, res.Status))
	}
	pages := codexPages(*out.Output, out, searchCount(ctx))
	if len(pages) == 0 {
		return "", nil, fmt.Errorf("%s%w", who, errFoundNothing)
	}
	said, hits := pagesSaid(pages)
	return said, hits, nil
}

// codexPages are the pages a search's output names, in its order: each
// is "Title (URL)", then a line opening with its citation mark (the
// private-use "\ue200cite\ue202turn0search0\ue201") and "[wordlim: N]",
// then what the page says, and a rule of 80 dashes before the next. The
// results name each page's title and address by its ref_id, which the
// mark carries.
func codexPages(output string, out codexSearchReply, n int) []foundPage {
	type result struct{ title, url, snippet string }
	byRef := map[string]result{}
	for _, x := range out.Results {
		if x.RefID != "" && x.URL != "" {
			byRef[x.RefID] = result{x.Title, x.URL, x.Snippet}
		}
	}
	var pages []foundPage
	for _, block := range strings.Split(output, codexSearchRule) {
		head, rest, _ := strings.Cut(strings.TrimSpace(block), "\n")
		title, link := head, ""
		if i := strings.LastIndex(head, " ("); i >= 0 && strings.HasSuffix(head, ")") {
			title, link = head[:i], head[i+2:len(head)-1]
		}
		ref := ""
		if i := strings.Index(rest, "\ue202"); i >= 0 {
			if j := strings.Index(rest[i:], "\ue201"); j >= 0 {
				ref = rest[i+len("\ue202") : i+j]
				rest = rest[i+j+len("\ue201"):]
			}
		}
		rest = strings.TrimSpace(rest)
		if strings.HasPrefix(rest, "[wordlim:") {
			if _, after, ok := strings.Cut(rest, "]"); ok {
				rest = after
			}
		}
		text := rest
		if r, ok := byRef[ref]; ok {
			title, link = cmp.Or(strings.TrimSpace(r.title), title), r.url
			if strings.TrimSpace(text) == "" {
				text = r.snippet
			}
		}
		if !strings.HasPrefix(link, "http://") && !strings.HasPrefix(link, "https://") {
			continue
		}
		title = searchPlain(title)
		if title == "" {
			title = link
		}
		pages = append(pages, foundPage{Title: title, URL: link, Text: clipText(searchPlain(strings.Map(dropPrivateUse, text)), pageText)})
		if len(pages) == n {
			break
		}
	}
	return pages
}

// dropPrivateUse leaves out the private-use characters OpenAI marks
// citations with.
func dropPrivateUse(r rune) rune {
	if r >= 0xe000 && r <= 0xf8ff {
		return -1
	}
	return r
}
