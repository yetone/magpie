package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A request with output_config.format runs Claude Code with --json-schema;
// one without runs it without.
func TestClaudeSchemaGivenToClaudeCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
for a in "$@"; do echo "$a"; done > ` + dir + `/args.$$
while read -r line; do
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`
	os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	s := New()
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	for _, body := range []string{
		`{"model":"claude-sonnet-5","max_tokens":100,"system":"rules","messages":[{"role":"user","content":"one"}]}`,
		`{"model":"claude-sonnet-5","max_tokens":100,"system":"rules","messages":[{"role":"user","content":"two"}],"output_config":{"format":{"type":"json_schema","schema":{"type":"object","properties":{"color":{"type":"string"}}}}}}`,
	} {
		rec := httptest.NewRecorder()
		var u Usage
		if code, msg := s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, p, "claude-sonnet-5", []byte(body), &u); code != 200 {
			t.Fatalf("%d %s", code, msg)
		}
	}
	var schemas []string
	files, _ := filepath.Glob(filepath.Join(dir, "args.*"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		args := strings.Split(strings.TrimSpace(string(b)), "\n")
		if i := slices.Index(args, "--json-schema"); i >= 0 {
			schemas = append(schemas, args[i+1])
		}
	}
	if len(files) != 2 || len(schemas) != 1 || !strings.Contains(schemas[0], `"color"`) {
		t.Fatalf("--json-schema: %q of %d runs", schemas, len(files))
	}
}

// With a schema the reply is Claude Code's StructuredOutput call, as the
// JSON text the client asked for: the words beside it are left out, and
// the call ends the turn.
func TestClaudeSchemaAnswer(t *testing.T) {
	body := `{"model":"claude-sonnet-5","max_tokens":10,"messages":[{"role":"user","content":"sky?"}],"output_config":{"effort":"low","format":{"type":"json_schema","schema":{"type":"object","properties":{"color":{"type":"string"}}}}}}`
	req, err := parseAnthropic([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(req.Schema), `"color"`) {
		t.Fatalf("schema: %s", req.Schema)
	}
	var built struct {
		OutputConfig struct {
			Format struct {
				Type   string          `json:"type"`
				Schema json.RawMessage `json:"schema"`
			} `json:"format"`
		} `json:"output_config"`
	}
	json.Unmarshal(buildAnthropic(req, "claude-sonnet-4-5"), &built)
	if built.OutputConfig.Format.Type != "json_schema" || !strings.Contains(string(built.OutputConfig.Format.Schema), `"color"`) {
		t.Fatalf("rebuilt output_config: %+v", built.OutputConfig)
	}

	ev := func(e string) string { return `{"type":"stream_event","event":` + e + `}` }
	lines := []string{
		ev(`{"type":"message_start","message":{"id":"m1","model":"x","usage":{"input_tokens":10}}}`),
		ev(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		ev(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"The sky is **blue**."}}`),
		ev(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t1","name":"StructuredOutput","input":{}}}`),
		ev(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":""}}`),
		ev(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"color"}}`),
		ev(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\": \"blue\"}"}}`),
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"Structured output provided successfully"}]}}`,
		ev(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`),
		ev(`{"type":"message_stop"}`),
		`{"type":"result","subtype":"success","is_error":false,"result":"{\"color\":\"blue\"}","structured_output":{"color":"blue"}}`,
	}
	run := &subscriptionRun{schema: true}
	seg := run.attach()
	go run.readOutput(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var got []string
	for e := range seg {
		switch e.Kind {
		case KText:
			got = append(got, "text:"+e.Text)
		case KToolStart, KToolArgs:
			got = append(got, "tool:"+e.Name+e.Text)
		case KStop:
			got = append(got, "stop:"+e.Stop)
		}
	}
	if s := strings.Join(got, "|"); s != `text:{"color|text:": "blue"}|stop:stop` {
		t.Fatalf("events: %s", s)
	}
}
