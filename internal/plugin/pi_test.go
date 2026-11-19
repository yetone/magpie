package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
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
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	installPinnedPi(t, ctx)
	abs := filepath.Join(Dir(), "fake-pi-package")
	if err := os.CopyFS(abs, os.DirFS("testdata/pi")); err != nil {
		t.Fatal(err)
	}
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if v := Version(piAgent); v != pinnedPi {
		t.Fatalf("pi is %q after adding the package, not the %s the lock has", v, pinnedPi)
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
	// the extension's import reaches the pi the lock installed, not npm's
	// latest that Bun installs for a folder with no node_modules above it
	// (#2865)
	if s["pi"] != pinnedPi {
		t.Fatalf("the extension's import reached pi %v, not the %s the lock installed", s["pi"], pinnedPi)
	}
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
	// message_start tells the prompt's usage pi's start named, which
	// Claude Code's context meter reads before message_delta (#1887)
	if u := r.events[0]["message"].(map[string]any)["usage"].(map[string]any); u["input_tokens"] != 11.0 || u["output_tokens"] != 0.0 || u["cache_read_input_tokens"] != 3.0 || u["cache_creation_input_tokens"] != 2.0 {
		t.Fatalf("message_start's usage = %v", u)
	}
	// and the prompt pi-ai's anthropic-messages counts only after its
	// start, on the message the events after it share, which goes on being
	// written: its output isn't the start's
	r = piAsk(t, ctx, "fakepi", "fp-plain", map[string]any{"max_tokens": 100, "stream": true, "messages": []map[string]any{{"role": "user", "content": "late"}}})
	if u := r.events[0]["message"].(map[string]any)["usage"].(map[string]any); u["input_tokens"] != 11.0 || u["output_tokens"] != 0.0 || u["cache_read_input_tokens"] != 3.0 || u["cache_creation_input_tokens"] != 2.0 {
		t.Fatalf("message_start's usage, counted after pi's start = %v", u)
	}
	if u := r.events[len(r.events)-2]["usage"].(map[string]any); u["input_tokens"] != 11.0 || u["output_tokens"] != 7.0 || u["cache_read_input_tokens"] != 3.0 || u["cache_creation_input_tokens"] != 2.0 {
		t.Fatalf("message_delta's usage, counted after pi's start = %v", u)
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

// The Plugins page's search, with npmmirror answering (the 「国内镜像」
// switch puts it before npm's own): npmmirror answers a keywords:
// qualifier with nothing, so pi packages were never found there (eachg0
// on X, the Pi market's sibling). Asked again in plain words, they are.
// The answers are npmmirror's own bytes (testdata/npmmirror, 2026-11-19).
func TestSearchFindsPiPackagesOnNPMMirror(t *testing.T) {
	read := func(f string) []byte {
		b, err := os.ReadFile(filepath.Join("testdata", "npmmirror", f))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	fakeNPM(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("text") == "pi-package antigravity" {
			w.Write(read("search-pi-package-antigravity.json"))
			return
		}
		w.Write(read("search-keywords.json"))
	})
	hits, err := Search(context.Background(), "antigravity")
	var names []string
	for _, h := range hits {
		names = append(names, h.Package)
	}
	if err != nil || !slices.Contains(names, "pi-antigravity") {
		t.Fatalf("Search = %v, %v", names, err)
	}
}

func TestPlainKeywords(t *testing.T) {
	for in, want := range map[string]string{
		"keywords:pi-package rename": "pi-package rename",
		"keywords:a,b  x":            "a b x",
		"rename opencode":            "rename opencode",
		"keywords:":                  "keywords:",
	} {
		if got := PlainKeywords(in); got != want {
			t.Errorf("PlainKeywords(%q) = %q, want %q", in, got, want)
		}
	}
}

// pinnedPi is the pi testdata/pi-lock installs. Its bun.lock names every
// package of pi at a version and its npm integrity, so the test runs on
// the same pi each time. Installed as ensurePi installs it, at npm's
// latest, the test ran on whatever pi's packages were that minute: in the
// three minutes between @earendil-works/pi-codemode 1.2.0 (which dropped
// MCP_TYPESCRIPT_PREAMBLE) and pi-coding-agent 1.2.0, latest was
// pi-coding-agent 1.1.0, whose ^1.1.0 took pi-codemode 1.2.0, and pi
// didn't load (#1879). To move to a newer pi, set its version in
// testdata/pi-lock/package.json and run `bun install --ignore-scripts`
// there, with npm's registry, to write the lock again.
const pinnedPi = "1.2.0"

// installPinnedPi installs testdata/pi-lock's pi into the plugins folder
// with its lock frozen, as the folder would have it after ensurePi. A
// test's pi package is put in the plugins folder too, so pi and the
// package's imports of it resolve to that pi: from a folder with no
// node_modules above it, testdata/pi or a TempDir, Bun installs npm's
// latest pi as the host starts (its auto-install), and the host waited
// on npm for over 100 manifests and ran that pi, not this one (#2865).
func installPinnedPi(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"package.json", "bun.lock"} {
		b, err := os.ReadFile(filepath.Join("testdata", "pi-lock", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(Dir(), f), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bun, err := Bun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cmd := bunCommand(ctx, bun, Dir(), "install", "--frozen-lockfile", "--ignore-scripts")
	if cache := pinnedPiCache(time.Now()); cache != "" {
		cmd.Env = append(cmd.Env, "BUN_INSTALL_CACHE_DIR="+cache) // os/exec takes a key's last
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("pi couldn't be installed: %v: %s", err, lastLines(string(out), 6))
	}
	if v := Version(piAgent); v != pinnedPi {
		t.Fatalf("installed pi %q, the lock has %s", v, pinnedPi)
	}
}

// pinnedPiCache is the Bun install cache installPinnedPi installs with at
// now, in the temp folder, which outlives the test's home: a run after the
// first installs pi from it, where the home's own cache started empty each
// run and every run fetched pi's 118 tarballs, most of TestPiPackage's 4
// minutes on a busy machine (#2865). It is this user's (their uid in its
// name, as the temp folder is shared on Linux; Windows' is the user's
// own), and used only when no one else can read or write it and this
// user can write in it; else "", and the home's cache is used as before.
// Never as root: root passes those checks in a folder another user made.
// It is a new folder every two days (pinnedPiCacheDays), so none of its
// files is old enough for the temp folder's cleaner to have taken it
// (macOS' takes what went unread for 3 days): Bun installs a package
// whose cached folder lost a file as it is, and pi didn't load. The
// folder before stays for a run still using it; older ones go. Bun
// unpacks a package in a folder of its own and renames it into the
// cache, so two go test runs can share it. It isn't the app's cache
// (installCacheDir), which pruneInstallCache prunes.
func pinnedPiCache(now time.Time) string {
	if os.Getuid() == 0 {
		return ""
	}
	name := "magpie-test-bun-cache-" + strconv.Itoa(os.Getuid()) + "-"
	n := now.Unix() / int64(pinnedPiCacheDays*24*time.Hour/time.Second)
	old, _ := filepath.Glob(filepath.Join(os.TempDir(), name+"*"))
	for _, o := range old {
		if k, err := strconv.ParseInt(strings.TrimPrefix(filepath.Base(o), name), 10, 64); err == nil && k < n-1 {
			os.RemoveAll(o)
		}
	}
	dir := filepath.Join(os.TempDir(), name+strconv.FormatInt(n, 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() || runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return ""
	}
	f, err := os.CreateTemp(dir, "probe-")
	if err != nil {
		return ""
	}
	f.Close()
	os.Remove(f.Name())
	return dir
}

// pinnedPiCacheDays is how long pinnedPiCache keeps one folder.
const pinnedPiCacheDays = 2

// The pinned pi's cache moves to a new folder every two days, the one
// before kept and older ones taken out, so the temp folder's cleaner
// never takes a file from the one in use (#2865).
func TestPinnedPiCacheMovesOn(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root gets no cache")
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp) // os.TempDir: TMPDIR on Unix, TMP on Windows
	t.Setenv("TMP", tmp)
	day := 24 * time.Hour
	period := int64(pinnedPiCacheDays * 24 * 3600)
	start := time.Unix(1800000000/period*period, 0) // a folder's first second
	a := pinnedPiCache(start)
	if a == "" || filepath.Dir(a) != tmp {
		t.Fatalf("the cache is %q, not a folder in %s", a, tmp)
	}
	if got := pinnedPiCache(start.Add(2*day - time.Second)); got != a {
		t.Fatalf("within its two days the cache moved from %s to %s", a, got)
	}
	b := pinnedPiCache(start.Add(2 * day))
	if b == "" || b == a {
		t.Fatalf("after two days the cache is %q, not a folder other than %s", b, a)
	}
	if _, err := os.Stat(a); err != nil {
		t.Fatalf("the folder before was taken out while a run may use it: %v", err)
	}
	c := pinnedPiCache(start.Add(4 * day))
	if _, err := os.Stat(a); !os.IsNotExist(err) {
		t.Fatalf("a folder four days old is kept (%v)", err)
	}
	if _, err := os.Stat(b); err != nil || c == "" || c == b {
		t.Fatalf("after four days: %q, and the one before %s: %v", c, b, err)
	}
}

// A pi package added without pi has pi installed with it (ensurePi), once;
// with pi there already, and for an OpenCode plugin, nothing is installed.
func TestEnsurePi(t *testing.T) {
	// sandbox's folders, without the Bun on PATH it needs: bun never runs
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAGPIE_BUN", self) // never downloaded
	var ran [][]string
	orig := bunCommand
	bunCommand = func(ctx context.Context, bun, dir string, args ...string) *exec.Cmd {
		ran = append(ran, args)
		if dir != Dir() {
			t.Errorf("bun ran in %s, not the plugins folder", dir)
		}
		// as bun add leaves it; the test binary running no test stands in
		// for bun
		if err := os.MkdirAll(Target(piAgent), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(Target(piAgent), "package.json"), []byte(`{"version":"9.9.9"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		return exec.CommandContext(ctx, self, "-test.run=^$")
	}
	t.Cleanup(func() { bunCommand = orig })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	opencode, _ := filepath.Abs("testdata/fake/index.js")
	if err := ensurePi(ctx, opencode); err != nil || len(ran) != 0 {
		t.Fatalf("an OpenCode plugin: bun ran %q, %v", ran, err)
	}
	pi, _ := filepath.Abs("testdata/pi")
	if err := ensurePi(ctx, pi); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"add", "--ignore-scripts", piAgent}}; !reflect.DeepEqual(ran, want) {
		t.Fatalf("bun ran %q, want %q", ran, want)
	}
	if err := ensurePi(ctx, pi); err != nil || len(ran) != 1 {
		t.Fatalf("with pi installed: bun ran %q, %v", ran, err)
	}
}
