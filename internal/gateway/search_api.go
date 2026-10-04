package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/provider"
)

// When no provider can search, magpie's web_search tool is answered by a
// search API the user gave a key to (#419): what it found goes to the
// model as it came, each page's title, address and what it says, with no
// model asked to read it first.

// searchHits is how many pages a search API is asked for.
const searchHits = 6

// pageText is the most of a page's text a hit carries.
const pageText = 1500

// foundPage is one page a search API found.
type foundPage struct {
	Title, URL, Text string
	Age              string // how old the search said it is, "" when it didn't
}

// canSearch is whether magpie can answer its web_search tool: a provider
// searches for it, or a search API does.
func canSearch() bool {
	if _, _, ok := searcher(); ok {
		return true
	}
	return len(provider.SearchAPIs()) > 0
}

// Searcher names the provider and model magpie searches with for a model
// that can't, "" when none can.
func Searcher() string {
	if p, m, ok := searcher(); ok {
		return searcherName(p, m)
	}
	return ""
}

// searcherName is a searcher by its provider and model; a Kimi Code plan,
// which searches with no model, by its name.
func searcherName(p provider.Provider, model string) string {
	if model == "" {
		return p.Name
	}
	if e, ok := provider.ServedEntryOf(p.ID + "/" + model); ok && e.Name != "" {
		model = e.Name
	}
	return p.Name + " · " + model
}

// AutoSearcher names the provider and model magpie picks to search with
// when Settings names none, "" when none can.
func AutoSearcher() string {
	if p, m, ok := autoSearcher(); ok {
		return searcherName(p, m)
	}
	return ""
}

// SearcherUnused is why the searcher Settings names isn't used (one of
// the Searcher* reasons), "" when it is or none is named.
func SearcherUnused() string {
	_, _, why := chosenSearcher()
	return why
}

// apiSearch asks the search APIs, in their order, until one answers.
func (s *Server) apiSearch(ctx context.Context, query string) (string, []Hit, error) {
	var errs []error
	for _, a := range provider.SearchAPIs() {
		ctx, cancel := context.WithTimeout(ctx, searchTimeout/4)
		pages, err := s.askSearchAPI(ctx, a, query)
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", a.Name(), err))
			continue
		}
		if len(pages) == 0 {
			errs = append(errs, fmt.Errorf("%s found nothing", a.Name()))
			continue
		}
		said, hits := pagesSaid(pages)
		return said, hits, nil
	}
	return "", nil, errors.Join(errs...)
}

// pagesSaid is what the pages a search found say, as its model is given
// it, and the pages as the client hears of them.
func pagesSaid(pages []foundPage) (string, []Hit) {
	var b strings.Builder
	var hits []Hit
	for i, p := range pages {
		fmt.Fprintf(&b, "%d. %s — %s\n", i+1, p.Title, p.URL)
		if p.Text != "" {
			b.WriteString(p.Text + "\n")
		}
		b.WriteString("\n")
		hits = append(hits, Hit{Title: p.Title, URL: p.URL, PageAge: p.Age})
	}
	return strings.TrimSpace(b.String()), hits
}

// askSearchAPI asks one search API for the query.
func (s *Server) askSearchAPI(ctx context.Context, a provider.SearchAPI, query string) ([]foundPage, error) {
	var r *http.Request
	var err error
	post := func(path string, body any) {
		b, _ := json.Marshal(body)
		if r, err = http.NewRequestWithContext(ctx, http.MethodPost, a.Base()+path, bytes.NewReader(b)); err == nil {
			r.Header.Set("Content-Type", "application/json")
		}
	}
	get := func(path string, q url.Values) {
		r, err = http.NewRequestWithContext(ctx, http.MethodGet, a.Base()+path+"?"+q.Encode(), nil)
	}
	switch a.Vendor {
	case "tavily":
		post("/search", map[string]any{"query": query, "max_results": searchHits, "search_depth": "basic"})
	case "brave":
		get("/res/v1/web/search", url.Values{"q": {query}, "count": {fmt.Sprint(searchHits)}})
	case "exa":
		post("/search", map[string]any{"query": query, "numResults": searchHits,
			"contents": map[string]any{"text": map[string]any{"maxCharacters": pageText}}})
	case "firecrawl":
		post("/v2/search", map[string]any{"query": query, "limit": searchHits})
	case "searxng":
		get("/search", url.Values{"q": {query}, "format": {"json"}})
	default:
		return nil, fmt.Errorf("magpie can't ask %q", a.Vendor)
	}
	if err != nil {
		return nil, err
	}
	r.Header.Set("Accept", "application/json")
	r.Header.Set("User-Agent", SearchAgent)
	if a.Key != "" {
		switch a.Vendor {
		case "brave":
			r.Header.Set("X-Subscription-Token", a.Key)
		case "exa":
			r.Header.Set("x-api-key", a.Key)
		default:
			r.Header.Set("Authorization", "Bearer "+a.Key)
		}
	}
	res, err := s.client.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("no answer in time")
		}
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode >= 300 {
		return nil, errors.New(provider.APIError(b, res.Status))
	}
	pages, err := readSearchAPI(a.Vendor, b)
	if err != nil {
		return nil, fmt.Errorf("unexpected reply: %w", err)
	}
	if len(pages) > searchHits {
		pages = pages[:searchHits]
	}
	return pages, nil
}

// readSearchAPI reads the pages out of a search API's reply.
func readSearchAPI(vendor string, b []byte) ([]foundPage, error) {
	type result struct {
		Title       string `json:"title"`
		URL         string `json:"url"`
		Content     string `json:"content"`
		Description string `json:"description"`
		Text        string `json:"text"`
		Markdown    string `json:"markdown"`
	}
	var out struct {
		Results []result `json:"results"` // tavily, exa, searxng
		Web     struct {
			Results []result `json:"results"`
		} `json:"web"` // brave
		Data json.RawMessage `json:"data"` // firecrawl: {web: […]}, or […] on its v1
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	rs := out.Results
	switch vendor {
	case "brave":
		rs = out.Web.Results
	case "firecrawl":
		var v2 struct {
			Web []result `json:"web"`
		}
		if json.Unmarshal(out.Data, &v2) != nil || v2.Web == nil {
			json.Unmarshal(out.Data, &rs)
		} else {
			rs = v2.Web
		}
	}
	var pages []foundPage
	for _, r := range rs {
		if r.URL == "" {
			continue
		}
		text := r.Content
		for _, t := range []string{r.Text, r.Description, r.Markdown} {
			if text == "" {
				text = t
			}
		}
		title := searchPlain(r.Title)
		if title == "" {
			title = r.URL
		}
		pages = append(pages, foundPage{Title: title, URL: r.URL, Text: clipText(searchPlain(text), pageText)})
	}
	return pages, nil
}

var searchTags = regexp.MustCompile(`<[^>]*>`)

// searchPlain is a search API's text without the tags it marks matches with
// (Brave's <strong>), on one line.
func searchPlain(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(searchTags.ReplaceAllString(s, ""))), " ")
}

// clipText cuts s to at most n bytes, at a rune.
func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
