package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

func TestParseGrokModels(t *testing.T) {
	b := []byte(`{"object":"list","data":[
		{"id":"grok-4.7","name":"Grok 4.7","context_window":500000,"api_backend":"responses","reasoning_efforts":[{"value":"xhigh"},{"value":"high"},{"value":"medium"},{"value":"low"}]},
		{"id":"grok-4.7-build-fast","context_window":256000,"api_backend":"responses"},
		{"id":"grok-old","api_backend":"chat_completions"}]}`)
	ms := parseGrokModels(b)
	if len(ms) != 2 || ms[0].ID != "grok-4.7" || ms[0].Name != "Grok 4.7" || ms[0].Context != 500000 ||
		strings.Join(ms[0].Efforts, ",") != "low,medium,high,xhigh" || ms[1].Name != "grok-4.7-build-fast" || ms[1].Efforts != nil {
		t.Fatalf("models = %+v", ms)
	}
}

// A request is signed with the sign-in of its account's home, as the CLI
// signs its own.
func TestGrokSigns(t *testing.T) {
	var got http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Write([]byte(`{"data":[{"id":"grok-4.7","api_backend":"responses"}]}`))
	}))
	defer up.Close()
	base := GrokBase
	GrokBase = up.URL
	defer func() { GrokBase = base }()
	home := t.TempDir()
	grokSignedIn(t, home, "me@x.ai")
	acct := &Account{Agent: "grok"}
	grokSigned(acct, home)
	req, _ := http.NewRequest("POST", up.URL+"/responses", nil)
	if err := acct.sign(context.Background(), req, []byte(`{"model":"grok-4.7","prompt_cache_key":"c1"}`)); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "Bearer k-me@x.ai" || req.Header.Get("x-grok-client-version") == "" ||
		req.Header.Get("x-grok-model-override") != "grok-4.7" || req.Header.Get("x-grok-conv-id") != "c1" ||
		!strings.HasPrefix(req.Header.Get("User-Agent"), "grok-shell/") {
		t.Fatalf("headers = %v", req.Header)
	}
	ms, err := grokModels(context.Background(), acct.sign)
	if err != nil || len(ms) != 1 || got.Get("Authorization") != "Bearer k-me@x.ai" {
		t.Fatalf("%v %+v %v", err, ms, got)
	}
}

func TestGrokTokenReadsTheCLIsSignIn(t *testing.T) {
	home := t.TempDir()
	exp := time.Now().Add(2 * time.Hour).UTC()
	auth := map[string]any{"https://auth.x.ai::u1": map[string]any{
		"key": "tok", "email": "me@example.com", "auth_mode": "oidc", "refresh_token": "r",
		"expires_at": exp.Format(time.RFC3339Nano), "oidc_issuer": "https://auth.x.ai"}}
	b, _ := json.Marshal(auth)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if u, ok := GrokUser(home); !ok || u != "me@example.com" {
		t.Fatalf("user = %q %v", u, ok)
	}
	c, err := grokAccessToken(home, "", false)
	if err != nil || c.Key != "tok" {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := grokAccessToken(t.TempDir(), "", false); err == nil {
		t.Fatal("no sign-in, yet a token")
	}
}

// Codex's freeform apply_patch is left out: Grok's backend turns away a
// request with a tool type it doesn't know, a namespace among them. A
// namespace's functions go as functions of their own, under their flat
// names (#404); one with none to give goes whole.
func TestGrokBodyLeavesOutCustomTools(t *testing.T) {
	in := []byte(`{"model":"grok-4.7","tools":[{"type":"function","name":"shell"},{"type":"custom","name":"apply_patch","format":{"type":"grammar"}},{"type":"namespace","name":"multi_agent_v1","tools":[]},{"type":"namespace","name":"collaboration","description":"agents","tools":[{"type":"function","name":"spawn_agent","parameters":{"type":"object"}},{"type":"custom","name":"freeform"}]},{"type":"namespace","name":"odd","tools":[{"type":"custom","name":"x"}]},{"type":"web_search","external_web_access":false}],"tool_choice":{"type":"custom","name":"apply_patch"},"max_output_tokens":100}`)
	var got struct {
		Tools  []map[string]any `json:"tools"`
		Choice any              `json:"tool_choice"`
		Max    int              `json:"max_output_tokens"`
	}
	if err := json.Unmarshal(grokBody(in), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tools) != 3 || got.Tools[0]["type"] != "function" || got.Tools[0]["name"] != "shell" ||
		got.Tools[1]["type"] != "function" || got.Tools[1]["name"] != "collaboration__spawn_agent" || got.Tools[1]["parameters"] == nil ||
		len(got.Tools[2]) != 1 || got.Tools[2]["type"] != "web_search" || got.Choice != nil || got.Max != 100 {
		t.Fatalf("%+v", got)
	}
	same := []byte(`{"tools":[{"type":"function","name":"x"}],"tool_choice":"auto"}`)
	if string(grokBody(same)) != string(same) {
		t.Fatal("a body Grok takes was changed")
	}
}

// A call to a namespaced function handed back, and a tool_choice naming
// one, go under the flat name Grok was offered it by; its output keeps its
// call_id. Responses Lite's "functions" namespace is no namespace at all.
func TestGrokBodyFlattensNamespacedCalls(t *testing.T) {
	in := []byte(`{"tools":[{"type":"function","name":"exec_command"},{"type":"namespace","name":"codex_app","tools":[{"type":"function","name":"set_thread_title"}]},{"type":"namespace","name":"functions","tools":[{"type":"function","name":"view_image"}]}],"tool_choice":{"type":"function","name":"set_thread_title","namespace":"codex_app"},"input":[
		{"type":"function_call","call_id":"c1","name":"spawn_agent","namespace":"collaboration","arguments":"{}"},
		{"type":"function_call_output","call_id":"c1","output":"ok"},
		{"type":"function_call","call_id":"c2","name":"exec_command","arguments":"{}"},
		{"type":"function_call","call_id":"c3","name":"view_image","namespace":"functions","arguments":"{}"}]}`)
	var got struct {
		Tools  []map[string]any `json:"tools"`
		Choice map[string]any   `json:"tool_choice"`
		Input  []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(grokBody(in), &got); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range got.Tools {
		names = append(names, fmt.Sprint(tl["name"]))
	}
	if strings.Join(names, ",") != "exec_command,codex_app__set_thread_title,view_image" {
		t.Fatalf("tools %v", names)
	}
	if got.Choice["name"] != "codex_app__set_thread_title" || got.Choice["namespace"] != nil {
		t.Fatalf("tool_choice %v", got.Choice)
	}
	if c := got.Input[0]; c["name"] != "collaboration__spawn_agent" || c["namespace"] != nil || c["call_id"] != "c1" {
		t.Fatalf("call %v", c)
	}
	if got.Input[1]["call_id"] != "c1" || got.Input[2]["name"] != "exec_command" || got.Input[3]["name"] != "view_image" || got.Input[3]["namespace"] != nil {
		t.Fatalf("input %v", got.Input)
	}
}

// A tool_choice with no tools beside it goes: the backend turns the
// request away over it, as it did Codex's compaction summary (#378).
func TestGrokBodyDropsLoneToolChoice(t *testing.T) {
	for _, in := range []string{
		`{"model":"grok-4.7","reasoning":{"effort":"low"},"tool_choice":"auto","parallel_tool_calls":false,"input":[]}`,
		`{"model":"grok-4.7","tool_choice":"auto"}`,
		`{"model":"grok-4.7","tools":[{"type":"custom","name":"apply_patch"}],"tool_choice":"auto"}`,
	} {
		if got := string(grokBody([]byte(in))); strings.Contains(got, "tool_choice") {
			t.Errorf("%s\n-> %s", in, got)
		}
	}
	same := []byte(`{"tools":[{"type":"function","name":"x"}],"tool_choice":"required","reasoning":{"effort":"low"}}`)
	if string(grokBody(same)) != string(same) {
		t.Fatal("a tool_choice with tools was changed")
	}
}

// Codex's reasoning, handed back with a null content, is sent without it.
func TestGrokBodyDropsNullReasoningContent(t *testing.T) {
	in := []byte(`{"input":[{"type":"reasoning","id":"rs_1","summary":[],"content":null,"encrypted_content":"a+b/c="},{"type":"message","role":"user","content":"hi"}]}`)
	want := `{"input":[{"encrypted_content":"a+b/c=","id":"rs_1","summary":[],"type":"reasoning"},{"content":"hi","role":"user","type":"message"}]}`
	if got := string(grokBody(in)); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	same := []byte(`{"input":[{"type":"reasoning","content":[],"encrypted_content":"x"}]}`)
	if string(grokBody(same)) != string(same) {
		t.Fatal("reasoning with content was changed")
	}
}

// On Windows the CLI is grok.exe, looked for in the installer's bin first,
// then on PATH, then ~/.local/bin; only the installer's own folders are
// taken on trust (#180).
func TestGrokCandidates(t *testing.T) {
	home := t.TempDir()
	npm := filepath.Join(home, "AppData", "Roaming", "npm")
	custom := filepath.Join(home, "tools")
	for _, c := range []struct {
		goos, binDir, grokHome string
		want                   []grokCandidate
	}{
		{"windows", "", filepath.Join(home, ".grok"), []grokCandidate{
			{filepath.Join(home, ".grok", "bin", "grok.exe"), true},
			{filepath.Join(npm, "grok.exe"), false},
			{filepath.Join(home, ".local", "bin", "grok.exe"), false},
		}},
		{"windows", custom, filepath.Join(home, "gh"), []grokCandidate{
			{filepath.Join(custom, "grok.exe"), true},
			{filepath.Join(home, "gh", "bin", "grok.exe"), true},
			{filepath.Join(home, ".grok", "bin", "grok.exe"), true},
			{filepath.Join(npm, "grok.exe"), false},
			{filepath.Join(home, ".local", "bin", "grok.exe"), false},
		}},
		{"darwin", "", filepath.Join(home, ".grok"), []grokCandidate{
			{filepath.Join(home, ".grok", "bin", "grok"), true},
			{filepath.Join(npm, "grok"), false},
			{filepath.Join(home, ".local", "bin", "grok"), false},
		}},
	} {
		// the registry's PATH repeats the process' and has a relative entry
		path := []string{npm, "", "relative", filepath.Join(home, ".grok", "bin"), npm}
		got := grokCandidates(c.goos, home, c.grokHome, c.binDir, path)
		if len(got) != len(c.want) {
			t.Fatalf("%s %q: %+v", c.goos, c.binDir, got)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s %q: [%d] = %+v, want %+v", c.goos, c.binDir, i, got[i], c.want[i])
			}
		}
	}
}

// An installed Grok Build is found where its installer put it even with
// another grok ahead of it on PATH, and one on PATH is taken only when it
// lives under a .grok folder.
func TestGrokExecutableFinds(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GROK_HOME", "")
	t.Setenv("GROK_BIN_DIR", "")
	name := "grok"
	if runtime.GOOS == "windows" {
		name = "grok.exe"
	}
	put := func(p string) string {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		testenv.Program(t, p, "#!/bin/sh\n")
		return p
	}
	npm := filepath.Join(home, "npm")
	put(filepath.Join(npm, name))
	t.Setenv("PATH", npm)
	if p := GrokExecutable(); p != "" && !strings.Contains(p, ".grok") {
		t.Fatalf("another grok taken: %s", p)
	}
	pathed := put(filepath.Join(home, ".grok", "versions", "1", name))
	t.Setenv("PATH", npm+string(os.PathListSeparator)+filepath.Dir(pathed))
	if p := GrokExecutable(); p != pathed {
		t.Fatalf("on PATH: %q", p)
	}
	installed := put(filepath.Join(home, ".grok", "bin", name))
	if p := GrokExecutable(); p != installed {
		t.Fatalf("installed: %q", p)
	}
	custom := put(filepath.Join(home, "tools", name))
	t.Setenv("GROK_BIN_DIR", filepath.Dir(custom))
	if p := GrokExecutable(); p != custom {
		t.Fatalf("GROK_BIN_DIR: %q", p)
	}
}

// A function whose parameters are an anyOf (or oneOf) at the root, as
// Codex's codex_app automation_update, goes to Grok as an object: Grok's
// backend turns the request away otherwise ("[invalid_client_tool_schema]
// ... tool parameter root must be an object type", Fate on Discord). The
// object branches' properties are merged, $refs to $defs read, a field all
// of them require stays required, the non-object branches go. One that is
// an object already is left as it is.
// TestObjectRootKeepsOwnRequired: the root's and allOf's own required
// fields stay required beside anyOf's and oneOf's branches, which only add
// the fields they all require.
func TestObjectRootKeepsOwnRequired(t *testing.T) {
	var ps map[string]any
	if err := json.Unmarshal([]byte(`{
		"type": "object",
		"properties": {"id": {"type": "string"}},
		"required": ["id"],
		"allOf": [{"properties": {"kind": {"type": "string"}}, "required": ["kind"]}],
		"anyOf": [
			{"type": "object", "properties": {"a": {"type": "string"}, "n": {"type": "integer"}}, "required": ["a", "n"]},
			{"type": "object", "properties": {"b": {"type": "string"}}, "required": ["b", "n", "id"]}
		]
	}`), &ps); err != nil {
		t.Fatal(err)
	}
	if !ObjectRoot(ps) {
		t.Fatal("not changed")
	}
	if got := fmt.Sprint(ps["required"]); got != "[id kind n]" {
		t.Fatalf("required = %s", got)
	}
	if props := ps["properties"].(map[string]any); len(props) != 5 {
		t.Fatalf("properties = %v", props)
	}
}

func TestGrokBodyObjectRoot(t *testing.T) {
	in := []byte(`{"tools":[
		{"type":"function","name":"mcp__codex_app__automation_update","parameters":{"anyOf":[
			{"type":"object","properties":{"id":{"type":"string"},"prompt":{"type":"string"}},"required":["id","prompt"]},
			{"$ref":"#/$defs/pause"},
			{"type":"null"}],"$defs":{"pause":{"type":"object","properties":{"id":{"type":"string"},"paused":{"type":"boolean"}},"required":["id"]}}}},
		{"type":"namespace","name":"codex_app","tools":[{"type":"function","name":"pick","parameters":{"oneOf":[{"type":"object","properties":{"a":{"type":"string"}}}]}}]},
		{"type":"function","name":"plain","parameters":{"type":"object","properties":{"x":{"type":"string"}}}}]}`)
	var got struct {
		Tools []struct {
			Name       string         `json:"name"`
			Parameters map[string]any `json:"parameters"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(grokBody(in), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tools) != 3 {
		t.Fatalf("%+v", got)
	}
	for _, tl := range got.Tools {
		p := tl.Parameters
		if p["type"] != "object" || p["anyOf"] != nil || p["oneOf"] != nil {
			t.Fatalf("%s: %v", tl.Name, p)
		}
	}
	p := got.Tools[0].Parameters
	props, _ := p["properties"].(map[string]any)
	if len(props) != 3 || props["paused"] == nil || props["prompt"] == nil || fmt.Sprint(p["required"]) != "[id]" {
		t.Fatalf("merged %v", p)
	}
	if got.Tools[1].Name != "codex_app__pick" || got.Tools[1].Parameters["properties"].(map[string]any)["a"] == nil {
		t.Fatalf("namespaced %+v", got.Tools[1])
	}
	same := []byte(`{"tools":[{"type":"function","name":"x","parameters":{"type":"object","properties":{}}}]}`)
	if string(grokBody(same)) != string(same) {
		t.Fatal("an object root was changed")
	}
}

// TestObjectRootNestedUnion: Codex desktop's automation_update, whose
// parameters are zod's discriminated union on mode with the create and
// update branches each a union on kind of their own (toJSONSchema with
// reused: "ref", as ChatGPT.app 26.930 builds it; #1271). Folded to an
// object root, the nested branches' fields are kept, so a model can still
// create an automation, and a field the branches type apart (mode, kind)
// takes any of their types, not the first branch's alone; only mode,
// which every branch requires, stays required.
func TestObjectRootNestedUnion(t *testing.T) {
	b, err := os.ReadFile("testdata/codex_automation_update_schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var ps map[string]any
	if err := json.Unmarshal(b, &ps); err != nil {
		t.Fatal(err)
	}
	if !ObjectRoot(ps) {
		t.Fatal("not changed")
	}
	if ps["type"] != "object" || ps["anyOf"] != nil || ps["oneOf"] != nil {
		t.Fatalf("root %v", ps)
	}
	props, _ := ps["properties"].(map[string]any)
	for _, k := range []string{"mode", "id", "name", "prompt", "rrule", "status", "kind", "projectId", "model", "targetThreadId", "executionEnvironment"} {
		if props[k] == nil {
			t.Errorf("no %s among %v", k, slices.Sorted(maps.Keys(props)))
		}
	}
	if got := fmt.Sprint(ps["required"]); got != "[mode]" {
		t.Errorf("required = %s", got)
	}
	// every mode a branch takes can still be sent
	mode, _ := json.Marshal(props["mode"])
	for _, m := range []string{`"view"`, `"delete"`, `__schema11`} {
		if !strings.Contains(string(mode), m) {
			t.Errorf("mode %s leaves out %s", mode, m)
		}
	}
	kind, _ := json.Marshal(props["kind"])
	if !strings.Contains(string(kind), "__schema7") || !strings.Contains(string(kind), "__schema12") {
		t.Errorf("kind %s", kind)
	}
}
