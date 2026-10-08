package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
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
  echo '{"type":"result","subtype":"success","is_error":false,"result":"","structured_output":{"color":"blue"}}'
done
`
	testenv.Program(t, filepath.Join(dir, "claude"), script)
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

	got := schemaReplay(t,
		cliStart("m1"),
		cliText(0, "The sky is **blue**."),
		cliStructured(1, "t1", `{\"color`, `\": \"blue\"}`),
		cliToolResult("t1", false, "Structured output provided successfully"),
		cliEvent(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`),
		cliEvent(`{"type":"message_stop"}`),
		`{"type":"result","subtype":"success","is_error":false,"result":"{\"color\":\"blue\"}","structured_output":{"color":"blue"}}`,
	)
	if got != `start:m1|text:{"color":"blue"}|stop:stop|end` {
		t.Fatalf("events: %s", got)
	}
}

// A StructuredOutput call that doesn't fit the schema is told so by Claude
// Code, which has the model call it again: the client gets the call that
// fit, never the one turned away, and one reply.
func TestClaudeSchemaRetriedCall(t *testing.T) {
	got := schemaReplay(t,
		cliStart("m1"),
		cliStructured(0, "t1", `{\"color\": 3}`),
		cliToolResult("t1", true, "Output does not match required schema: color must be string"),
		cliEvent(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`),
		cliEvent(`{"type":"message_stop"}`),
		cliStart("m2"),
		cliStructured(0, "t2", `{\"color\": \"blue\"}`),
		cliToolResult("t2", false, "Structured output provided successfully"),
		cliEvent(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`),
		cliEvent(`{"type":"message_stop"}`),
		`{"type":"result","subtype":"success","is_error":false,"result":"{\"color\":\"blue\"}","structured_output":{"color":"blue"}}`,
	)
	if got != `start:m1|text:{"color":"blue"}|stop:stop|end` {
		t.Fatalf("events: %s", got)
	}
}

// A model that answers in words first is asked by Claude Code for the
// call: the words are left out and the reply goes on to the call.
func TestClaudeSchemaAnsweredInWordsFirst(t *testing.T) {
	got := schemaReplay(t,
		cliStart("m1"),
		cliText(0, "Blue."),
		cliEvent(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`),
		cliEvent(`{"type":"message_stop"}`),
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"You MUST call the StructuredOutput tool"}]}}`,
		cliStart("m2"),
		cliStructured(0, "t1", `{\"color\": \"blue\"}`),
		cliToolResult("t1", false, "Structured output provided successfully"),
		cliEvent(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`),
		cliEvent(`{"type":"message_stop"}`),
		`{"type":"result","subtype":"success","is_error":false,"result":"","structured_output":{"color":"blue"}}`,
	)
	if got != `start:m1|text:{"color":"blue"}|stop:stop|end` {
		t.Fatalf("events: %s", got)
	}
}

// Claude Code giving up on a fitting call is an error to the client, not
// the last call it turned away.
func TestClaudeSchemaGivenUp(t *testing.T) {
	got := schemaReplay(t,
		cliStart("m1"),
		cliStructured(0, "t1", `{\"color\": 3}`),
		cliToolResult("t1", true, "Output does not match required schema"),
		cliEvent(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`),
		cliEvent(`{"type":"message_stop"}`),
		`{"type":"result","subtype":"error_max_structured_output_retries","is_error":true,"result":""}`,
	)
	if !strings.HasPrefix(got, `start:m1|error:Claude Code gave no answer fitting the schema (error_max_structured_output_retries)`) || strings.Contains(got, "text:") {
		t.Fatalf("events: %s", got)
	}
}

func cliEvent(e string) string { return `{"type":"stream_event","event":` + e + `}` }

func cliStart(id string) string {
	return cliEvent(`{"type":"message_start","message":{"id":"` + id + `","model":"x","usage":{"input_tokens":10}}}`)
}

func cliText(i int, s string) string {
	n := strconv.Itoa(i)
	return cliEvent(`{"type":"content_block_start","index":`+n+`,"content_block":{"type":"text","text":""}}`) + "\n" +
		cliEvent(`{"type":"content_block_delta","index":`+n+`,"delta":{"type":"text_delta","text":`+strconv.Quote(s)+`}}`)
}

// cliStructured is a StructuredOutput call streamed in parts, each JSON
// already escaped for a string.
func cliStructured(i int, id string, parts ...string) string {
	n := strconv.Itoa(i)
	lines := []string{cliEvent(`{"type":"content_block_start","index":` + n + `,"content_block":{"type":"tool_use","id":"` + id + `","name":"StructuredOutput","input":{}}}`)}
	for _, p := range parts {
		lines = append(lines, cliEvent(`{"type":"content_block_delta","index":`+n+`,"delta":{"type":"input_json_delta","partial_json":"`+p+`"}}`))
	}
	return strings.Join(lines, "\n")
}

func cliToolResult(id string, isError bool, content string) string {
	return `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","is_error":` + strconv.FormatBool(isError) + `,"content":` + strconv.Quote(content) + `}]}}`
}

// schemaReplay is what a run with a schema tells the client of Claude
// Code's stream, until the reply ends.
func schemaReplay(t *testing.T, lines ...string) string {
	t.Helper()
	run := &subscriptionRun{schema: true}
	seg := run.attach()
	go run.readOutput(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var got []string
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-seg:
			if !ok {
				return strings.Join(append(got, "end"), "|")
			}
			switch e.Kind {
			case KStart:
				got = append(got, "start:"+e.MsgID)
			case KText:
				got = append(got, "text:"+e.Text)
			case KToolStart, KToolArgs:
				got = append(got, "tool:"+e.Name+e.Text)
			case KStop:
				got = append(got, "stop:"+e.Stop)
			case KError:
				got = append(got, "error:"+e.Text)
			}
		case <-timeout:
			t.Fatalf("reply never ended: %s", strings.Join(got, "|"))
		}
	}
}
