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
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
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

// canSearch is whether magpie can answer its web_search tool: plugins/search
// is on (searchServed), and a provider searches for it, or a search API
// does.
func canSearch() bool {
	if !searchServed() {
		return false
	}
	if _, _, ok := searcher(); ok {
		return true
	}
	return len(provider.SearchAPIs()) > 0
}

// CanSearch is canSearch for the agents' rows: whether a web_search
// server tool asked of any model here is answered.
func CanSearch() bool { return canSearch() }

// Searcher names the provider and model magpie searches with for a model
// that can't, "" when none can.
func Searcher() string {
	if p, m, ok := searcher(); ok {
		return searcherName(p, m)
	}
	return ""
}

// searcherName is a searcher by its provider and model; a Kimi Code plan,
// which searches with no model, and a ChatGPT account by the backend's
// search, by its name.
func searcherName(p provider.Provider, model string) string {
	if model == "" || model == codexSearchModel {
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

// NamedSearchModel is the model the searcher Settings names searches with,
// as "<provider>/<model>": what an agent's own web search row asks, so a
// search goes to the searcher the user picked rather than first to the
// agent's model (dsh, 我只会玩单机 on Discord). "" when Settings names
// none, provider search is off, the one named can't be used, or it
// searches with no model (a Kimi Code plan's search service).
func NamedSearchModel() string {
	p, m, why := chosenSearcher()
	if p == nil || why != "" || m == "" || m == codexSearchModel {
		return ""
	}
	return p.ID + "/" + m
}

// SearcherUnused is why the searcher Settings names isn't used (one of
// the Searcher* reasons), "" when it is or none is named.
func SearcherUnused() string {
	_, _, why := chosenSearcher()
	return why
}

// apiSearch asks the search APIs, in their order, until one answers.
func (s *Server) apiSearch(ctx context.Context, query string) (string, []Hit, error) {
	query, err := maskedQuery(ctx, query, nil)
	if err != nil {
		return "", nil, err
	}
	var errs []error
	for _, a := range provider.SearchAPIs() {
		pages, err := s.askSearchAPIKeys(ctx, a, query)
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

// askSearchAPIKeys asks a search API with its keys, in searchKeyOrder:
// on to the next while one is refused (out of credit, rate limited,
// turned away; #1477), which then rests (searchRestFor). Anything else, such as
// no answer in time, isn't about the key, and the next isn't tried.
func (s *Server) askSearchAPIKeys(ctx context.Context, a provider.SearchAPI, query string) ([]foundPage, error) {
	keys := a.Keys()
	if len(keys) == 0 {
		keys = []string{""}
	}
	var errs []error
	for _, i := range searchKeyOrder(a, keys, time.Now()) {
		one := a
		one.Key = keys[i]
		ctx, cancel := context.WithTimeout(ctx, searchTimeout/4)
		start := time.Now()
		pages, err := s.askSearchAPI(ctx, one, query)
		cancel()
		logSearchAPI(ctx, one, query, start, len(pages), err)
		if err == nil {
			wakeSearchKey(a.Vendor, keys[i])
			return pages, nil
		}
		var r searchRefused
		refused := errors.As(err, &r)
		if refused && keys[i] != "" {
			d, why := searchRestFor(a.Vendor, r.status, r.retryAfter, time.Now())
			restSearchKey(a.Vendor, keys[i], d, why, time.Now())
		}
		if len(keys) > 1 {
			err = fmt.Errorf("%s: %w", keyLabel(i, keys[i]), err)
		}
		errs = append(errs, err)
		if !refused {
			break
		}
	}
	return nil, errors.Join(errs...)
}

// logSearchAPI writes the request log's row of one ask of a search API
// with one key (usage.SourceSearch; cybercaius on Discord: which search
// API each search used, and how much, couldn't be seen): the API by name,
// where it was asked, the key masked, how long it took and how it ended.
func logSearchAPI(ctx context.Context, a provider.SearchAPI, query string, start time.Time, found int, err error) {
	rec := usage.Record{Requested: a.Name(), Host: provider.HostOf(a.Base())}
	if a.Key != "" {
		rec.ProviderKeyID, rec.ProviderKeyName = searchKeyID(a.Vendor, a.Key), provider.MaskSearchKey(a.Key)
	}
	logSearch(ctx, rec, query, start, found, err)
}

// logSearch writes rec, a search magpie ran with a search API or a plan's
// own search service, as the request log's row of it: the agent it was
// for, how long it took and how it ended (200, the API's refusal, the
// error, "found nothing"). The query is never kept, nor said in the
// error: the row may go to another computer the usage syncs with.
func logSearch(ctx context.Context, rec usage.Record, query string, start time.Time, found int, err error) {
	rec.Time, rec.Agent, rec.Kind, rec.Source = start, "magpie", "web_search", usage.SourceSearch
	rec.Millis = time.Since(start).Milliseconds()
	if f := searchFor(ctx); f != nil && f.Agent != "" {
		rec.Agent = f.Agent
	}
	who := access.Caller(ctx)
	rec.CallerKeyID, rec.CallerKeyName, rec.CallerIssuerID = who.KeyID, who.KeyName, who.IssuerID()
	switch {
	case err != nil && found == 0 && errors.Is(err, errFoundNothing):
		rec.Error = "found nothing"
	case err != nil:
		said := err.Error()
		var ue *url.Error // its URL holds the query of an API asked by GET
		if errors.As(err, &ue) {
			said = ue.Op + " " + provider.HostOf(ue.URL) + ": " + ue.Err.Error()
		}
		if q := strings.TrimSpace(query); q != "" {
			for _, form := range []string{q, url.QueryEscape(q), url.PathEscape(q)} {
				said = strings.ReplaceAll(said, form, "…")
			}
		}
		rec.Error = keepMsg(said)
		var r searchRefused
		if errors.As(err, &r) {
			rec.Status = r.status
		}
	case found == 0:
		rec.Error = "found nothing"
	default:
		rec.Status = http.StatusOK
	}
	budget.Append(rec)
}

// searchRefused is a search API's refusal of the key it was asked with,
// by its status: 401/403 turned away, 402/432/433 out of credit (Tavily's
// plan and pay-as-you-go limits), 429 rate limited.
type searchRefused struct {
	status     int
	msg        string
	retryAfter string // its Retry-After, "" when it said none
}

func (e searchRefused) Error() string { return e.msg }

// keyRefusal is whether an answer's status is about the key it was asked
// with (searchRefused).
func keyRefusal(status int) bool {
	switch status {
	case 401, 402, 403, 429, 432, 433:
		return true
	}
	return false
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
		hits = append(hits, Hit{Title: p.Title, URL: p.URL, PageAge: p.Age, Cited: p.Text})
	}
	return strings.TrimSpace(b.String()), hits
}

// askSearchAPI asks one search API for the query.
func (s *Server) askSearchAPI(ctx context.Context, a provider.SearchAPI, query string) ([]foundPage, error) {
	if v, ok := provider.SearchVendorOf(a.Vendor); ok && v.Plugin != "" {
		return s.askPluginSearch(ctx, a, v, query)
	}
	var r *http.Request
	var err error
	post := func(path string, body any) {
		b, _ := json.Marshal(body)
		if r, err = http.NewRequestWithContext(ctx, http.MethodPost, a.Base()+path, bytes.NewReader(b)); err == nil {
			r.Header.Set("Content-Type", "application/json")
		}
	}
	getAt := func(at string, q url.Values) {
		r, err = http.NewRequestWithContext(ctx, http.MethodGet, at+"?"+q.Encode(), nil)
	}
	get := func(path string, q url.Values) { getAt(a.Base()+path, q) }
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
	case "deepseek":
		// DeepSeek's Search API takes a query of 1 to 128 characters
		// (api-docs.deepseek.com/guides/search); its page text is its
		// summary, so need_content is left false
		post("/v1/tools/search", map[string]any{"query": firstRunes(query, deepseekQueryMax), "count": searchHits})
	case "volcengine":
		// Doubao Search Custom (volcengine.com/docs/87772/2272953): a
		// query of 1 to 100 characters, web pages only, and only those
		// with a URL (NeedUrl leaves out its 火山如意 cards, which have none)
		post("/search_api/web_search", map[string]any{"Query": firstRunes(query, volcQueryMax), "SearchType": "web",
			"Count": searchHits, "Filter": map[string]any{"NeedUrl": true}})
	case "wsa":
		// Tencent Cloud's Yuanbao Search by its service API key
		// (cloud.tencent.com/document/product/1806/130615): Cnt is one of
		// 10/20/30/40/50 (cloud.tencent.com/document/api/1806/121811), and
		// a smaller one is turned away, so the fewest, cut to searchHits
		// below
		post("/SearchPro", map[string]any{"Query": query, "Cnt": wsaCnt})
	case "searxng":
		get("/search", url.Values{"q": {query}, "format": {"json"}})
	case "you":
		// with a key, its Search API; without one, its free search (#1626),
		// at an address of the user's own when one is set
		switch {
		case a.Key != "":
			post("/v1/search", map[string]any{"query": query, "count": searchHits})
		case a.URL != "":
			get("/v1/agents/search", url.Values{"query": {query}, "count": {fmt.Sprint(searchHits)}})
		default:
			getAt(provider.YouFreeSearch, url.Values{"query": {query}, "count": {fmt.Sprint(searchHits)}})
		}
	default:
		return nil, fmt.Errorf("magpie can't ask %q", a.Vendor)
	}
	if err != nil {
		return nil, err
	}
	r.Header.Set("Accept", "application/json")
	r.Header.Set("User-Agent", SearchAgent)
	if a.Vendor == "you" {
		// the tag You.com asked for, to tell magpie's searches apart (#1626)
		r.Header.Set("User-Agent", SearchAgent+" youdotcom-integration/yetone-magpie")
	}
	if a.Key != "" {
		switch a.Vendor {
		case "brave":
			r.Header.Set("X-Subscription-Token", a.Key)
		case "exa", "you":
			r.Header.Set("X-API-Key", a.Key)
		default:
			r.Header.Set("Authorization", "Bearer "+a.Key)
		}
	}
	// the key goes to the address the API is set at (#1580) and nowhere
	// else: Go drops only Authorization on a redirect to another host,
	// never Brave's or Exa's own key headers, so such a redirect isn't
	// followed, and its answer is said as the error it is
	c := *s.client
	c.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if next.URL.Scheme != via[0].URL.Scheme || next.URL.Host != via[0].URL.Host {
			return http.ErrUseLastResponse
		}
		return nil
	}
	res, err := c.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("no answer in time")
		}
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode >= 300 && res.StatusCode < 400 && res.Header.Get("Location") != "" {
		return nil, fmt.Errorf("%s: it sends the search on to %s, which isn't given its key; set that address instead", res.Status, res.Header.Get("Location"))
	}
	status := res.StatusCode
	var volcMsg string
	switch a.Vendor {
	case "volcengine":
		// its error is in ResponseMetadata.Error, a code beside the
		// status, and may come with a 200
		status, volcMsg = volcSearchError(b, status)
	case "wsa":
		// Tencent Cloud's is in Response.Error, its HTTP status 200
		status, volcMsg = wsaSearchError(b, status)
	}
	if status >= 300 {
		msg := provider.APIError(b, res.Status)
		if volcMsg != "" {
			msg = volcMsg
		}
		if a.Vendor == "you" {
			// You.com's error is a code ("payment_required"), its message
			// beside it the words
			var e struct{ Message string }
			if json.Unmarshal(b, &e) == nil && e.Message != "" {
				msg = e.Message
			}
		}
		// an API that says the key back in its error doesn't get it into
		// magpie's logs or the model's turn
		if a.Key != "" {
			msg = strings.ReplaceAll(msg, a.Key, provider.MaskSearchKey(a.Key))
		}
		if keyRefusal(status) {
			return nil, searchRefused{status, msg, res.Header.Get("Retry-After")}
		}
		return nil, errors.New(msg)
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

// runPluginSearch asks a plugin's search API in the host (a test's has no host).
var runPluginSearch = plugin.RunSearch

// askPluginSearch asks a search API a plugin gives (its search hook) in the
// plugin host. It is asked as a built-in is: from askSearchAPIKeys, so with
// the user's keys in their order, the same time to answer, the same rest of
// a key it refused (the plugin throws an error with a status) and the same
// row in the request log; and with the query apiSearch masked. What it
// found is cut and cleaned as a built-in's pages are.
func (s *Server) askPluginSearch(ctx context.Context, a provider.SearchAPI, v provider.SearchVendor, query string) ([]foundPage, error) {
	got, err := runPluginSearch(ctx, v.Plugin, v.PluginID, query, searchHits, a.Key, a.Base())
	if err != nil {
		var r plugin.SearchRefused
		if errors.As(err, &r) {
			msg := r.Msg
			if a.Key != "" {
				msg = strings.ReplaceAll(msg, a.Key, provider.MaskSearchKey(a.Key))
			}
			if keyRefusal(r.Status) {
				return nil, searchRefused{r.Status, msg, r.RetryAfter}
			}
			return nil, errors.New(msg)
		}
		if ctx.Err() != nil {
			return nil, errors.New("no answer in time")
		}
		if a.Key != "" {
			err = errors.New(strings.ReplaceAll(err.Error(), a.Key, provider.MaskSearchKey(a.Key)))
		}
		return nil, err
	}
	var pages []foundPage
	for _, r := range got {
		if r.URL == "" {
			continue
		}
		title := searchPlain(r.Title)
		if title == "" {
			title = r.URL
		}
		pages = append(pages, foundPage{Title: title, URL: r.URL, Text: clipText(searchPlain(r.Text), pageText), Age: r.Age})
		if len(pages) == searchHits {
			break
		}
	}
	return pages, nil
}

// readSearchAPI reads the pages out of a search API's reply.
func readSearchAPI(vendor string, b []byte) ([]foundPage, error) {
	switch vendor {
	case "you":
		return readYouSearch(b)
	case "volcengine":
		return readVolcSearch(b)
	case "wsa":
		return readWSASearch(b)
	}
	type result struct {
		Title       string `json:"title"`
		URL         string `json:"url"`
		Content     string `json:"content"`
		Description string `json:"description"`
		Text        string `json:"text"`
		Markdown    string `json:"markdown"`
		Summary     string `json:"summary"` // deepseek: its longer text for a model
		Snippet     string `json:"snippet"` // deepseek: its short one
		Date        string `json:"date"`    // deepseek: RFC 3339, or null
	}
	var out struct {
		Results []result `json:"results"` // tavily, exa, searxng, deepseek
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
		for _, t := range []string{r.Summary, r.Text, r.Description, r.Markdown, r.Snippet} {
			if text == "" {
				text = t
			}
		}
		title := searchPlain(r.Title)
		if title == "" {
			title = r.URL
		}
		pages = append(pages, foundPage{Title: title, URL: r.URL, Text: clipText(searchPlain(text), pageText), Age: r.Date})
	}
	return pages, nil
}

// deepseekQueryMax is the most characters DeepSeek's Search API takes in a
// query; a longer one is turned away with 400.
const deepseekQueryMax = 128

// volcQueryMax is the most characters Doubao Search takes in a query.
const volcQueryMax = 100

// volcSearchError is the status a Doubao Search reply stands for, and its
// error's words: its ResponseMetadata.Error, read by its code, as a key
// turned away (403), out of credit (402) or rate limited (429), or as
// the status it came with; a reply with no error is its own status.
func volcSearchError(b []byte, status int) (int, string) {
	var out struct {
		ResponseMetadata struct {
			Error *struct {
				CodeN   int
				Code    string
				Message string
			}
		}
	}
	if json.Unmarshal(b, &out) != nil || out.ResponseMetadata.Error == nil {
		return status, ""
	}
	e := out.ResponseMetadata.Error
	code := e.CodeN
	if code == 0 {
		code, _ = strconv.Atoi(e.Code)
	}
	msg := strings.TrimSpace(fmt.Sprintf("%d %s", code, e.Message))
	switch code {
	case 10401, 10403, 10409:
		// a key it doesn't know, a service not opened for it, a plan's
		// key that can't search the web this way
		return 403, msg
	case 10406, 10408, 10410, 10412:
		// the free searches spent and none paid for, no plan, a plan
		// spent
		return 402, msg
	case 700429:
		return 429, msg
	}
	if status < 300 {
		status = 502
	}
	return status, msg
}

// readVolcSearch reads Doubao Search's reply: its pages under
// Result.WebResults, each with a Summary (what its docs give a model:
// the page's text about the query) and a short Snippet.
func readVolcSearch(b []byte) ([]foundPage, error) {
	type hit struct {
		Title, Url, Summary, Snippet, PublishTime string
	}
	var out struct {
		Result *struct {
			WebResults []hit
		}
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if out.Result == nil {
		return nil, errors.New("no Result in it")
	}
	var pages []foundPage
	for _, h := range out.Result.WebResults {
		if h.Url == "" {
			continue
		}
		text := h.Summary
		if strings.TrimSpace(text) == "" {
			text = h.Snippet
		}
		title := searchPlain(h.Title)
		if title == "" {
			title = h.Url
		}
		age := h.PublishTime
		if strings.HasPrefix(age, "1970-01-01") {
			// its time for a page it has none for
			age = ""
		}
		pages = append(pages, foundPage{Title: title, URL: h.Url, Text: clipText(searchPlain(text), pageText), Age: age})
	}
	return pages, nil
}

// wsaCnt is the Cnt Yuanbao Search is asked for: the fewest it takes.
const wsaCnt = 10

// wsaSearchError is the status a Yuanbao Search (Tencent Cloud) reply
// stands for, and its error's words. Tencent Cloud answers 200 whenever
// the request was handled (cloud.tencent.com/document/api/1806/121818),
// its error in Response.Error {Code, Message}, so the code decides: one
// about the key or the service not opened for it is a key turned away
// (403), one about an account in arrears (ResourceUnavailable, "请检查账号
// 是否欠费") or a spent quota out of credit (402), a frequency limit
// rate limited (429). Any other code is a plain error.
func wsaSearchError(b []byte, status int) (int, string) {
	var out struct {
		Response struct {
			Error *struct{ Code, Message string }
		}
	}
	if json.Unmarshal(b, &out) != nil || out.Response.Error == nil || out.Response.Error.Code == "" {
		return status, ""
	}
	e := out.Response.Error
	msg := strings.TrimSpace(e.Code + " " + e.Message)
	code, _, _ := strings.Cut(e.Code, ".")
	switch code {
	case "AuthFailure", "UnauthorizedOperation", "ResourceNotFound":
		// a key it doesn't take ("请检查服务密钥 API KEY 是否正确"), or
		// the service not opened ("用户资源未开通")
		return 403, msg
	case "ResourceUnavailable", "LimitExceeded", "ResourceInsufficient":
		return 402, msg
	case "RequestLimitExceeded":
		return 429, msg
	}
	if status < 300 {
		status = 502
	}
	return status, msg
}

// readWSASearch reads Yuanbao Search's reply: Response.Pages, each page
// a JSON object in a string, with its title, url, date, passage (its
// standard summary, in every edition) and content (a summary for the
// query, not in its 极速版).
func readWSASearch(b []byte) ([]foundPage, error) {
	var out struct {
		Response *struct {
			Pages []string
		}
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if out.Response == nil || out.Response.Pages == nil {
		return nil, errors.New("no Response.Pages in it")
	}
	var pages []foundPage
	for _, s := range out.Response.Pages {
		var h struct {
			Title, URL, Date, Passage, Content string
		}
		if json.Unmarshal([]byte(s), &h) != nil || h.URL == "" {
			continue
		}
		text := h.Passage
		if strings.TrimSpace(searchPlain(text)) == "" || searchPlain(text) == searchPlain(h.Title) {
			// a passage that is only its title says less than its content
			if c := searchPlain(h.Content); c != "" {
				text = h.Content
			}
		}
		title := searchPlain(h.Title)
		if title == "" {
			title = h.URL
		}
		pages = append(pages, foundPage{Title: title, URL: h.URL, Text: clipText(searchPlain(text), pageText), Age: h.Date})
	}
	return pages, nil
}

// firstRunes is s cut to at most n characters, with nothing added.
func firstRunes(s string, n int) string {
	i := 0
	for at := range s {
		if i == n {
			return s[:at]
		}
		i++
	}
	return s
}

// readYouSearch reads You.com's reply, free or keyed alike: its pages
// under results.web, then results.news, each with a description and the
// snippets that matched.
func readYouSearch(b []byte) ([]foundPage, error) {
	type hit struct {
		Title       string   `json:"title"`
		URL         string   `json:"url"`
		Description string   `json:"description"`
		Snippets    []string `json:"snippets"`
		PageAge     string   `json:"page_age"`
	}
	var out struct {
		Results *struct {
			Web  []hit `json:"web"`
			News []hit `json:"news"`
		} `json:"results"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if out.Results == nil {
		return nil, errors.New("no results in it")
	}
	var pages []foundPage
	for _, h := range append(out.Results.Web, out.Results.News...) {
		if h.URL == "" {
			continue
		}
		text := strings.Join(h.Snippets, " ")
		if text == "" {
			text = h.Description
		}
		title := searchPlain(h.Title)
		if title == "" {
			title = h.URL
		}
		pages = append(pages, foundPage{Title: title, URL: h.URL, Text: clipText(searchPlain(text), pageText), Age: h.PageAge})
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
