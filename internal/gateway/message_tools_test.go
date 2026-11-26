package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// piAddedTool is Pi 1.2.0's own Chat request for the turn after an
// extension called pi.setActiveTools([lookup_pantry, lookup_recipe]),
// with supportsMidConvoSystemMessages and supportsMidConvoToolAdditions
// on, as deephbz recorded it (#2322; only the working directory was
// redacted). The added tool is in a system message with no content; the
// request's tools stay as they were at the start.
const piAddedTool = `{
 "model": "wire-model",
 "messages": [
  {"role": "system", "content": "You are a helpful cooking assistant. Help the user find recipes and pantry information.\n\n\n\n<cwd>\n(redacted)\n</cwd>"},
  {"role": "user", "content": [{"type": "text", "text": "Please reply READY. Do not call any tools."}]},
  {"role": "assistant", "content": "PLAIN_OK"},
  {"role": "system", "tools": [{"type": "function", "function": {"name": "lookup_recipe", "description": "Look up the recipe catalogue and return the recipe code.", "parameters": {"type": "object", "properties": {}, "required": [], "additionalProperties": false}}}]},
  {"role": "system", "content": "Updated system prompt section \"recipe_catalogue\":\n\n<recipe_catalogue>\nThe recipe catalogue is now available.\n</recipe_catalogue>"},
  {"role": "user", "content": [{"type": "text", "text": "Please call lookup_recipe exactly once with {}. Then tell me the recipe code it returned."}]}
 ],
 "stream": true,
 "stream_options": {"include_usage": true},
 "store": false,
 "max_completion_tokens": 1024,
 "tools": [{"type": "function", "function": {"name": "lookup_pantry", "description": "Look up the pantry and return its status.", "parameters": {"type": "object", "properties": {}, "required": [], "additionalProperties": false}}}]
}`

// piAsking is piAddedTool asking model.
func piAsking(model string) string {
	return strings.Replace(piAddedTool, `"wire-model"`, `"`+model+`"`, 1)
}

var piTools = []string{"lookup_pantry", "lookup_recipe"}

func namesAt(body, path string) []string {
	var out []string
	for _, n := range gjson.Get(body, path).Array() {
		out = append(out, n.String())
	}
	return out
}

// noEmptySystem fails when a system text magpie sent upstream is empty, or
// when it is not Pi's leading system message alone: the update Pi sends
// after a reply stays in its turn (#7204), so the system a Messages
// upstream caches ahead of the conversation is the same on every turn.
func noEmptySystem(t *testing.T, texts []string) {
	t.Helper()
	all := strings.Join(texts, "\n")
	for _, s := range texts {
		if strings.TrimSpace(s) == "" {
			t.Errorf("an empty system text was sent: %q", texts)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(all), "</cwd>") {
		t.Errorf("system texts are not Pi's leading system message alone: %q", all)
	}
}

// updateInTurn fails unless Pi's system prompt update reaches the
// conversation as a reminder ahead of the user's last words.
func updateInTurn(t *testing.T, body string) {
	t.Helper()
	var texts []string
	var walk func(gjson.Result)
	walk = func(v gjson.Result) {
		switch {
		case v.IsArray(), v.IsObject():
			v.ForEach(func(_, e gjson.Result) bool { walk(e); return true })
		case v.Type == gjson.String:
			texts = append(texts, v.String())
		}
	}
	walk(gjson.Parse(body))
	update, ask := -1, -1
	for i, s := range texts {
		if update < 0 && strings.Contains(s, "Updated system prompt section") {
			update = i
			if !strings.Contains(s, "<system-reminder>\nUpdated system prompt section") {
				t.Errorf("the update isn't told as a reminder: %q", s)
			}
		}
		if strings.Contains(s, "Please call lookup_recipe") {
			ask = i
		}
	}
	if update < 0 || ask < update {
		t.Errorf("the update is not in the conversation ahead of the last turn (at %d, the ask at %d): %s", update, ask, body)
	}
}

// The tool Pi adds in a message reaches every route a Chat body takes, once
// beside the request's own: relayed to a Chat API, and translated to
// Messages, Responses, Gemini and Codex's ChatGPT backend. A system message
// that carried only the tool is not sent on as an empty system message.
func TestPiAddedToolReachesEveryRoute(t *testing.T) {
	srv, seen := relayOfEvery(t)
	for _, c := range []struct {
		name, model, path string
		p                 provider.Provider
		check             func(t *testing.T, body string)
	}{
		{"chat relayed", "wire-model", "/v1/chat/completions", provider.Provider{Chat: srv.URL + "/v1"}, func(t *testing.T, body string) {
			if got := namesAt(body, "tools.#.function.name"); !slices.Equal(got, piTools) {
				t.Errorf("tools %v, want %v: %s", got, piTools, body)
			}
			msgs := gjson.Get(body, "messages").Array()
			var roles []string
			for _, m := range msgs {
				roles = append(roles, m.Get("role").String())
				if m.Get("tools").Exists() {
					t.Errorf("a message still carries tools: %s", m.Raw)
				}
				if m.Get("role").String() == "system" && strings.TrimSpace(m.Get("content").String()) == "" {
					t.Errorf("an empty system message was relayed: %s", m.Raw)
				}
			}
			// the update stays where Pi put it, as every Chat message does
			if want := []string{"system", "user", "assistant", "system", "user"}; !slices.Equal(roles, want) {
				t.Errorf("roles %v, want %v", roles, want)
			}
			if !strings.HasPrefix(msgs[3].Get("content").String(), "Updated system prompt section") {
				t.Errorf("the update moved: %s", msgs[3].Raw)
			}
		}},
		{"messages", "claude-opus-5-5", "/v1/messages", provider.Provider{Anthropic: srv.URL}, func(t *testing.T, body string) {
			if got := namesAt(body, "tools.#.name"); !slices.Equal(got, piTools) {
				t.Errorf("tools %v, want %v: %s", got, piTools, body)
			}
			sys := gjson.Get(body, "system")
			texts := namesAt(body, "system.#.text")
			if sys.Type == gjson.String {
				texts = []string{sys.String()}
			}
			noEmptySystem(t, texts)
			updateInTurn(t, gjson.Get(body, "messages").Raw)
		}},
		{"responses", "wire-model", "/v1/responses", provider.Provider{Responses: srv.URL + "/v1"}, func(t *testing.T, body string) {
			if got := namesAt(body, "tools.#.name"); !slices.Equal(got, piTools) {
				t.Errorf("tools %v, want %v: %s", got, piTools, body)
			}
			noEmptySystem(t, []string{gjson.Get(body, "instructions").String()})
			updateInTurn(t, gjson.Get(body, "input").Raw)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			c.p.ID, c.p.Name, c.p.Key, c.p.Models = "pi", "Pi", "k", []string{c.model}
			if err := provider.Save(c.p); err != nil {
				t.Fatal(err)
			}
			seen()
			if code, b := post(t, "/v1/chat/completions", piAsking("pi/"+c.model)); code != 200 {
				t.Fatalf("%d %s", code, b)
			}
			paths, bodies := seen()
			if len(paths) != 1 || paths[0] != c.path {
				t.Fatalf("went to %v, want %s", paths, c.path)
			}
			c.check(t, bodies[0])
		})
	}

	t.Run("gemini", func(t *testing.T) {
		fresh(t)
		var got []byte
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`))
		}))
		t.Cleanup(up.Close)
		if err := provider.Save(provider.Provider{ID: "gem", Name: "Gem", Key: "AIza-test", Gemini: up.URL + "/v1beta", Models: []string{"gemini-2.5-pro"}}); err != nil {
			t.Fatal(err)
		}
		if code, b := post(t, "/v1/chat/completions", piAsking("gem/gemini-2.5-pro")); code != 200 {
			t.Fatalf("%d %s", code, b)
		}
		body := string(got)
		if names := namesAt(body, "tools.#.functionDeclarations.#.name|@flatten"); !slices.Equal(names, piTools) {
			t.Errorf("tools %v, want %v: %s", names, piTools, body)
		}
		noEmptySystem(t, namesAt(body, "systemInstruction.parts.#.text"))
		updateInTurn(t, gjson.Get(body, "contents").Raw)
	})

	t.Run("codex", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com")
		d := daybreak(t)
		if code, b := post(t, "/v1/chat/completions", piAsking("codex/gpt-daybreak-blue-latest")); code != 200 {
			t.Fatalf("%d %s", code, b)
		}
		body := string(d.last(t))
		if got := namesAt(body, "tools.#.name"); !slices.Equal(got, piTools) {
			t.Errorf("tools %v, want %v: %s", got, piTools, body)
		}
		// the client's system is the developer message ahead of its turns,
		// and its update a developer message where Pi put it (#502, #7204)
		dev := namesAt(body, `input.#(role=="developer")#.content.#.text|@flatten`)
		if len(dev) != 2 || !strings.HasPrefix(dev[1], "Updated system prompt section") {
			t.Fatalf("developer texts %q, want the system then the update", dev)
		}
		noEmptySystem(t, dev[:1])
	})
}

// Through a Claude subscription, whose Claude Code is offered the client's
// tools by magpie's MCP helper, the added tool is offered too, once.
func TestPiAddedToolOfferedToClaudeCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	dir := t.TempDir()
	// answers with the names in the tools file its MCP config names
	script := `#!/bin/sh
for a in "$@"; do
  case "$a" in *mcpServers*) tp=$(printf '%s' "$a" | sed 's/.*"\([^"]*tools\.json\)".*/\1/');; esac
done
while read -r line; do
  names=$(grep -o '"name":"[^"]*"' "$tp" | sed 's/"name":"//; s/"$//' | tr '\n' ',')
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-opus-5-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"tools '"$names"'"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`
	testenv.Program(t, filepath.Join(dir, "claude"), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	runsEndWithTest(t, dir)
	s := New()
	t.Cleanup(s.subscription.abortAll)
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	body := piAsking("claude-opus-5-5")
	rec := httptest.NewRecorder()
	var u Usage
	if code, msg := s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)), provider.Chat, p, "claude-opus-5-5", []byte(body), &u); code != 200 {
		t.Fatalf("%d %s", code, msg)
	}
	if !strings.Contains(rec.Body.String(), "tools lookup_pantry,lookup_recipe,") {
		t.Errorf("Claude Code was not offered %v once each: %s", piTools, rec.Body)
	}
}

// A name already offered keeps its first definition: the request's own,
// then the earliest message's. A system message with content and tools
// keeps its place and words.
func TestMessageToolsFirstDefinitionWins(t *testing.T) {
	body := `{"model":"m","messages":[
		{"role":"system","content":"base"},
		{"role":"user","content":"hi"},
		{"role":"system","content":"note","tools":[{"type":"function","function":{"name":"a","description":"again"}},{"type":"function","function":{"name":"b","description":"first b"}}]},
		{"role":"developer","tools":[{"type":"function","function":{"name":"b","description":"second b"}}]},
		{"role":"user","content":"go"}],
		"tools":[{"type":"function","function":{"name":"a","description":"own"}}]}`
	req, err := parseChat([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tl := range req.Tools {
		got = append(got, tl.Name+":"+tl.Description)
	}
	if want := []string{"a:own", "b:first b"}; !slices.Equal(got, want) {
		t.Errorf("tools %v, want %v", got, want)
	}
	if req.System != "base\n\nnote" {
		t.Errorf("system %q", req.System)
	}
	lifted := string(liftMessageTools([]byte(body)))
	if n := len(gjson.Get(lifted, "messages").Array()); n != 4 {
		t.Errorf("%d messages, want 4 (the tools-only one left out): %s", n, lifted)
	}
	if m := gjson.Get(lifted, "messages.2"); m.Get("content").String() != "note" || m.Get("tools").Exists() {
		t.Errorf("the system message with content: %s", m.Raw)
	}
	// a request without message tools is as it came
	plain := `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"a"}}]}`
	if out := string(liftMessageTools([]byte(plain))); out != plain {
		t.Errorf("changed: %s", out)
	}
}
