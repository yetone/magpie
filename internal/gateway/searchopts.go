package gateway

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
)

// searchScope is what a web_search call narrows its search by, beyond the
// query: the pages' sites, how recent they are, and images instead of pages.
// The ChatGPT backend's search takes these as its own fields (codex-rs
// codex-api search.rs: a query's domains and recency, image_query,
// settings.filters, settings.search_context_size); any other searcher is
// given the sites as search operators in the query, and ignores the rest.
type searchScope struct {
	Allowed, Blocked []string
	RecencyDays      int
	Images           bool
	Size             string // "low", "medium" or "high"
	Original         string // the query before the operators were added
}

type searchScopeKey struct{}

func scopeOf(ctx context.Context) *searchScope {
	s, _ := ctx.Value(searchScopeKey{}).(*searchScope)
	return s
}

// withScope is ctx whose search is narrowed by sc; an empty scope leaves it.
func withScope(ctx context.Context, sc *searchScope) context.Context {
	if sc == nil {
		return ctx
	}
	return context.WithValue(ctx, searchScopeKey{}, sc)
}

// siteOf is the host a domain argument names: a model may write a URL.
func siteOf(d string) string {
	d = strings.TrimSpace(d)
	if strings.Contains(d, "://") {
		if u, err := url.Parse(d); err == nil {
			d = u.Host
		}
	}
	d = strings.TrimPrefix(strings.ToLower(d), "www.")
	d, _, _ = strings.Cut(d, "/")
	if strings.ContainsAny(d, " \t\"'") {
		return ""
	}
	return d
}

func sitesOf(in []string) []string {
	var out []string
	for _, d := range in {
		if h := siteOf(d); h != "" {
			out = append(out, h)
		}
		if len(out) == 20 {
			break
		}
	}
	return out
}

// searchScoped is the query a searcher that has no such fields is asked,
// the sites as operators, and ctx holding the scope for the one that has.
func searchScoped(ctx context.Context, query string) (context.Context, string) {
	sc := scopeOf(ctx)
	if sc == nil {
		return ctx, query
	}
	sc.Original = query
	var b strings.Builder
	b.WriteString(query)
	if len(sc.Allowed) > 0 {
		b.WriteString(" (")
		for i, d := range sc.Allowed {
			if i > 0 {
				b.WriteString(" OR ")
			}
			b.WriteString("site:" + d)
		}
		b.WriteString(")")
	}
	for _, d := range sc.Blocked {
		b.WriteString(" -site:" + d)
	}
	return ctx, b.String()
}

// unscoped is the query as the call wrote it, for a searcher that takes the
// scope as fields.
func unscoped(ctx context.Context, query string) string {
	if sc := scopeOf(ctx); sc != nil && sc.Original != "" {
		return sc.Original
	}
	return query
}

// searchArgs are web_search's optional arguments after the query and count.
type searchArgs struct {
	AllowedDomains []string        `json:"allowed_domains"`
	BlockedDomains []string        `json:"blocked_domains"`
	RecencyDays    json.RawMessage `json:"recency_days"`
	Images         bool            `json:"images"`
	ContextSize    string          `json:"search_context_size"`
}

// scope is what the arguments narrow by; nil when they name nothing.
func (a searchArgs) scope() *searchScope {
	sc := &searchScope{Allowed: sitesOf(a.AllowedDomains), Blocked: sitesOf(a.BlockedDomains), Images: a.Images}
	if n := searchCountArg(a.RecencyDays); n != nil && *n > 0 {
		sc.RecencyDays = min(*n, 3650)
	}
	switch c := strings.ToLower(strings.TrimSpace(a.ContextSize)); c {
	case "low", "medium", "high":
		sc.Size = c
	}
	if len(sc.Allowed) == 0 && len(sc.Blocked) == 0 && sc.RecencyDays == 0 && !sc.Images && sc.Size == "" {
		return nil
	}
	return sc
}

// searchScopeSchema is the properties searchMCPSchema adds.
const searchScopeSchema = `"allowed_domains":{"type":"array","items":{"type":"string"},"description":"Only return pages from these sites, e.g. [\"docs.python.org\"]"},` +
	`"blocked_domains":{"type":"array","items":{"type":"string"},"description":"Leave out pages from these sites"},` +
	`"recency_days":{"type":"integer","minimum":1,"description":"Only pages from the last this many days"},` +
	`"images":{"type":"boolean","description":"Search for images instead of pages"},` +
	`"search_context_size":{"type":"string","enum":["low","medium","high"],"description":"How much of each page to return"}`
