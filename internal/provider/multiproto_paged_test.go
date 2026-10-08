package provider

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
)

// The shapes below are openrouter.ai's own, as it answered on 2026-10-06.
// The catalog is left as it came, with the long descriptions cut, as
// nothing here reads them; it is also the list of ids OpenRouter serves,
// which is what says of the two pages under testdata that they name none
// (openRouterNamespaced).

// openRouterCatalog is /api/v1/models without the Anthropic header: the
// whole catalog, 466 ids, the Anthropic models among them under their own
// `anthropic/` author, nothing paged. It is the list of ids OpenRouter
// serves, which is what says of the two pages below that they are not
// models.
const openRouterCatalog = `{"data":[` +
	`{"id":"mistralai/mistral-large-4-0","name":"Mistral: Mistral Large 4","context_length":524288,` +
	`"architecture":{"input_modalities":["text","image"],"output_modalities":["text"],"modality":"text+image->text"}},` +
	`{"id":"openai/gpt-6.1-sol","name":"OpenAI: GPT-6.1 Sol","context_length":1050000,` +
	`"architecture":{"input_modalities":["file","image","text"],"output_modalities":["text"],"modality":"text+image+file->text"}},` +
	`{"id":"anthropic/claude-sonnet-5.5","name":"Anthropic: Claude Sonnet 5.5","context_length":1000000,` +
	`"architecture":{"input_modalities":["text","image","file"],"output_modalities":["text"],"modality":"text+image+file->text"}},` +
	`{"id":"anthropic/claude-sonnet-5.5:batch","name":"Anthropic: Claude Sonnet 5.5 (batch)","context_length":1000000,` +
	`"architecture":{"input_modalities":["text","image","file"],"output_modalities":["text"],"modality":"text+image+file->text"}},` +
	`{"id":"anthropic/claude-opus-5","name":"Anthropic: Claude Opus 5","context_length":1000000,` +
	`"architecture":{"input_modalities":["text","image","file"],"output_modalities":["text"],"modality":"text+image+file->text"}}` +
	`],"total_count":466,"links":{"next":null}}`

// openRouterNamespacedCursor is where the first namespaced page ends, and
// so the after_id the second one is asked at: the id the endpoint put in
// last_id, which is the twenty-first of its list and the newest model in
// it (TestOpenRouterNamespacedPagesAreTheEndpointShape reads it back off
// the page itself, so the two cannot drift apart).
const openRouterNamespacedCursor = "anthropic/openai/gpt-6-luna-pro[1m]"

// openRouterFile is the bytes of one of the endpoint's own replies.
func openRouterFile(t *testing.T, file string) []byte {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// openRouterNamespaced is /api/v1/models as openrouter.ai answers the
// Anthropic version header, page for page: the bytes of the two replies it
// gave on 2026-10-06, under testdata, and nothing else. Twenty ids newest
// first, every one with `anthropic/` in front of the vendor's own, saying
// there is another page and where that one starts; and under that cursor
// 413 more, saying there is none after it. The two pages have nothing in
// common, and neither of them has anything in common with the catalog
// above — following the list leads deeper into the namespace, never out of
// it. A cursor the endpoint does not know answers with an empty list and
// no has_more, as it did.
func openRouterNamespaced(t *testing.T, after string) string {
	t.Helper()
	file := "testdata/openrouter-models-anthropic-1.json"
	switch after {
	case "":
	case openRouterNamespacedCursor:
		file = "testdata/openrouter-models-anthropic-2.json"
	default:
		return `{"data":[]}`
	}
	return string(openRouterFile(t, file))
}

// pagedOnceThenAgain is a list that says it has more and answers the very
// same page again under the id just asked for — one of the four ways
// following a list stops, and what a relay whose cursors do nothing gives.
// The ids are the namespaced ones of the pages above, as those are the ids
// a namespaced reply carries; what matters here is only that the second
// answer is the first one again.
const pagedOnceThenAgain = `{"data":[` +
	`{"id":"anthropic/mistralai/mistral-large-4-0","type":"model","display_name":"Mistral: Mistral Large 4","max_input_tokens":524288,"max_tokens":262144,"capabilities":null},` +
	`{"id":"anthropic/openai/gpt-6.1-sol","type":"model","display_name":"OpenAI: GPT-6.1 Sol","max_input_tokens":1050000,"max_tokens":128000,"capabilities":null},` +
	`{"id":"anthropic/openai/gpt-6.1-sol[1m]","type":"model","display_name":"OpenAI: GPT-6.1 Sol","max_input_tokens":1050000,"max_tokens":128000,"capabilities":null},` +
	`{"id":"anthropic/claude-sonnet-5.5","type":"model","display_name":"Anthropic: Claude Sonnet 5.5","max_input_tokens":1000000,"max_tokens":128000,"capabilities":null},` +
	`{"id":"anthropic/claude-sonnet-5.5[1m]","type":"model","display_name":"Anthropic: Claude Sonnet 5.5","max_input_tokens":1000000,"max_tokens":128000,"capabilities":null},` +
	`{"id":"anthropic/claude-sonnet-5.5:batch[1m]","type":"model","display_name":"Anthropic: Claude Sonnet 5.5 (batch)","max_input_tokens":1000000,"max_tokens":128000,"capabilities":null},` +
	`{"id":"anthropic/claude-opus-5","type":"model","display_name":"Anthropic: Claude Opus 5","max_input_tokens":1000000,"max_tokens":128000,"capabilities":null},` +
	`{"id":"anthropic/typesafe/jev-router[1m]","type":"model","display_name":"TypeSafe: Jev Router","max_input_tokens":1000000,"max_tokens":null,"capabilities":null},` +
	`{"id":"anthropic/cohere/command-a-plus","type":"model","display_name":"Cohere: Command A+","max_input_tokens":128000,"max_tokens":64000,"capabilities":null}` +
	`],"has_more":true,"first_id":"anthropic/mistralai/mistral-large-4-0",` +
	`"last_id":"anthropic/cohere/command-a-plus"}`

// oneHome gives a test a home of its own, so nothing it fetches is read
// from or written to the machine's own magpie.
func oneHome(t *testing.T) {
	t.Helper()
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}
}

// A list that says it has more is a page of the vendor's catalog, not the
// catalog: a model the user picked that lives on a later page is not in
// this reply, and under the rule that a base which answers without a model
// has dropped it (#904) the pick would go with it, for good and with no way
// back. So a paged reply says nothing about what that base serves today:
// the base is treated as one that could not be asked, which keeps what it
// listed last time.
//
// Any /v1/models that paginates the way the Anthropic and OpenAI lists do
// is in this — the shape, not the vendor. OpenRouter is what was observed
// serving one; the check reads has_more and no host.
func TestFetchKeepsPicksWhenAModelListIsPaginated(t *testing.T) {
	oneHome(t)
	var paged atomic.Bool
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"gpt-a"},{"id":"anthropic/claude-opus-5"}]}`))
	}))
	defer chat.Close()
	// the whole list to begin with, then a page of it: claude-opus-5 is on
	// neither, so a reply read as the whole list drops it
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if paged.Load() {
			w.Write([]byte(pagedOnceThenAgain))
			return
		}
		w.Write([]byte(`{"data":[{"id":"anthropic/claude-opus-5"},{"id":"claude-b"}]}`))
	}))
	defer anthropic.Close()

	if err := Save(Provider{ID: "paged", Name: "Paged", Key: "sk-p",
		Chat: chat.URL + "/v1", Anthropic: anthropic.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	fetch := func() ([]string, []string) {
		p, err := Find("paged")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Fetch(context.Background()); err != nil {
			t.Fatal(err)
		}
		p, _ = Find("paged")
		live, _, ok := p.live()
		if !ok {
			t.Fatal("no list kept")
		}
		return idsOf(live), p.Models
	}

	got, _ := fetch()
	if want := []string{"gpt-a", "anthropic/claude-opus-5", "claude-b"}; !slices.Equal(got, want) {
		t.Fatalf("list after the first round: %v, want %v", got, want)
	}
	p, _ := Find("paged")
	p.Models = []string{"gpt-a", "anthropic/claude-opus-5", "claude-b"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}

	// the base answers with a page of its list, which leaves out both of the
	// models it listed. A page is not an answer, so neither is dropped.
	paged.Store(true)
	got, picks := fetch()
	if want := []string{"gpt-a", "anthropic/claude-opus-5", "claude-b"}; !slices.Equal(got, want) {
		t.Fatalf("list after the base answered with a page: %v, want %v", got, want)
	}
	if want := []string{"gpt-a", "anthropic/claude-opus-5", "claude-b"}; !slices.Equal(picks, want) {
		t.Fatalf("picks after the base answered with a page: %v, want %v", picks, want)
	}
}

// OpenRouter's Anthropic base is not asked for the model list at all.
//
// With the Anthropic version header its /api/v1/models answers with the
// catalog again, twenty newest first, every id with `anthropic/` in front
// of the vendor's own (`anthropic/mistralai/mistral-large-4-0`) or with a
// `[1m]` suffix OpenRouter does not serve, and the cursors of the pages
// after (observed 2026-10-06). Merged into the
// union those twenty are twenty entries of the model picker that name no
// model: none of them is in OpenRouter's own catalog above, which is the
// list of ids it serves. That is what
// #904's reporter saw as a long, cluttered list.
//
// The rule is OpenRouter's, and is scoped to it: the same fetch for another
// vendor still merges what its Anthropic base lists, ids and all
// (TestFetchKeepsAnotherVendorsAnthropicList).
func TestFetchDoesNotMergeOpenRoutersNamespacedList(t *testing.T) {
	oneHome(t)
	asked, urls := fakeOpenRouterCatalog(t, func(after string) string { return openRouterNamespaced(t, after) })

	// no Preset, so the rule cannot be the preset's: it is the host the
	// user's own Chat base sits at
	if err := Save(Provider{ID: "orr", Name: "OpenRouter", Key: "k",
		Chat: "https://openrouter.ai/api/v1", Anthropic: "https://openrouter.ai/api"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("orr")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	got := idsOf(ms)
	want := []string{"mistralai/mistral-large-4-0", "openai/gpt-6.1-sol", "anthropic/claude-sonnet-5.5", "anthropic/claude-sonnet-5.5:batch", "anthropic/claude-opus-5"}
	if !slices.Equal(got, want) {
		t.Fatalf("list: %v, want the catalog alone, %v", got, want)
	}
	// the models of the other vendor, the same list by another name, are
	// not in it under a second namespace
	for _, m := range ms {
		if rest, ok := strings.CutPrefix(m.ID, "anthropic/"); ok && !slices.Contains(want, m.ID) {
			t.Errorf("namespaced id in the list: %s (the catalog has %s)", m.ID, rest)
		}
	}
	if n := asked(); n != 0 {
		t.Errorf("asked the Anthropic base %d times, want none", n)
	}
	// the fetch is one request, where before this it asked the Anthropic
	// base as well and merged a page of the catalog this one is
	if n := urls(); n != 1 {
		t.Errorf("asked %d URLs, want the Chat base's one", n)
	}
	t.Logf("asked %d URLs, %d of them with the Anthropic header", urls(), asked())
}

// The same fetch for a vendor of another kind: the union is the two bases'
// lists, and an `anthropic/` id of its own is kept. The rule above is
// OpenRouter's alone.
func TestFetchKeepsAnotherVendorsAnthropicList(t *testing.T) {
	oneHome(t)
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"gpt-a"}]}`))
	}))
	defer chat.Close()
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"claude-b"},{"id":"anthropic/claude-c"}]}`))
	}))
	defer anthropic.Close()

	if err := Save(Provider{ID: "other", Name: "Other", Key: "k",
		Chat: chat.URL + "/v1", Anthropic: anthropic.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("other")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if want := []string{"gpt-a", "claude-b", "anthropic/claude-c"}; !slices.Equal(idsOf(ms), want) {
		t.Fatalf("list: %v, want %v", idsOf(ms), want)
	}
}

// One OpenRouter provider, over two rounds, with the shapes openrouter.ai
// really answered (#904, yetone's review of #1006): the catalog at the Chat
// base, and at the Anthropic base first the whole namespaced catalog and
// then one page of it. Neither the ids of that page nor a pick that fell
// off it belong to what the provider serves.
func TestFetchOpenRouterAddsNoNamespacedModelAndKeepsPicks(t *testing.T) {
	oneHome(t)
	var paged atomic.Bool
	asked, _ := fakeOpenRouterCatalog(t, func(after string) string {
		if paged.Load() {
			return pagedOnceThenAgain
		}
		// the whole namespaced list: both of its pages, as the endpoint
		// answers when asked for them in order
		return openRouterNamespaced(t, after)
	})

	if err := Save(Provider{ID: "orr2", Name: "OpenRouter", Key: "k",
		Chat: "https://openrouter.ai/api/v1", Anthropic: "https://openrouter.ai/api"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("orr2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	p, _ = Find("orr2")
	// a model of the catalog the user picked, which the next list has too
	p.Models = []string{"anthropic/claude-opus-5"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}

	paged.Store(true)
	p, _ = Find("orr2")
	ms, dropped, err := p.Refetch(context.Background())
	if err != nil {
		t.Fatalf("refetch: %v", err)
	}
	if len(dropped) != 0 {
		t.Errorf("dropped %v, want none", dropped)
	}
	p, _ = Find("orr2")
	if !slices.Equal(p.Models, []string{"anthropic/claude-opus-5"}) {
		t.Errorf("picks: %v, want the one that was picked", p.Models)
	}
	for _, m := range ms {
		if rest, ok := strings.CutPrefix(m.ID, "anthropic/"); ok && rest != "" &&
			!slices.Contains([]string{"claude-sonnet-5.5", "claude-sonnet-5.5:batch", "claude-opus-5"}, rest) {
			t.Errorf("namespaced id in the list: %s", m.ID)
		}
	}
	if n := asked(); n != 0 {
		t.Errorf("asked the Anthropic base %d times, want none", n)
	}
}

// The two namespaced pages have to be the endpoint's own, or the tests
// below measure a shape no endpoint has: a fake that answers every cursor
// with the same page makes following a list stop, which is what the rule
// of catalog.Paged was read to rely on here, and hides what the endpoint
// really does — carry on, 413 ids deeper into the namespace, and stop
// there. So the fixture is checked against what it is for: the twenty and
// the 413, the cursor between them, and neither page an id OpenRouter
// serves. The counts are of the day they were taken (2026-10-06) and the
// files under testdata are the replies of that day, so what is asserted is
// the shape and not today's catalog.
func TestOpenRouterNamespacedPagesAreTheEndpointShape(t *testing.T) {
	one := readOpenRouterPage(t, openRouterFile(t, "testdata/openrouter-models-anthropic-1.json"))
	two := readOpenRouterPage(t, openRouterFile(t, "testdata/openrouter-models-anthropic-2.json"))
	// the ids OpenRouter serves, which is what says of the two pages that
	// they name no model of its
	served := map[string]bool{}
	for _, id := range readOpenRouterPage(t, []byte(openRouterCatalog)).data {
		served[id.id] = true
	}

	if len(one.data) != 20 {
		t.Errorf("first page: %d ids, want the endpoint's 20", len(one.data))
	}
	if !one.more {
		t.Error("first page: has_more false, the endpoint answered true")
	}
	if len(one.data) > 0 {
		if one.first != one.data[0].id {
			t.Errorf("first page: first_id %q, want the first id %q", one.first, one.data[0].id)
		}
		if want := one.data[len(one.data)-1].id; one.last != want {
			t.Errorf("first page: last_id %q, want the last id %q", one.last, want)
		}
		if one.last != openRouterNamespacedCursor {
			t.Errorf("first page: last_id %q, want the cursor the tests page on, %q", one.last, openRouterNamespacedCursor)
		}
	}
	if len(two.data) != 413 {
		t.Errorf("page after the cursor: %d ids, want the endpoint's 413", len(two.data))
	}
	if two.more {
		t.Error("page after the cursor: has_more true, the endpoint answered false — the list ends there")
	}
	if one.last == two.last {
		t.Errorf("both pages end at %q, so the second answers the cursor of the first as itself", one.last)
	}
	seen := map[string]bool{}
	for _, p := range []namespacedPage{one, two} {
		for _, m := range p.data {
			if !strings.HasPrefix(m.id, "anthropic/") {
				t.Errorf("id %q is not namespaced, as every id of these pages is", m.id)
			}
			if served[m.id] {
				t.Errorf("id %q is in the catalog OpenRouter serves, so this page is not the namespaced one", m.id)
			}
			if seen[m.id] {
				t.Errorf("id %q is on both pages, and the endpoint's two share nothing", m.id)
			}
			seen[m.id] = true
		}
	}
}

// A page of the namespaced list, as much of it as a test reads: the ids,
// and the cursors that say whether another follows.
type namespacedPage struct {
	data  []struct{ id string }
	more  bool
	first string
	last  string
}

// readOpenRouterPage reads a reply as the endpoint gave it.
func readOpenRouterPage(t *testing.T, raw []byte) namespacedPage {
	t.Helper()
	var wire struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		HasMore bool   `json:"has_more"`
		FirstID string `json:"first_id"`
		LastID  string `json:"last_id"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("%s: %v", raw[:min(len(raw), 40)], err)
	}
	var p namespacedPage
	for _, m := range wire.Data {
		p.data = append(p.data, struct{ id string }{m.ID})
	}
	p.more, p.first, p.last = wire.HasMore, wire.FirstID, wire.LastID
	return p
}

// fakeOpenRouterCatalog answers for openrouter.ai and counts the requests
// that carried the Anthropic version header — the ones whose reply is the
// namespaced catalog — and every URL asked, which is what a fetch of one
// OpenRouter provider costs. namespaced is what to answer with them, given
// the after_id asked for, so a fake of the endpoint's shape pages with it;
// the other replies are the catalog itself, and the URLs FetchAt would
// fall back to answer as openrouter.ai did: 404, then its web page.
func fakeOpenRouterCatalog(t *testing.T, namespaced func(after string) string) (anthropicAsked, urls func() int32) {
	t.Helper()
	var n, hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/api/v1/models":
			if r.Header.Get("anthropic-version") != "" {
				n.Add(1)
				w.Write([]byte(namespaced(r.URL.Query().Get("after_id"))))
				return
			}
			w.Write([]byte(openRouterCatalog))
		case "/api/models":
			w.Write([]byte(`{"error":{"message":"Not Found","code":404}}`))
		default:
			w.Write([]byte("<!DOCTYPE html><html lang=\"en-US\"></html>"))
		}
	}))
	t.Cleanup(srv.Close)
	routeOpenRouterHost(t, srv.Listener.Addr().String())
	return n.Load, hits.Load
}

// routeOpenRouterHost points openrouter.ai at addr and leaves every other host
// to be dialed as written, so a second vendor of a test can be served by its
// own server in the same fetch.
func routeOpenRouterHost(t *testing.T, addr string) {
	t.Helper()
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, host string) (net.Conn, error) {
			if h, _, err := net.SplitHostPort(host); err == nil && strings.EqualFold(h, openRouterHost) {
				return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
			}
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, host)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = tr
	t.Cleanup(func() { http.DefaultClient.Transport = old })
}

// listSrv serves body at /v1/models, as a vendor's own base does.
func vendorListSrv(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The four ways an Anthropic base and a Chat base can sit with respect to
// openrouter.ai, one assertion each. What decides the skip is the host of
// the base being asked, and whether the provider asks at a base of its own
// besides it — never the other base (moPS-42's finding on 0fb16990, where
// reading the Chat base's host skipped an Anthropic base of another
// vendor's: its models were in neither the union nor the sides nor the
// bases that could not be asked, so no path kept them and the user's picks
// of them went for good).
func TestFetchOpenRouterRuleOverTheFourBaseCombinations(t *testing.T) {
	other := func() *httptest.Server { return vendorListSrv(t, `{"data":[{"id":"kimi-k2"}]}`) }
	namespaced := func(after string) string { return openRouterNamespaced(t, after) }

	t.Run("both of OpenRouter's: not asked", func(t *testing.T) {
		oneHome(t)
		asked, _ := fakeOpenRouterCatalog(t, namespaced)
		if err := Save(Provider{ID: "both", Name: "Both", Key: "k",
			Chat: "https://openrouter.ai/api/v1", Anthropic: "https://openrouter.ai/api"}); err != nil {
			t.Fatal(err)
		}
		ms := fetchIDs(t, "both")
		if n := asked(); n != 0 {
			t.Errorf("asked OpenRouter's Anthropic base %d times, want none", n)
		}
		if want := []string{"mistralai/mistral-large-4-0", "openai/gpt-6.1-sol", "anthropic/claude-sonnet-5.5", "anthropic/claude-sonnet-5.5:batch", "anthropic/claude-opus-5"}; !slices.Equal(ms, want) {
			t.Errorf("list %v, want the catalog alone, %v", ms, want)
		}
	})

	t.Run("only an Anthropic base of OpenRouter's: asked, and its answer refused", func(t *testing.T) {
		oneHome(t)
		asked, _ := fakeOpenRouterCatalog(t, namespaced)
		if err := Save(Provider{ID: "alone", Name: "Alone", Key: "k",
			Anthropic: "https://openrouter.ai/api"}); err != nil {
			t.Fatal(err)
		}
		p, err := Find("alone")
		if err != nil {
			t.Fatal(err)
		}
		// It is asked, since it is the only base there is. What it answers
		// with is the namespaced list, which is not a list of models to
		// serve: its pages are followed to the end of it (catalog.Paged) and
		// every id that comes back is refused as a whole, so the fetch fails
		// rather than saving them as the provider's models. Two requests: the
		// page asked as the URL stands, and the one under the cursor it ended
		// at.
		ms, err := p.Fetch(context.Background())
		if err == nil {
			t.Fatalf("the namespaced list was taken as the catalog: %d ids, first %q", len(ms), idsOf(ms)[0])
		}
		// What the user is left with says why the answer is no good and what
		// to do instead — a Chat base of OpenRouter's, or the ids typed in
		// by hand, which is the advice the caller adds to every base it
		// could not ask. It does not count the list it refused: how many ids
		// are namespaced today is of OpenRouter's catalog, not a fact about
		// the base, and the number in a message would be wrong tomorrow.
		for _, want := range []string{"https://openrouter.ai/api/v1", "type its model ids in by hand"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the message does not tell the user what to do (%q missing): %v", want, err)
			}
		}
		// left: the message with the one number it must keep, the version in
		// the Chat base's path
		if rest := strings.ReplaceAll(err.Error(), "https://openrouter.ai/api/v1", ""); strings.ContainsAny(rest, "0123456789") {
			t.Errorf("the message counts something of the day's catalog: %v", err)
		}
		if n := asked(); n != 2 {
			t.Errorf("asked OpenRouter's Anthropic base %d times, want the two pages it answers with", n)
		}
		p, err = Find("alone")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, ok := p.live(); ok {
			t.Errorf("a list was kept for a base whose only answer is the namespaced one: %v", p.Models)
		}
	})

	t.Run("Chat of OpenRouter's, an Anthropic base of another: asked", func(t *testing.T) {
		oneHome(t)
		asked, _ := fakeOpenRouterCatalog(t, namespaced)
		vendor := other()
		if err := Save(Provider{ID: "mix", Name: "Mix", Key: "k",
			Chat: "https://openrouter.ai/api/v1", Anthropic: vendor.URL + "/v1"}); err != nil {
			t.Fatal(err)
		}
		ms := fetchIDs(t, "mix")
		if n := asked(); n != 0 {
			t.Errorf("asked OpenRouter's Anthropic base %d times, want none", n)
		}
		if !slices.Contains(ms, "kimi-k2") {
			t.Errorf("the other vendor's model missing from the union %v", ms)
		}
	})

	t.Run("a Chat base of another, an Anthropic base of OpenRouter's: not asked", func(t *testing.T) {
		oneHome(t)
		asked, _ := fakeOpenRouterCatalog(t, namespaced)
		vendor := other()
		if err := Save(Provider{ID: "swap", Name: "Swap", Key: "k",
			Chat: vendor.URL + "/v1", Anthropic: "https://openrouter.ai/api"}); err != nil {
			t.Fatal(err)
		}
		ms := fetchIDs(t, "swap")
		if n := asked(); n != 0 {
			t.Errorf("asked OpenRouter's Anthropic base %d times, want none", n)
		}
		// the other vendor's models, and nothing of the namespaced page.
		// OpenRouter's catalog is not this provider's: the base that could
		// have given it answers with a page of namespaced ids, of which
		// none is a model it serves, so none was ever there to keep.
		if want := []string{"kimi-k2"}; !slices.Equal(ms, want) {
			t.Errorf("list %v, want %v", ms, want)
		}
	})

	// A Responses base lists models the same way a Chat one does, so it is
	// the same "asked at a base of its own besides it" (fetchOne asks Chat
	// and Responses at one base once between them).
	t.Run("a Responses base of OpenRouter's, and no Chat: not asked", func(t *testing.T) {
		oneHome(t)
		asked, _ := fakeOpenRouterCatalog(t, namespaced)
		if err := Save(Provider{ID: "resp", Name: "Resp", Key: "k",
			Responses: "https://openrouter.ai/api/v1", Anthropic: "https://openrouter.ai/api"}); err != nil {
			t.Fatal(err)
		}
		ms := fetchIDs(t, "resp")
		if n := asked(); n != 0 {
			t.Errorf("asked OpenRouter's Anthropic base %d times, want none", n)
		}
		if want := []string{"mistralai/mistral-large-4-0", "openai/gpt-6.1-sol", "anthropic/claude-sonnet-5.5", "anthropic/claude-sonnet-5.5:batch", "anthropic/claude-opus-5"}; !slices.Equal(ms, want) {
			t.Errorf("list %v, want the catalog alone, %v", ms, want)
		}
	})
}

// fetchIDs is one fetch of a saved provider's list, as ids.
func fetchIDs(t *testing.T, id string) []string {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	p, err = Find(id)
	if err != nil {
		t.Fatal(err)
	}
	live, _, ok := p.live()
	if !ok {
		return nil
	}
	return idsOf(live)
}
