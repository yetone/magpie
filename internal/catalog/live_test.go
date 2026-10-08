package catalog

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEndpointAPIs(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{"/messages"}, "anthropic"},
		{[]string{"/v1/messages", "/chat/completions", "ws:/responses", "/responses"}, "anthropic,chat,responses"},
		{[]string{"/v1/chat/completions/", "/chat/completions"}, "chat"},
		{[]string{"/embeddings"}, ""},
		{nil, ""},
	} {
		if got := strings.Join(EndpointAPIs(c.in), ","); got != c.want {
			t.Errorf("%v: %q", c.in, got)
		}
	}
}

// A list that tells each model's context window (Command Code, OpenRouter)
// has it kept; an odd value is only left out.
func TestFetchContextLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Write([]byte(`{"object":"list","data":[{"id":"claude-sonnet-5","name":"Claude Sonnet 5","context_length":1000000,"supported_endpoints":["/messages"]},{"id":"kimi-k3","context_length":"lots"},{"id":"glm-5"}]}`))
	}))
	defer srv.Close()
	ms, _, err := FetchAt(context.Background(), srv.URL+"/v1", "k", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, m := range ms {
		got[m.ID] = m.Context
	}
	if len(got) != 3 || got["claude-sonnet-5"] != 1000000 || got["kimi-k3"] != 0 || got["glm-5"] != 0 {
		t.Errorf("contexts %v", got)
	}
}

// Another magpie's list tells each model's native APIs, over the ones it
// serves it on, its longest reply and its reasoning levels; a vendor's odd
// values are only left out, never the whole list.
func TestFetchMagpieFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Write([]byte(`{"object":"list","data":[
		  {"id":"anthropic/claude-opus-4-8","native_endpoints":["/v1/messages"],"max_output_tokens":128000,"supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]},
		  {"id":"group/fast","supported_reasoning_levels":[]},
		  {"id":"codex/gpt-6","supported_endpoints":["/responses","/chat/completions"],"supported_reasoning_levels":["low","medium"]},
		  {"id":"odd","max_output_tokens":"lots","supported_reasoning_levels":{"low":true}}]}`))
	}))
	defer srv.Close()
	ms, _, err := FetchAt(context.Background(), srv.URL+"/v1", "k", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range ms {
		got[m.ID] = fmt.Sprint(m.APIs, m.Output, m.Efforts)
	}
	want := map[string]string{
		"anthropic/claude-opus-4-8": "[anthropic] 128000 [low high]",
		"group/fast":                "[] 0 []",
		"codex/gpt-6":               "[responses chat] 0 [low medium]",
		"odd":                       "[] 0 []",
	}
	if !maps.Equal(got, want) {
		t.Errorf("got %v", got)
	}
}

// A base with a version in its path (Ark's /api/plan/v3, tcdw's report) is
// asked for its list only as written: no /v1 is put on it, and a failure
// names the URL asked and what the vendor said. A base without one is
// still looked around for its /v1.
func TestFetchAtVersionedBase(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		if r.URL.Path == "/v1/models" || r.URL.Path == "/api/plan/v3/v1/models" {
			rw.Write([]byte(`{"data":[{"id":"doubao-seed-2.1-pro"}]}`))
			return
		}
		rw.WriteHeader(http.StatusUnauthorized)
		rw.Write([]byte(`{"error":{"code":"AuthenticationError","message":"The API key doesn't exist.","type":"Unauthorized"}}`))
	}))
	defer srv.Close()

	_, _, err := FetchAt(context.Background(), srv.URL+"/api/plan/v3/", "k", false, nil)
	if err == nil {
		t.Fatal("listed from under a /v1 the base doesn't have")
	}
	if strings.Join(asked, " ") != "/api/plan/v3/models" {
		t.Errorf("asked %v", asked)
	}
	if msg := err.Error(); !strings.Contains(msg, srv.URL+"/api/plan/v3/models: 401") || !strings.Contains(msg, "The API key doesn't exist.") || strings.Contains(msg, "/v3/v1/") {
		t.Errorf("error: %s", msg)
	}

	if _, at, err := FetchAt(context.Background(), srv.URL, "k", false, nil); err != nil || at != srv.URL+"/v1/models" {
		t.Errorf("bare host: %q %v", at, err)
	}
	// an Anthropic base is the root its /v1 goes after
	if _, at, err := FetchAt(context.Background(), srv.URL+"/api/plan/v3", "k", true, nil); err != nil || at != srv.URL+"/api/plan/v3/v1/models" {
		t.Errorf("anthropic: %q %v", at, err)
	}

	for u, want := range map[string]bool{
		"https://ark.cn-beijing.volces.com/api/plan/v3":           true,
		"https://open.bigmodel.cn/api/paas/v4":                    true,
		"https://generativelanguage.googleapis.com/v1beta/openai": true,
		"https://api.openai.com/v1":                               true,
		"https://openrouter.ai/api":                               false,
		"https://relay.example.com":                               false,
		"https://api.stepfun.ai/step_plan":                        false,
		"https://vault.example.com/v":                             false,
	} {
		if Versioned(u) != want {
			t.Errorf("Versioned(%q) = %v", u, !want)
		}
	}
}

// Command Code supplies distinct names for its Flash and Flash Fast models.
func TestFetchCommandCodeNames(t *testing.T) {
	writeCatalog(t, `{
	  "coralbricks":{"models":{
	    "deepseek-v4.1-flash":{"id":"deepseek-v4.1-flash","name":"DeepSeek V4.1 Flash"},
	    "deepseek-v4.1-flash-fast":{"id":"deepseek-v4.1-flash-fast","name":"DeepSeek V4.1 Flash"}}},
	  "vercel":{"models":{
	    "deepseek-v4.1-flash-fast":{"id":"deepseek-v4.1-flash-fast","name":"DeepSeek V4.1 Flash Fast"}}}
	}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"object":"list","data":[
		  {"id":"deepseek/deepseek-v4.1-flash","object":"model","created":1791381046,"owned_by":"command-code","name":"DeepSeek V4.1 Flash","context_length":1000000,"supported_endpoints":["/chat/completions","/responses"]},
		  {"id":"deepseek/deepseek-v4.1-flash-fast","object":"model","created":1791381046,"owned_by":"command-code","name":"DeepSeek V4.1 Flash Fast","context_length":1000000,"supported_endpoints":["/chat/completions","/responses"]}]}`))
	}))
	defer srv.Close()
	ms, err := Fetch(context.Background(), srv.URL+"/v1", "", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 {
		t.Fatalf("models: %+v", ms)
	}
	for i, m := range Named(Decorate(ms, Provider("coralbricks"))) {
		wantID, wantName := "deepseek/deepseek-v4.1-flash", "DeepSeek V4.1 Flash"
		if i == 1 {
			wantID += "-fast"
			wantName += " Fast"
		}
		if m.ID != wantID || m.Name != wantName {
			t.Errorf("model %d: id %q, name %q; want %q, %q", i, m.ID, m.Name, wantID, wantName)
		}
		if m.Context != 1000000 || strings.Join(m.APIs, ",") != "chat,responses" {
			t.Errorf("model facts lost: %+v", m)
		}
	}
}

func TestFetchNamePrecedence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[
		  {"id":"all","magpie_label":"label","display_name":"display","name":"name"},
		  {"id":"display","magpie_label":"","display_name":"display name","name":"name"},
		  {"id":"name","display_name":"","name":"provider name"},
		  {"id":"id","name":""},
		  {"id":"prefixed","name":"Vendor: Model"},
		  {"id":"prefixed-display","display_name":"Vendor: Display","name":"Vendor: Name"},
		  {"id":"prefixed-label","magpie_label":"Vendor: Label","name":"Vendor: Name"},
		  {"id":"variant","name":"Model:free"},
		  {"name":"name-only"},
		  {"display_name":"no identifier"},
		  {}]}`))
	}))
	defer srv.Close()
	ms, err := Fetch(context.Background(), srv.URL+"/v1", "", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range ms {
		got = append(got, m.ID+"|"+m.Name)
	}
	want := "all|label\ndisplay|display name\nname|provider name\nid|id\nprefixed|prefixed\nprefixed-display|Vendor: Display\nprefixed-label|Vendor: Label\nvariant|Model:free\nname-only|name-only"
	if strings.Join(got, "\n") != want {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), want)
	}
}

// OpenRouter's real /api/v1/models shape has vendor-prefixed names and no
// display_name. Those names still come from its catalog, without the prefix.
func TestFetchOpenRouterNames(t *testing.T) {
	writeCatalog(t, `{"openrouter":{"models":{
	  "anthropic/claude-haiku-5.5":{"id":"anthropic/claude-haiku-5.5","name":"Claude Haiku 5.5"},
	  "google/gemini-nano-banana-2.1":{"id":"google/gemini-nano-banana-2.1","name":"Nano Banana 2.1"}
	}}}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fields from https://openrouter.ai/api/v1/models, 2026-10-08.
		w.Write([]byte(`{"data":[
		  {"id":"anthropic/claude-haiku-5.5","canonical_slug":"anthropic/claude-haiku-5.5-20261007","name":"Anthropic: Claude Haiku 5.5","created":1791397883,"context_length":1000000,"architecture":{"modality":"text+image+file->text","input_modalities":["text","image","file"],"output_modalities":["text"],"tokenizer":"Claude","instruct_type":null},"top_provider":{"context_length":1000000,"max_completion_tokens":128000,"is_moderated":true}},
		  {"id":"google/gemini-nano-banana-2.1","canonical_slug":"google/gemini-nano-banana-2.1-20261006","name":"Google: Nano Banana 2.1","created":1791300825,"context_length":65536,"architecture":{"modality":"text+image->text+image","input_modalities":["image","text"],"output_modalities":["image","text"],"tokenizer":"Gemini","instruct_type":null},"top_provider":{"context_length":65536,"max_completion_tokens":58982,"is_moderated":false}}]}`))
	}))
	defer srv.Close()
	ms, err := Fetch(context.Background(), srv.URL+"/api/v1", "", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 {
		t.Fatalf("models: %+v", ms)
	}
	for _, m := range ms {
		if m.Name != m.ID {
			t.Errorf("prefixed name kept for %q: %q", m.ID, m.Name)
		}
	}
	wantNames := []string{"Claude Haiku 5.5", "Nano Banana 2.1"}
	wantContexts := []int{1000000, 65536}
	for i, m := range Named(Decorate(ms, Provider("openrouter"))) {
		if m.ID != ms[i].ID || m.Name != wantNames[i] || m.Context != wantContexts[i] {
			t.Errorf("model %d: %+v; want id %q, name %q, context %d", i, m, ms[i].ID, wantNames[i], wantContexts[i])
		}
	}
}
