package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/yetone/magpie/internal/provider"
)

// A Kimi Code plan searches the web with its own key, as kimi-cli's
// SearchWeb tool does (kimi_cli/tools/web/search.py, 1.52.0): a POST to the
// plan's /coding/v1/search of
//
//	{"text_query":"…","limit":5,"enable_page_crawling":false,"timeout_seconds":30}
//
// with the key as a Bearer, answered with
//
//	{"search_results":[{"site_name","title","url","snippet","content","date","icon","mime"}]}
//
// No model is asked. A model served by a Kimi Code plan has its searches
// done there first, so a Kimi user's searches stay on Kimi, as a provider
// that searches by itself does; failing, the searcher magpie picks or
// Settings names comes next. For other providers' models a plan searches
// only when Settings names it: picking it by itself would spend the plan's
// allowance on them, and change the searcher users have now.

// kimiSearchAgent is the User-Agent kimi-cli sends (constant.py,
// get_user_agent: KimiCLI/<version>).
const kimiSearchAgent = "KimiCLI/1.52.0"

// searchOnKey holds the provider serving the conversation a search is
// made for.
type searchOnKey struct{}

// searchCallKey holds the id of the model's call a search answers, which
// kimi-cli sends as X-Msh-Tool-Call-Id.
type searchCallKey struct{}

// canSearchFor is whether a model of p that can't search is given magpie's
// search: one of p's own (a Kimi Code plan, a Google sign-in), or
// canSearch.
func canSearchFor(p provider.Provider) bool {
	return provider.KimiCodeSearch(p) != "" || googleAccount(p) && searcherModel(p) != "" || canSearch()
}

// searchingOn is ctx for the searches made for a model of p.
func searchingOn(ctx context.Context, p provider.Provider) context.Context {
	return context.WithValue(ctx, searchOnKey{}, p)
}

// ownSearcher is the provider serving the conversation when it searches
// for itself: by a search service of its own plan (Kimi Code), or, a
// Google sign-in, by its own Gemini's googleSearch — which a request with
// function tools can't have (codeAssistSearches), so the search goes out
// on its own, and not to another subscription's allowance (#757).
func ownSearcher(ctx context.Context) (provider.Provider, bool) {
	p, ok := ctx.Value(searchOnKey{}).(provider.Provider)
	return p, ok && (provider.KimiCodeSearch(p) != "" || googleAccount(p) && searcherModel(p) != "")
}

// ownSearch searches with the conversation's own provider (ownSearcher).
func (s *Server) ownSearch(ctx context.Context, p provider.Provider, query string) (string, []Hit, error) {
	if provider.KimiCodeSearch(p) != "" {
		return s.kimiSearch(ctx, p, query)
	}
	ctx, cancel := context.WithTimeout(context.WithValue(ctx, searchingKey{}, true), searchTimeout)
	defer cancel()
	return s.searchWith(ctx, p, searcherModel(p), query)
}

// searchBy searches with one searcher: a Kimi Code plan by its search
// service, any other by its model.
func (s *Server) searchBy(ctx context.Context, p provider.Provider, model, query string) (string, []Hit, error) {
	if provider.KimiCodeSearch(p) != "" {
		return s.kimiSearch(ctx, p, query)
	}
	return s.searchWith(ctx, p, model, query)
}

// kimiSearch asks a Kimi Code plan's search service.
func (s *Server) kimiSearch(ctx context.Context, p provider.Provider, query string) (string, []Hit, error) {
	// the service gives itself 30 s, as kimi-cli asks
	ctx, cancel := context.WithTimeout(ctx, searchTimeout/2)
	defer cancel()
	body, _ := json.Marshal(map[string]any{"text_query": query, "limit": searchHits, "enable_page_crawling": false, "timeout_seconds": 30})
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.KimiCodeSearch(p), bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	r.Header.Set("User-Agent", kimiSearchAgent)
	r.Header.Set("Authorization", "Bearer "+p.Key)
	if id, _ := ctx.Value(searchCallKey{}).(string); id != "" {
		r.Header.Set("X-Msh-Tool-Call-Id", id)
	}
	res, err := s.client.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return "", nil, fmt.Errorf("%s: no answer in time", p.Name)
		}
		return "", nil, fmt.Errorf("%s: %w", p.Name, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode >= 300 {
		return "", nil, errors.New(p.Name + ": " + provider.APIError(b, res.Status))
	}
	var out struct {
		Results *[]struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Snippet string `json:"snippet"`
			Content string `json:"content"`
			Date    string `json:"date"`
		} `json:"search_results"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.Results == nil {
		return "", nil, fmt.Errorf("%s: unexpected reply: %s", p.Name, provider.APIError(b, res.Status))
	}
	var pages []foundPage
	for _, x := range *out.Results {
		if x.URL == "" {
			continue
		}
		title := searchPlain(x.Title)
		if title == "" {
			title = x.URL
		}
		text := x.Content
		if text == "" {
			text = x.Snippet
		}
		pages = append(pages, foundPage{Title: title, URL: x.URL, Text: clipText(searchPlain(text), pageText), Age: x.Date})
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
