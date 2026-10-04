package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// piReply is what a request to a pi provider answered: its status and
// headers, and its events (streamed) or message.
type piReply struct {
	status int
	header map[string]string
	events []map[string]any
	body   map[string]any
}

func piAsk(t *testing.T, ctx context.Context, provider, model string, req map[string]any) piReply {
	t.Helper()
	req["model"] = model
	b, _ := json.Marshal(req)
	res, err := Fetch(ctx, FetchRequest{
		Provider: provider, Model: model, NPM: "@ai-sdk/anthropic",
		URL: "http://pi.magpie/v1/messages", Method: "POST",
		Headers: map[string]string{"content-type": "application/json"},
		Body:    b,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	r := piReply{status: res.StatusCode, header: map[string]string{}}
	for k := range res.Header {
		r.header[strings.ToLower(k)] = res.Header.Get(k)
	}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		all, _ := io.ReadAll(res.Body)
		if err := json.Unmarshal(all, &r.body); err != nil {
			t.Fatalf("%s: %d %s", model, res.StatusCode, all)
		}
		return r
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			var e map[string]any
			if err := json.Unmarshal([]byte(d), &e); err != nil {
				t.Fatalf("event %q: %v", d, err)
			}
			r.events = append(r.events, e)
		}
	}
	return r
}

// blocks are the content blocks a stream told, as a message has them.
func (r piReply) blocks() []map[string]any {
	var out []map[string]any
	for _, e := range r.events {
		switch e["type"] {
		case "content_block_start":
			out = append(out, e["content_block"].(map[string]any))
		case "content_block_delta":
			b, d := out[int(e["index"].(float64))], e["delta"].(map[string]any)
			switch d["type"] {
			case "text_delta":
				b["text"] = b["text"].(string) + d["text"].(string)
			case "thinking_delta":
				b["thinking"] = b["thinking"].(string) + d["thinking"].(string)
			case "signature_delta":
				b["signature"] = d["signature"]
			case "input_json_delta":
				var in any
				json.Unmarshal([]byte(d["partial_json"].(string)), &in)
				b["input"] = in
			}
		}
	}
	return out
}

// seen is what the fake extension's stream said pi handed it.
func (r piReply) seen(t *testing.T) map[string]any {
	t.Helper()
	bs := r.blocks()
	if r.body != nil {
		for _, b := range r.body["content"].([]any) {
			bs = append(bs, b.(map[string]any))
		}
	}
	for _, b := range bs {
		if b["type"] == "text" {
			var s map[string]any
			if err := json.Unmarshal([]byte(b["text"].(string)), &s); err != nil {
				t.Fatalf("text %q: %v", b["text"], err)
			}
			return s
		}
	}
	t.Fatalf("no text in %+v %+v", r.events, r.body)
	return nil
}

// A pi package (pi coding agent's extensions) is a plugin: pi is
// installed with it, its extensions load with their commands and tools
// left aside, its providers list their models, sign in as pi signs in
// (the OAuth login's questions asked one at a time, the code pasted
// back; a key), renew through magpie, and answer Anthropic's Messages
// from pi's stream: text, thinking with its signature, tool calls and
// their thought signatures back, usage, and a failure's status.
func TestPiPackage(t *testing.T) {
	home, _ := os.UserHomeDir()
	sandbox(t)
	// pi is fetched once into Bun's own cache, not each run's HOME
	if os.Getenv("BUN_INSTALL_CACHE_DIR") == "" && home != "" {
		t.Setenv("BUN_INSTALL_CACHE_DIR", filepath.Join(home, ".bun", "install", "cache"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	abs, _ := filepath.Abs("testdata/pi")
	if _, err := Add(ctx, abs); err != nil {
		if strings.Contains(err.Error(), "bun add") {
			t.Skipf("pi couldn't be installed: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(Dir(), "node_modules", "@earendil-works", "pi-coding-agent", "package.json")); err != nil {
		t.Fatalf("pi wasn't installed with the package: %v", err)
	}
	ls, err := Plugins(ctx)
	if err != nil || len(ls) != 1 || ls[0].Error != "" {
		t.Fatalf("Plugins = %+v, %v", ls, err)
	}
	ps, err := Providers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Provider{}
	for _, p := range ps {
		byID[p.ID] = p
	}
	fp, fk := byID["fakepi"], byID["fakepi-key"]
	if len(ps) != 2 || fp.Name != "FakePi" || fk.Name != "FakePi Key" {
		t.Fatalf("Providers = %+v", ps)
	}
	if len(fp.Methods) != 1 || fp.Methods[0].Type != "oauth" || fp.Methods[0].Label != "Sign in with FakePi Account" {
		t.Fatalf("fakepi's methods = %+v", fp.Methods)
	}
	if len(fk.Methods) != 1 || fk.Methods[0].Type != "api" {
		t.Fatalf("fakepi-key's methods = %+v", fk.Methods)
	}
	models := map[string]Model{}
	for _, m := range fp.Models {
		models[m.ID] = m
	}
	if m := models["fp-think"]; m.NPM != "@ai-sdk/anthropic" || m.Context != 100000 || m.Output != 8000 || !m.Reasoning || !m.Image || !slices.Equal(m.Variants, []string{"low", "high"}) {
		t.Fatalf("fp-think = %+v", m)
	}
	if m := models["fp-plain"]; m.Reasoning || m.Image || m.Context != 1000 {
		t.Fatalf("fp-plain = %+v", m)
	}

	// the login asks how on pi's terminal UI (a list, then a field), its
	// region and its team, shows its page, and waits for the code
	signIn := func(code string) (Saved, error) {
		p, err := NextPrompt(ctx, "fakepi", 0, map[string]string{})
		if err != nil || p == nil || p.Type != "select" || p.Message != "FakePi Login" || len(p.Options) != 2 || p.Options[1].Label != "Your organization" || p.Options[1].Value != "org" || p.Options[1].Hint != "its start URL" {
			t.Fatalf("first prompt = %+v, %v", p, err)
		}
		in := map[string]string{p.Key: "org"}
		p, err = NextPrompt(ctx, "fakepi", 0, in)
		if err != nil || p == nil || p.Type != "text" || p.Message != "Start URL (https://…)" {
			t.Fatalf("the field = %+v, %v", p, err)
		}
		in[p.Key] = "https://acme.invalid/start"
		p, err = NextPrompt(ctx, "fakepi", 0, in)
		if err != nil || p == nil || p.Type != "select" || len(p.Options) != 2 || p.Options[0].Label != "China" || p.Options[0].Value != "cn" {
			t.Fatalf("the region = %+v, %v", p, err)
		}
		in[p.Key] = p.Options[0].Value
		p, err = NextPrompt(ctx, "fakepi", 0, in)
		if err != nil || p == nil || p.Message != "Team?" || p.Placeholder != "blue" {
			t.Fatalf("second prompt = %+v, %v", p, err)
		}
		in[p.Key] = "blue"
		if p, err := NextPrompt(ctx, "fakepi", 0, in); err != nil || p != nil {
			t.Fatalf("asked after the team: %+v, %v", p, err)
		}
		a, err := Authorize(ctx, "fakepi", 0, in, NewAccount)
		if err != nil || a.Method != "code" || a.URL != "https://fakepi.invalid/auth?team=blue" || a.Instructions != "Sign in as blue" {
			t.Fatalf("Authorize = %+v, %v", a, err)
		}
		return Finish(ctx, a.Session, code)
	}
	if _, err := signIn("bad"); !errors.Is(err, ErrFailed) || !strings.Contains(err.Error(), "that code isn't right") {
		t.Fatalf("a bad code: %v", err)
	}
	if got, err := signIn("good"); err != nil || got != (Saved{"fakepi", "fakepi"}) {
		t.Fatalf("Finish = %+v, %v", got, err)
	}
	if b, err := os.ReadFile(filepath.Join(filepath.Dir(AuthPath()), "pi", "stolen")); err == nil {
		t.Fatalf("a program the login started read magpie's messages: %s", b)
	}
	var saved map[string]map[string]any
	b, _ := os.ReadFile(AuthPath())
	json.Unmarshal(b, &saved)
	if a := saved["fakepi"]; a["type"] != "oauth" || a["refresh"] != "r-blue" || a["access"] != "a-blue" || a["team"] != "blue" || a["region"] != "cn" || a["org"] != "https://acme.invalid/start" {
		t.Fatalf("saved %v", saved)
	}
	// pi's auth.json has the sign-in as pi keeps it, and the package, told
	// the session started, reads it back and adds a model
	late := false
	for i := 0; i < 100 && !late; i++ {
		ps, _ := Providers(ctx)
		for _, p := range ps {
			for _, m := range p.Models {
				late = late || p.ID == "fakepi" && m.ID == "fp-blue"
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	var piAuth map[string]map[string]any
	b, _ = os.ReadFile(filepath.Join(filepath.Dir(AuthPath()), "pi", "auth.json"))
	json.Unmarshal(b, &piAuth)
	if a := piAuth["fakepi"]; a["type"] != "oauth" || a["refresh"] != "r-blue" || !late {
		t.Fatalf("pi's auth.json %v; the model added on session_start listed: %v", piAuth, late)
	}
	if got, err := APIKey(ctx, "fakepi-key", 0, nil, "k-123", NewAccount); err != nil || got.Account != "fakepi-key" {
		t.Fatalf("APIKey = %+v, %v", got, err)
	}

	// a request as pi's context, with the account's sign-in, as hard a
	// think as the model can, and the output it can give
	r := piAsk(t, ctx, "fakepi", "fp-think", map[string]any{
		"max_tokens": 99999, "stream": true,
		"system":   []map[string]any{{"type": "text", "text": "be brief"}},
		"thinking": map[string]any{"type": "enabled", "budget_tokens": 30000},
		"tools":    []map[string]any{{"name": "lookup", "description": "looks up", "input_schema": map[string]any{"type": "object"}}},
		"messages": []map[string]any{{"role": "user", "content": []map[string]any{
			{"type": "text", "text": "hello"},
			{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "iVBORw0KGgo="}},
		}}},
	})
	s := r.seen(t)
	if r.status != 200 || s["apiKey"] != "a-blue" || s["reasoning"] != "high" || s["maxTokens"] != 8000.0 || s["system"] != "be brief" ||
		!slices.Equal(anys(s["tools"]), []string{"lookup"}) || !slices.Equal(anys(s["images"]), []string{"image/png"}) {
		t.Fatalf("pi was handed %v (%d)", s, r.status)
	}
	// the empty text the vendor sent isn't told
	if bs := r.blocks(); len(bs) != 1 {
		t.Fatalf("blocks = %+v", bs)
	}
	var types []string
	for _, e := range r.events {
		types = append(types, e["type"].(string))
	}
	if types[0] != "message_start" || types[len(types)-1] != "message_stop" {
		t.Fatalf("events = %v", types)
	}
	md := r.events[len(r.events)-2]
	u := md["usage"].(map[string]any)
	if md["type"] != "message_delta" || md["delta"].(map[string]any)["stop_reason"] != "end_turn" || u["input_tokens"] != 11.0 || u["output_tokens"] != 7.0 || u["cache_read_input_tokens"] != 3.0 || u["cache_creation_input_tokens"] != 2.0 {
		t.Fatalf("message_delta = %v", md)
	}

	// a model that can't think less than "low" is asked to, one that
	// doesn't think isn't
	for model, want := range map[string]any{"fp-think": "low", "fp-plain": nil} {
		r := piAsk(t, ctx, "fakepi", model, map[string]any{"max_tokens": 100, "messages": []map[string]any{{"role": "user", "content": "hi"}}})
		if s := r.seen(t); s["reasoning"] != want {
			t.Fatalf("%s thought %v", model, s["reasoning"])
		}
	}

	// thinking with its signature, and a tool call
	r = piAsk(t, ctx, "fakepi", "fp-think", map[string]any{"max_tokens": 100, "stream": true, "messages": []map[string]any{{"role": "user", "content": "think, then use a tool"}}})
	bs := r.blocks()
	if len(bs) != 2 || bs[0]["type"] != "thinking" || bs[0]["thinking"] != "hmm" || bs[0]["signature"] != "sig-1" ||
		bs[1]["type"] != "tool_use" || bs[1]["id"] != "call_1" || bs[1]["name"] != "lookup" || bs[1]["input"].(map[string]any)["q"] != "x" {
		t.Fatalf("blocks = %+v", bs)
	}
	if md := r.events[len(r.events)-2]; md["delta"].(map[string]any)["stop_reason"] != "tool_use" {
		t.Fatalf("message_delta = %v", md)
	}
	// and back: the signatures go with the turn (the call's kept by
	// magpie, as tool_use has no field for it), the result named
	r = piAsk(t, ctx, "fakepi", "fp-think", map[string]any{"max_tokens": 100, "stream": false, "messages": []map[string]any{
		{"role": "user", "content": "think, then use a tool"},
		{"role": "assistant", "content": []map[string]any{
			{"type": "thinking", "thinking": "hmm", "signature": "sig-1"},
			{"type": "tool_use", "id": "call_1", "name": "lookup", "input": map[string]any{"q": "x"}},
		}},
		{"role": "user", "content": []map[string]any{
			{"type": "tool_result", "tool_use_id": "call_1", "content": "found"},
			{"type": "text", "text": "go on"},
		}},
	}})
	s = r.seen(t)
	if r.status != 200 || r.body["stop_reason"] != "end_turn" ||
		!slices.Equal(anys(s["roles"]), []string{"user", "assistant", "toolResult", "user"}) ||
		!slices.Equal(anys(s["sigs"]), []string{"sig-1", "ts-1"}) || !slices.Equal(anys(s["results"]), []string{"lookup:found"}) {
		t.Fatalf("pi was handed %v; %+v", s, r.body)
	}

	// a failure before anything is said is the vendor's status
	r = piAsk(t, ctx, "fakepi", "fp-think", map[string]any{"max_tokens": 100, "stream": true, "messages": []map[string]any{{"role": "user", "content": "fail429"}}})
	if e, _ := r.body["error"].(map[string]any); r.status != 429 || r.header["retry-after"] != "7" || e["type"] != "rate_limit_error" || !strings.Contains(e["message"].(string), "slow down") {
		t.Fatalf("a 429: %d %v %v", r.status, r.header, r.body)
	}
	// a refusal the package didn't read, with nothing said, is told
	r = piAsk(t, ctx, "fakepi", "fp-plain", map[string]any{"max_tokens": 100, "stream": true, "messages": []map[string]any{{"role": "user", "content": "quota"}}})
	if e, _ := r.body["error"].(map[string]any); r.status != 429 || e["message"] != "exceed quota limit (1005)" {
		t.Fatalf("a refusal sent as JSON: %d %v", r.status, r.body)
	}
	if r := piAsk(t, ctx, "fakepi", "nope", map[string]any{"max_tokens": 100, "messages": []map[string]any{{"role": "user", "content": "hi"}}}); r.status != 404 {
		t.Fatalf("an unknown model: %d %v", r.status, r.body)
	}

	// the key's provider is asked with the key
	if s := piAsk(t, ctx, "fakepi-key", "fk-1", map[string]any{"max_tokens": 100, "messages": []map[string]any{{"role": "user", "content": "hi"}}}).seen(t); s["apiKey"] != "k-123" {
		t.Fatalf("fakepi-key was handed %v", s)
	}

	// a sign-in about to expire is renewed by magpie, with pi's refresh,
	// and kept
	b, _ = os.ReadFile(AuthPath())
	var all map[string]map[string]any
	json.Unmarshal(b, &all)
	all["fakepi"]["expires"] = time.Now().Add(time.Minute).UnixMilli()
	b, _ = json.Marshal(all)
	if err := os.WriteFile(AuthPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if s := piAsk(t, ctx, "fakepi", "fp-plain", map[string]any{"max_tokens": 100, "messages": []map[string]any{{"role": "user", "content": "hi"}}}).seen(t); s["apiKey"] != "renewed-r-blue" {
		t.Fatalf("after the renewal pi was handed %v", s)
	}
	b, _ = os.ReadFile(AuthPath())
	json.Unmarshal(b, &saved)
	if a := saved["fakepi"]; a["access"] != "renewed-r-blue" || a["team"] != "blue" || a["type"] != "oauth" {
		t.Fatalf("the renewal kept %v", a)
	}
}

func anys(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

// The Plugins page's search finds pi packages that bring a provider as
// well as OpenCode plugins, and not pi packages that only add pi a
// command or a guard.
func TestSearchFindsPiPackages(t *testing.T) {
	fakeNPM(t, func(w http.ResponseWriter, r *http.Request) {
		obj := func(name string, kw ...string) string {
			k, _ := json.Marshal(kw)
			return `{"package":{"name":"` + name + `","version":"1.0.0","keywords":` + string(k) + `}}`
		}
		var objs []string
		if q := r.URL.Query().Get("text"); strings.HasPrefix(q, "keywords:pi-package ") {
			objs = []string{obj("pi-antigravity", "pi-package", "oauth", "provider"), obj("pi-antigravity-guard", "pi-package", "antigravity"), obj("opencode-antigravity-auth", "opencode")}
		} else {
			objs = []string{obj("opencode-antigravity-auth", "opencode", "plugin"), obj("antigravity-cli")}
		}
		w.Write([]byte(`{"objects":[` + strings.Join(objs, ",") + `]}`))
	})
	hits, err := Search(context.Background(), "antigravity")
	var names []string
	for _, h := range hits {
		names = append(names, h.Package)
	}
	if err != nil || !slices.Equal(names, []string{"opencode-antigravity-auth", "pi-antigravity"}) {
		t.Fatalf("Search = %v, %v", names, err)
	}
}
