package gateway

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// web_search's optional arguments reach the ChatGPT backend's search as its
// own fields (an image search is the image_query command), and any other
// searcher is asked the sites as operators. A real Plus account answered
// an image_query on 2026-11-12 with "<title> (<page>)", a citation mark
// and "Image URL: <url># <caption>".
func TestSearchScopeReachesCodexSearch(t *testing.T) {
	codexSignedIn(t)
	f := newFakeChatGPT(t)
	s := New()
	args := searchArgs{AllowedDomains: []string{"https://www.Go.dev/doc", "bad domain"}, BlockedDomains: []string{"spam.example"},
		RecencyDays: json.RawMessage(`"30"`), Images: true, ContextSize: "high"}
	ctx := withScope(withSearchCount(context.Background(), 4), args.scope())
	if _, _, err := s.webSearch(ctx, "go gopher"); err != nil {
		t.Fatal(err)
	}
	_, bodies := f.asked()
	q := bodies[0]
	cmds := q["commands"].(map[string]any)
	if _, ok := cmds["search_query"]; ok {
		t.Fatalf("an image search asked for pages: %v", q)
	}
	iq := cmds["image_query"].([]any)[0].(map[string]any)
	set := q["settings"].(map[string]any)
	filters := set["filters"].(map[string]any)
	if iq["q"] != "go gopher" || iq["recency"] != float64(30) || len(iq["domains"].([]any)) != 1 || iq["domains"].([]any)[0] != "go.dev" ||
		filters["blocked_domains"].([]any)[0] != "spam.example" || set["search_context_size"] != "high" ||
		set["image_settings"].(map[string]any)["max_results"] != float64(4) {
		t.Fatalf("body %v", q)
	}

	// no arguments: the body is the plain search it was
	if _, _, err := s.webSearch(context.Background(), "go gopher"); err != nil {
		t.Fatal(err)
	}
	_, bodies = f.asked()
	if _, ok := bodies[0]["commands"].(map[string]any)["search_query"]; !ok || bodies[0]["settings"].(map[string]any)["filters"] != nil {
		t.Fatalf("body %v", bodies[0])
	}
}

func TestSearchScopeOperatorsForOtherSearchers(t *testing.T) {
	args := searchArgs{AllowedDomains: []string{"go.dev", "pkg.go.dev"}, BlockedDomains: []string{"x.com"}}
	ctx := withScope(context.Background(), args.scope())
	ctx, q := searchScoped(ctx, "gopher")
	if q != "gopher (site:go.dev OR site:pkg.go.dev) -site:x.com" || unscoped(ctx, q) != "gopher" {
		t.Fatalf("query %q, own %q", q, unscoped(ctx, q))
	}
	if _, q := searchScoped(context.Background(), "gopher"); q != "gopher" {
		t.Fatalf("no scope: %q", q)
	}
	if (searchArgs{RecencyDays: json.RawMessage(`0`), ContextSize: "huge"}).scope() != nil {
		t.Fatal("empty arguments made a scope")
	}
}

func TestImageResultsReadAsPages(t *testing.T) {
	out := "Bridges | example.com (https://example.com/g)\nciteturn0image0 \nImage URL: https://example.com/a.jpg# Golden Gate Bridge\n\nIt is orange."
	pages := codexPages(out, codexSearchReply{}, 5)
	if len(pages) != 1 || !strings.HasPrefix(pages[0].Text, "Image: https://example.com/a.jpg Golden Gate Bridge") {
		t.Fatalf("%+v", pages)
	}
}

func TestSearchMCPSchemaOffersScope(t *testing.T) {
	var v struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal([]byte(searchMCPSchema(5)), &v); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"query", "count", "allowed_domains", "blocked_domains", "recency_days", "images", "search_context_size"} {
		if v.Properties[k] == nil {
			t.Fatalf("no %s in %v", k, v.Properties)
		}
	}
	if strings.Contains(strings.ToLower(searchMCPSchema(5)), "magpie") {
		t.Fatal("the schema names magpie")
	}
}

// An image search's output, as a real Plus account answered one on
// 2026-11-15 (testdata/codex-image-search.json, image_query "golden gate
// bridge"), puts each rule straight after its page's last word. Its twelve
// images were read as one page, so a count past 1 gave one image (LBM_OASIG
// on X).
func TestImageSearchGivesTheCount(t *testing.T) {
	b, err := os.ReadFile("testdata/codex-image-search.json")
	if err != nil {
		t.Fatal(err)
	}
	var reply codexSearchReply
	if err := json.Unmarshal(b, &reply); err != nil || reply.Output == nil {
		t.Fatal(err)
	}
	for _, n := range []int{1, 5, 12} {
		pages := codexPages(*reply.Output, reply, n)
		if len(pages) != n {
			t.Fatalf("count %d: %d pages", n, len(pages))
		}
		seen := map[string]bool{}
		for _, p := range pages {
			img, _, _ := strings.Cut(p.Text, "\n")
			if !strings.HasPrefix(img, "Image: https://") || strings.Contains(p.Text, "-----") || seen[img] {
				t.Fatalf("count %d: %+v", n, p)
			}
			seen[img] = true
		}
	}
	if pages := codexPages(*reply.Output, reply, 5); pages[1].URL != "https://www.worldatlas.com/articles/the-7-wonders-of-california.html" {
		t.Fatalf("second page %+v", pages[1])
	}
}

// A longer line of dashes in a page's text is not a rule.
func TestCodexBlocksKeepLongDashLines(t *testing.T) {
	rule := strings.Repeat("-", 80) + "\n"
	long := strings.Repeat("-", 100) + "\n"
	got := codexBlocks("A (https://a.example)\ntext\n" + long + "more" + rule + "B (https://b.example)\nb")
	if len(got) != 2 || !strings.Contains(got[0], long+"more") || !strings.HasPrefix(got[1], "B ") {
		t.Fatalf("%q", got)
	}
}
