package cli

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// codexRead is a resources/read as Codex's TUI sends it (statusline.d.ts:
// the thread's state in params._meta), cut to the fields around the one
// read.
const codexRead = `{"jsonrpc":"2.0","id":3,"method":"resources/read","params":{"uri":"ui://quota-line/footer","_meta":{"com.openai/codex-statusline-state":{"thread":{"id":"6f1d2c3a-9b8e-4a7f-8c1d-2e3f4a5b6c7d"},"model":{"name":"gpt-5.5"},"account":{"rate_limits":null}}}}}`

// mcpTalk sends lines to a codexStatus and returns its replies by id.
func mcpTalk(t *testing.T, c *codexStatus, lines ...string) map[string]map[string]any {
	t.Helper()
	pr, pw := io.Pipe()
	go func() {
		c.serve(strings.NewReader(strings.Join(lines, "\n")+"\n"), pw)
		pw.Close()
	}()
	out := map[string]map[string]any{}
	sc := bufio.NewScanner(pr)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("%v: %s", err, sc.Text())
		}
		id, _ := json.Marshal(m["id"])
		out[string(id)] = m
	}
	return out
}

// lyj1115 on X: Codex's footer shows the model the thread went to and
// that account's windows, from what the gateway kept (never a vendor
// read); a tool Codex's statusline picker takes (its entrypoint), whose
// words name neither magpie nor a vendor, since the model is told them.
func TestCodexStatuslineServer(t *testing.T) {
	now := time.Now()
	s := statuslineGateway(t, now)
	c := newCodexStatus(s.URL)
	got := mcpTalk(t, c,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"codex-mcp-client","version":"0.171.0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		codexRead,
		`{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"ui://quota-line/footer","_meta":{"com.openai/codex-statusline-state":{"thread":{"id":"another"}}}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"nope"}`,
	)
	if len(got) != 5 {
		t.Fatalf("replies: %v", got)
	}
	if res, _ := got["1"]["result"].(map[string]any); res["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize: %v", got["1"])
	}
	tools, _ := got["2"]["result"].(map[string]any)["tools"].([]any)
	tool, _ := tools[0].(map[string]any)
	b, _ := json.Marshal(tool)
	if tool["name"] != "statusline" || !strings.Contains(string(b), `"entrypoints":[{"resourceUri":"ui://quota-line/footer","type":"statusline"}]`) {
		t.Fatalf("tool: %s", b)
	}
	if low := strings.ToLower(string(b)); strings.Contains(low, "magpie") || strings.Contains(low, "codex") || strings.Contains(low, "openai\"") {
		t.Fatalf("the tool the model is told of names magpie or a vendor: %s", b)
	}
	tree := func(id string) string {
		res, _ := got[id]["result"].(map[string]any)
		cs, _ := res["contents"].([]any)
		c, _ := cs[0].(map[string]any)
		if c["mimeType"] != "application/json" {
			t.Fatalf("read %s: %v", id, got[id])
		}
		return c["text"].(string)
	}
	// the thread's route is claude's: its account that answered last
	if want := `{"children":[{"children":["→ claude/claude-opus-5-5"],"props":{"color":"status-model"},"type":"text"},{"children":["claude a@example.com 5h 62% 7d 41%"],"props":{"color":"status-limit"},"type":"text"}],"props":{"gap":2,"wrap":"wrap"},"type":"row"}`; tree("3") != want {
		t.Fatalf("read:\n got %s\nwant %s", tree("3"), want)
	}
	// a thread the gateway has no route for: the account that answered last
	if !strings.Contains(tree("4"), `"claude a@example.com 5h 62% 7d 41%"`) || strings.Contains(tree("4"), "→") {
		t.Fatalf("read with no route: %s", tree("4"))
	}
	if got["5"]["error"] == nil {
		t.Fatalf("unknown method answered: %v", got["5"])
	}
}

// A relay key's balance is the account's line; a gateway that doesn't
// answer clears the footer (null) and never errors, which would stop
// Codex's reads.
func TestCodexStatuslineBalanceAndDown(t *testing.T) {
	now := time.Now()
	c := newCodexStatus(statuslineGateway(t, now).URL)
	if got := c.cells(""); len(got) != 1 || got[0] != "claude a@example.com 5h 62% 7d 41%" {
		t.Fatalf("no thread: %q", got)
	}
	down := newCodexStatus("http://127.0.0.1:1")
	got := mcpTalk(t, down, codexRead)
	res, _ := got["3"]["result"].(map[string]any)
	cs, _ := res["contents"].([]any)
	if len(cs) != 1 || cs[0].(map[string]any)["text"] != "null" {
		t.Fatalf("gateway down: %v", got["3"])
	}
}

// statusline --codex takes nothing else, and a flag it doesn't know is
// refused, never read as an account.
func TestStatuslineFlags(t *testing.T) {
	for _, args := range [][]string{{"statusline", "--codex", "x"}, {"statusline", "--nope"}, {"statusline", "--help"}} {
		if err := statuslineCmd(args); err == nil || !strings.Contains(err.Error(), "usage") {
			t.Fatalf("%q: %v", args, err)
		}
	}
}
