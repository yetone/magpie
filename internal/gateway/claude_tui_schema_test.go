package gateway

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// schemaTUI is a run in the interactive mode answering a request with a
// schema through magpie's answer tool.
func schemaTUI(t *testing.T, schema string) *tuiCalls {
	t.Helper()
	c := newTUICalls(t)
	s, err := compileAnswerSchema(json.RawMessage(schema))
	if err != nil {
		t.Fatal(err)
	}
	c.run.mu.Lock()
	c.run.schema, c.run.answerName, c.run.answerSchema = true, answerToolName, s
	c.run.mu.Unlock()
	return c
}

// answerCall makes Claude Code's call of the answer tool and returns what
// the MCP helper is told.
func (c *tuiCalls) answerCall(id, args string) mcpToolResult {
	c.t.Helper()
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"tool_call_id":"`+id+`","name":"`+answerToolName+`","arguments":`+args+`}`))
	r.SetPathValue("token", "run")
	rec := httptest.NewRecorder()
	c.bridge.mcpCall(rec, r)
	var res mcpToolResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		c.t.Fatalf("the answer tool said %q: %v", rec.Body.String(), err)
	}
	return res
}

func answerRow(id, input string) string {
	return tuiRow("msg_"+id, `{"type":"tool_use","id":"`+id+`","name":"mcp__magpie__`+answerToolName+`","input":`+input+`}`, "tool_use", 9)
}

// Claude Code's interactive mode ignores --json-schema: the answer is the
// call of magpie's answer tool that fits the schema. One that doesn't is
// told why, to be made again, and neither call, nor the words around
// them, is told to the client.
func TestClaudeTUISchemaAnswer(t *testing.T) {
	c := schemaTUI(t, `{"type":"object","properties":{"color":{"type":"string"}},"required":["color"],"additionalProperties":false}`)
	first := c.run.segment
	c.write(tuiRow("msg_w", `{"type":"text","text":"Let me answer."}`, "tool_use", 4), answerRow("toolu_1", `{"colour":"blue"}`))
	bad := c.answerCall("toolu_1", `{"colour":"blue"}`)
	if !bad.IsError || !strings.Contains(bad.Content[0]["text"].(string), "doesn't fit the schema") {
		t.Fatalf("a call that doesn't fit was told %+v", bad)
	}
	c.write(answerRow("toolu_2", `{"color":"blue"}`))
	if good := c.answerCall("toolu_2", `{"color":"blue"}`); good.IsError {
		t.Fatalf("a call that fits was told %+v", good)
	}
	c.write(tuiRow("msg_end", `{"type":"text","text":"Done."}`, "end_turn", 2), tuiDone)
	text, calls, stop, _, raw := c.reply(first)
	if text != `{"color":"blue"}` || len(calls) != 0 || stop != "end_turn" {
		t.Fatalf("the reply %q %v %q:\n%s", text, calls, stop, raw)
	}
	for _, said := range []string{"Let me answer", "Done.", "colour", answerToolName} {
		if strings.Contains(raw, said) {
			t.Fatalf("the client was told %q:\n%s", said, raw)
		}
	}
}

// A turn that ends without the answer tool's call is asked for it once,
// and the reply is the call it then makes; one that still makes none
// fails, saying so.
func TestClaudeTUISchemaAskedOnce(t *testing.T) {
	for _, comply := range []bool{true, false} {
		c := schemaTUI(t, `{"type":"object"}`)
		pr, pw := io.Pipe()
		c.run.stdin = pw
		told := make(chan string, 4)
		go func() {
			sc := bufio.NewScanner(pr)
			for sc.Scan() {
				told <- sc.Text()
			}
		}()
		first := c.run.segment
		c.write(tuiRow("msg_w", `{"type":"text","text":"blue"}`, "end_turn", 4), tuiDone)
		select {
		case line := <-told:
			if !strings.Contains(line, "calling the "+answerToolName+" tool") {
				t.Fatalf("the run was told %s", line)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a turn with no answer wasn't asked for one")
		}
		if comply {
			c.write(answerRow("toolu_1", `{"sky":"blue"}`))
			c.answerCall("toolu_1", `{"sky":"blue"}`)
		} else {
			c.write(tuiRow("msg_x", `{"type":"text","text":"blue, I said"}`, "end_turn", 4))
		}
		c.write(tuiDone)
		text, _, stop, _, raw := c.reply(first)
		if comply && (text != `{"sky":"blue"}` || stop != "end_turn") {
			t.Fatalf("the reply %q %q:\n%s", text, stop, raw)
		}
		if !comply && (!strings.Contains(raw, "no answer fitting the schema") || strings.Contains(raw, "blue")) {
			t.Fatalf("a turn that never answered:\n%s", raw)
		}
		select {
		case line := <-told:
			t.Fatalf("asked again: %s", line)
		default:
		}
		pw.Close()
	}
}

// The answer tool is named apart from the client's tools, takes the
// schema as Claude Code's -p does (no root $schema), and a schema that
// refers outside itself is refused, not fetched.
func TestAnswerTool(t *testing.T) {
	tool := answerTool([]Tool{{Name: answerToolName}}, json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object"}`))
	if tool.Name != answerToolName+"2" || string(tool.Schema) != `{"type":"object"}` {
		t.Fatalf("tool %s %s", tool.Name, tool.Schema)
	}
	for _, s := range []string{`{"$ref":"https://example.com/s.json"}`, `{"$ref":"file:///etc/passwd"}`, `not json`} {
		if _, err := compileAnswerSchema(json.RawMessage(s)); err == nil {
			t.Fatalf("%s compiled", s)
		}
	}
	if _, err := compileAnswerSchema(json.RawMessage(`{"$defs":{"c":{"type":"string"}},"type":"object","properties":{"c":{"$ref":"#/$defs/c"}}}`)); err != nil {
		t.Fatalf("a schema's own $ref: %v", err)
	}
}

// A schema is checked against its draft's metaschema, which the vendored
// jsonschema (third_party/jsonschema) compiles on first use, not at
// launch: a schema that breaks its draft is still refused.
func TestAnswerSchemaCheckedAgainstItsDraft(t *testing.T) {
	for _, s := range []string{
		`{"type":5}`,
		`{"type":"object","properties":{"a":{"minLength":"x"}}}`,
		`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","required":"a"}`,
	} {
		if _, err := compileAnswerSchema(json.RawMessage(s)); err == nil {
			t.Errorf("%s compiled", s)
		}
	}
}
