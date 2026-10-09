package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/settings"
)

func detectHome(t *testing.T) {
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

// asked is one request a fake relay was sent.
type asked struct {
	method, path, auth, model string
	body                      map[string]any
}

// relay serves Chat Completions and Anthropic Messages under /v1, not
// Responses, and lists a GPT and a Claude model.
func relay(t *testing.T) (*httptest.Server, func() []asked) {
	var mu sync.Mutex
	var got []asked
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(b, &body)
		m, _ := body["model"].(string)
		mu.Lock()
		got = append(got, asked{r.Method, r.URL.Path, r.Header.Get("Authorization") + r.Header.Get("x-api-key") + r.Header.Get("x-goog-api-key"), m, body})
		mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/models":
			io.WriteString(w, `{"data":[{"id":"gpt-image-2"},{"id":"gpt-5.5"},{"id":"claude-sonnet-5"}]}`)
		case "POST /v1/chat/completions":
			io.WriteString(w, `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"length"}]}`)
		case "POST /v1/messages":
			io.WriteString(w, `{"id":"m","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"max_tokens"}`)
		default:
			http.Error(w, `{"error":{"message":"Invalid URL (POST `+r.URL.Path+`)"}}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []asked {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got)
	}
}

// One URL typed, one click: each API magpie speaks upstream is sent the
// smallest request at that URL as it takes it, and what answered says
// which the relay serves (01huadalang on Discord). A model is picked from
// the vendor's list for each, which is asked for first, and kept nowhere.
func TestDetectProtocols(t *testing.T) {
	detectHome(t)
	srv, got := relay(t)
	for _, typed := range []string{srv.URL, srv.URL + "/v1/", srv.URL + "/v1/chat/completions"} {
		before := len(got())
		res, err := Provider{Key: "sk-relay"}.Detect(context.Background(), typed, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(res) != 4 {
			t.Fatalf("%s: %+v", typed, res)
		}
		want := []struct {
			proto Protocol
			ok    bool
			base  string
			model string
		}{
			{Chat, true, srv.URL + "/v1", "gpt-5.5"},
			{Responses, false, srv.URL + "/v1", "gpt-5.5"},
			{Anthropic, true, srv.URL, "claude-sonnet-5"},
			// no Gemini model listed: the first that chats, at the
			// …/v1beta beside the …/v1, where this relay serves none
			{Gemini, false, srv.URL + "/v1beta", "gpt-5.5"},
		}
		for i, w := range want {
			r := res[i]
			if r.Protocol != w.proto || r.OK != w.ok || r.Base != w.base || r.Model != w.model {
				t.Fatalf("%s: %s = %+v, want %+v", typed, w.proto, r, w)
			}
		}
		if res[1].Status != 404 || !strings.Contains(res[1].Error, "Invalid URL") {
			t.Fatalf("%s: responses said %+v", typed, res[1])
		}
		// the list, then one request an API, each the smallest, with the key
		var posts []string
		for _, a := range got()[before:] {
			if a.auth == "" || !strings.Contains(a.auth, "sk-relay") {
				t.Fatalf("%s %s went without the key", a.method, a.path)
			}
			if a.method == "GET" {
				continue
			}
			posts = append(posts, a.path)
			for _, k := range []string{"max_tokens", "max_output_tokens"} {
				if n, ok := a.body[k].(float64); ok && n > 16 {
					t.Fatalf("%s asked for %v tokens", a.path, n)
				}
			}
			if a.body["stream"] == true {
				t.Fatalf("%s streamed", a.path)
			}
		}
		slices.Sort(posts)
		if !slices.Equal(posts, []string{"/v1/chat/completions", "/v1/messages", "/v1/responses", "/v1beta/models/gpt-5.5:generateContent"}) {
			t.Fatalf("%s: asked %v", typed, posts)
		}
	}
	// nothing was kept: no provider, no model list
	if ps := All(); len(ps) != 0 {
		t.Fatalf("detecting saved %+v", ps)
	}
}

// Some Responses relays reject the string shorthand and require message arrays.
func TestResponsesProbesUseArrayInput(t *testing.T) {
	detectHome(t)
	const model = "gpt-test"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/models":
			io.WriteString(w, `{"data":[{"id":"`+model+`"}]}`)
		case "POST /v1/responses":
			var in struct {
				Model string `json:"model"`
				Input []struct {
					Type    string `json:"type"`
					Role    string `json:"role"`
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				http.Error(w, `{"error":{"message":"input must be an array"}}`, http.StatusBadRequest)
				return
			}
			if in.Model != model || len(in.Input) != 1 || in.Input[0].Type != "message" ||
				in.Input[0].Role != "user" || len(in.Input[0].Content) != 1 ||
				in.Input[0].Content[0].Type != "input_text" || in.Input[0].Content[0].Text != "hi" {
				http.Error(w, `{"error":{"message":"expected a user text message"}}`, http.StatusBadRequest)
				return
			}
			io.WriteString(w, `{"object":"response","status":"completed","output":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := Provider{ID: "relay", Key: "sk-relay", Responses: srv.URL + "/v1", Models: []string{model}}
	ctx := context.Background()
	check := func(t *testing.T, r Result) {
		t.Helper()
		if !r.OK || r.Status != http.StatusOK || r.Protocol != Responses || r.Model != model {
			t.Fatalf("Responses probe: %+v", r)
		}
	}
	t.Run("Detect", func(t *testing.T) {
		rs, err := p.Detect(ctx, srv.URL, model)
		if err != nil || len(rs) != len(detectProtocols) {
			t.Fatalf("Detect: %v, %+v", err, rs)
		}
		check(t, rs[1].Result)
	})
	t.Run("DetectModels", func(t *testing.T) {
		each, sum, err := p.DetectModels(ctx, srv.URL, []string{model})
		if err != nil || len(each) != 1 || len(each[0].Results) != len(detectProtocols) || len(sum) != len(detectProtocols) {
			t.Fatalf("DetectModels: %v, %+v, %+v", err, each, sum)
		}
		check(t, each[0].Results[1].Result)
		check(t, sum[1].Result)
	})
	t.Run("Test", func(t *testing.T) {
		rs := p.Test(ctx)
		if len(rs) != 1 {
			t.Fatalf("Test: %+v", rs)
		}
		check(t, rs[0])
	})
	t.Run("TestModels", func(t *testing.T) {
		rs := p.TestModels(ctx, []string{model})
		if len(rs) != 1 {
			t.Fatalf("TestModels: %+v", rs)
		}
		check(t, rs[0])
	})
}

// A model typed is the one asked on all three, and the vendor's list isn't
// asked for; a URL the provider has for an API is asked rather than the
// one typed; no URL at all says so.
func TestDetectModelAndOwnURLs(t *testing.T) {
	detectHome(t)
	srv, got := relay(t)
	other, gotOther := relay(t)
	res, err := Provider{Key: "k", Anthropic: other.URL}.Detect(context.Background(), srv.URL, "claude-sonnet-5")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Model != "claude-sonnet-5" {
			t.Fatalf("%s asked for %s", r.Protocol, r.Model)
		}
	}
	if res[2].Base != other.URL || !res[2].OK {
		t.Fatalf("anthropic: %+v", res[2])
	}
	for _, a := range got() {
		if a.method == "GET" || a.path == "/v1/messages" {
			t.Fatalf("asked %s %s at the typed URL", a.method, a.path)
		}
	}
	if as := gotOther(); len(as) != 1 || as[0].path != "/v1/messages" {
		t.Fatalf("the provider's own Anthropic URL was asked %+v", as)
	}
	if _, err := (Provider{Key: "k"}).Detect(context.Background(), " ", ""); err != ErrNoURL {
		t.Fatalf("no URL: %v", err)
	}
	if _, err := (Provider{Key: "k"}).Detect(context.Background(), "relay.example.com/v1", ""); err == nil {
		t.Fatal("a URL with no scheme was asked")
	}
}

// A relay whose one key serves some models on one API and others on
// another: the API the user picks for a model is the only one it is asked
// on — by the gateway (through APIs and Native) and by a model's test —
// and must be one the provider has a URL for.
func TestModelAPI(t *testing.T) {
	detectHome(t)
	srv, got := relay(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k", Chat: srv.URL + "/v1", Anthropic: srv.URL, Models: []string{"mixed", "plain"}}); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if apis := p.APIs("mixed"); apis != nil {
		t.Fatalf("before: %v", apis)
	}
	if n := p.Native("mixed"); n != Chat {
		t.Fatalf("before: native %s", n)
	}
	if err := SetModelAPI("relay/mixed", "responses"); err == nil || !strings.Contains(err.Error(), "no responses URL") {
		t.Fatalf("an API with no URL: %v", err)
	}
	if err := SetModelAPI("relay/mixed", "gemini"); err == nil {
		t.Fatal("gemini was taken")
	}
	if err := SetModelAPI("relay/nope", "anthropic"); err == nil {
		t.Fatal("a model it hasn't was given an API")
	}
	api := "anthropic"
	if err := SetModelPrefs("relay", map[string]ModelPref{"mixed": {API: &api}}); err != nil {
		t.Fatal(err)
	}
	if settings.Load().ModelAPIs["relay/mixed"] != "anthropic" {
		t.Fatalf("kept %v", settings.Load().ModelAPIs)
	}
	p, _ = Find("relay")
	if apis := p.APIs("mixed"); !slices.Equal(apis, []Protocol{Anthropic}) {
		t.Fatalf("apis %v", apis)
	}
	if n := p.Native("mixed"); n != Anthropic {
		t.Fatalf("native %s", n)
	}
	if apis := p.APIs("plain"); apis != nil {
		t.Fatalf("the other model followed: %v", apis)
	}
	if got, ok := p.ModelAPI("mixed"); !ok || got != Anthropic {
		t.Fatalf("ModelAPI %v %v", got, ok)
	}
	before := len(got())
	if r := p.TestModels(context.Background(), []string{"mixed"}); !r[0].OK || r[0].Protocol != Anthropic {
		t.Fatalf("test %+v", r)
	}
	if as := got()[before:]; len(as) != 1 || as[0].path != "/v1/messages" {
		t.Fatalf("the test asked %+v", as)
	}
	// its Anthropic URL gone, the model is asked where the provider can
	q := *p
	q.Anthropic = ""
	if apis := q.APIs("mixed"); apis != nil {
		t.Fatalf("with no Anthropic URL: %v", apis)
	}
	// "" gives it back to the list
	none := ""
	if err := SetModelPrefs("relay", map[string]ModelPref{"mixed": {API: &none}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := settings.Load().ModelAPIs["relay/mixed"]; ok {
		t.Fatal("still kept")
	}
	if p, _ = Find("relay"); p.APIs("mixed") != nil {
		t.Fatal("still anthropic")
	}
}

// A relay serving each model on some APIs only: Detect asked for several
// models says, model by model, which API answered it (01huadalang on
// Discord: 有的仅支持 response 有的双协议), a few at a time and no more
// than DetectMax of them; by API, the first model it served.
func TestDetectModels(t *testing.T) {
	detectHome(t)
	serves := map[string][]string{
		"/v1/chat/completions": {"gpt-both", "glm-chat"},
		"/v1/responses":        {"gpt-both", "gpt-resp"},
		"/v1/messages":         {"claude-only"},
	}
	var mu sync.Mutex
	var n, most, asked int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		asked++
		most = max(most, n)
		mu.Unlock()
		defer func() { mu.Lock(); n--; mu.Unlock() }()
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		m, _ := body["model"].(string)
		if !slices.Contains(serves[r.URL.Path], m) {
			http.Error(w, `{"error":{"message":"model `+m+` not supported on this endpoint"}}`, http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/v1/chat/completions":
			io.WriteString(w, `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"length"}]}`)
		case "/v1/responses":
			io.WriteString(w, `{"id":"r","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}`)
		case "/v1/messages":
			io.WriteString(w, `{"id":"m","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"max_tokens"}`)
		}
	}))
	t.Cleanup(srv.Close)
	models := []string{"gpt-both", "gpt-resp", " glm-chat", "claude-only", "gpt-both", "gpt-image-2", ""}
	each, sum, err := Provider{Key: "k"}.DetectModels(context.Background(), srv.URL+"/v1", models)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][4]bool{ // chat, responses, anthropic, gemini
		"gpt-both":    {true, true, false, false},
		"gpt-resp":    {false, true, false, false},
		"glm-chat":    {true, false, false, false},
		"claude-only": {false, false, true, false},
		"gpt-image-2": {false, false, false, false},
	}
	if len(each) != len(want) {
		t.Fatalf("asked %d models: %+v", len(each), each)
	}
	for _, md := range each {
		w, ok := want[md.Model]
		if !ok || len(md.Results) != 4 {
			t.Fatalf("%+v", md)
		}
		for i, r := range md.Results {
			if r.OK != w[i] || r.Model != md.Model || r.Protocol != detectProtocols[i] {
				t.Fatalf("%s on %s: %+v, want ok=%v", md.Model, detectProtocols[i], r, w[i])
			}
			if !r.OK && md.Model != "gpt-image-2" && (r.Status != 400 || !strings.Contains(r.Error, "not supported")) {
				t.Fatalf("%s on %s said %+v", md.Model, r.Protocol, r)
			}
		}
	}
	if each[len(each)-1].Results[0].Error == "" {
		t.Fatal("an image model was asked on chat")
	}
	if asked != 16 {
		t.Fatalf("%d requests for 4 models on 4 APIs", asked)
	}
	for i, w := range []struct {
		model, base string
	}{{"gpt-both", srv.URL + "/v1"}, {"gpt-both", srv.URL + "/v1"}, {"claude-only", srv.URL}} {
		if !sum[i].OK || sum[i].Model != w.model || sum[i].Base != w.base {
			t.Fatalf("by API %s: %+v", detectProtocols[i], sum[i])
		}
	}

	// many models: no more than DetectMax asked, DetectWide at a time
	mu.Lock()
	asked, most = 0, 0
	mu.Unlock()
	var many []string
	for i := range DetectMax + 5 {
		many = append(many, "m"+strings.Repeat("x", i))
	}
	each, _, err = Provider{Key: "k"}.DetectModels(context.Background(), srv.URL+"/v1", many)
	if err != nil {
		t.Fatal(err)
	}
	if len(each) != DetectMax || asked != 4*DetectMax || most > DetectWide {
		t.Fatalf("%d models, %d requests, %d at once", len(each), asked, most)
	}
	if _, _, err := (Provider{Key: "k"}).DetectModels(context.Background(), srv.URL, []string{" "}); err == nil {
		t.Fatal("no model was taken")
	}
	if ps := All(); len(ps) != 0 {
		t.Fatalf("detecting saved %+v", ps)
	}
}

// A probe not started before the detection's time is up isn't sent.
func TestDetectModelsOutOfTime(t *testing.T) {
	detectHome(t)
	srv, got := relay(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	each, _, err := Provider{Key: "k"}.DetectModels(ctx, srv.URL, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	for _, md := range each {
		for _, r := range md.Results {
			if r.OK || r.Error != "not asked: out of time" {
				t.Fatalf("%+v", r)
			}
		}
	}
	if n := len(got()); n != 0 {
		t.Fatalf("%d sent past the time", n)
	}
}

// A Gemini API is detected too (#1346, NagaseMinato: 「检测」不会探测 Gemini
// 端点，为什么呢？): typed as the relay's …/v1, the bare host, Google's
// …/v1beta or a generateContent URL pasted whole, it is asked at the
// …/v1beta, a Gemini model from its own list, the key in x-goog-api-key, the
// smallest request generateContent takes; a provider's own Gemini URL is
// asked as it is.
func TestDetectGemini(t *testing.T) {
	detectHome(t)
	var mu sync.Mutex
	var gem []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1beta/models" && r.Method == "GET":
			if r.Header.Get("x-goog-api-key") != "AIza-k" {
				http.Error(w, `{"error":{"code":401,"message":"API key not valid","status":"UNAUTHENTICATED"}}`, http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `{"models":[{"name":"models/text-embedding-004","supportedGenerationMethods":["embedContent"]},{"name":"models/gemini-2.5-flash","supportedGenerationMethods":["generateContent","countTokens"]}]}`)
		case strings.HasPrefix(r.URL.Path, "/v1beta/models/") && r.Method == "POST":
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			gem = append(gem, r.URL.Path+" "+r.Header.Get("x-goog-api-key")+" "+r.Header.Get("Authorization")+" "+string(b))
			mu.Unlock()
			if r.Header.Get("x-goog-api-key") != "AIza-k" {
				http.Error(w, `{"error":{"code":401,"message":"API key not valid","status":"UNAUTHENTICATED"}}`, http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"Hi"}],"role":"model"},"finishReason":"MAX_TOKENS","index":0}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2},"modelVersion":"gemini-2.5-flash"}`)
		default:
			http.Error(w, `{"error":{"code":404,"message":"Not Found","status":"NOT_FOUND"}}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	for _, typed := range []string{srv.URL + "/v1", srv.URL, srv.URL + "/v1beta", srv.URL + "/v1beta/models/gemini-2.5-flash:generateContent"} {
		mu.Lock()
		gem = nil
		mu.Unlock()
		res, err := Provider{Key: "AIza-k"}.Detect(context.Background(), typed, "")
		if err != nil {
			t.Fatal(err)
		}
		g := res[len(res)-1]
		if g.Protocol != Gemini || !g.OK || g.Base != srv.URL+"/v1beta" || g.Model != "gemini-2.5-flash" {
			t.Fatalf("%s: gemini = %+v", typed, g)
		}
		mu.Lock()
		if len(gem) != 1 || gem[0] != `/v1beta/models/gemini-2.5-flash:generateContent AIza-k  {"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":16}}` {
			t.Fatalf("%s: asked %q", typed, gem)
		}
		mu.Unlock()
		for _, r := range res[:len(res)-1] {
			if r.OK {
				t.Fatalf("%s: %s answered on a Gemini-only relay: %+v", typed, r.Protocol, r)
			}
		}
	}

	// the provider's own Gemini URL, and a model typed, asked as they are
	res, err := Provider{Key: "AIza-k", Gemini: srv.URL + "/v1beta"}.Detect(context.Background(), "https://elsewhere.invalid/v1", "gemini-2.5-flash")
	if err != nil {
		t.Fatal(err)
	}
	if g := res[3]; !g.OK || g.Base != srv.URL+"/v1beta" {
		t.Fatalf("own URL: %+v", g)
	}

	// each model on each API: Gemini's the fourth
	each, sum, err := Provider{Key: "AIza-k"}.DetectModels(context.Background(), srv.URL+"/v1beta", []string{"gemini-2.5-flash"})
	if err != nil {
		t.Fatal(err)
	}
	if len(each) != 1 || len(each[0].Results) != 4 || !each[0].Results[3].OK || !sum[3].OK || sum[3].Base != srv.URL+"/v1beta" {
		t.Fatalf("each %+v, sum %+v", each, sum)
	}
	if ps := All(); len(ps) != 0 {
		t.Fatalf("detecting saved %+v", ps)
	}
}
