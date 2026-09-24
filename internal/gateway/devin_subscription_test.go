package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDevinEnvRunsInMagpiesHome(t *testing.T) {
	got := strings.Join(devinEnv([]string{
		"PATH=/bin", "HOME=/Users/me", "XDG_CONFIG_HOME=/x", "XDG_DATA_HOME=/y",
		"DEVIN_API_KEY=k", "DEVIN_MODEL=x", "WINDSURF_API_KEY=w", "KEEP=1",
	}, "/cache/home"), "\n")
	for _, bad := range []string{"HOME=/Users/me", "XDG_CONFIG_HOME=/x", "XDG_DATA_HOME=/y", "DEVIN_API_KEY=", "DEVIN_MODEL=", "WINDSURF_API_KEY="} {
		if strings.Contains(got, bad) {
			t.Fatalf("kept %s in %q", bad, got)
		}
	}
	for _, want := range []string{"PATH=/bin", "KEEP=1", "HOME=/cache/home", "XDG_CONFIG_HOME=/cache/home/.config"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %q", want, got)
		}
	}
}

// devinPair wires a conn's stdin/stdout to pipes the test speaks ACP on. The
// conn's stdin is a kernel pipe: like a real process's, its writes don't wait
// for the test to read them.
type devinPair struct {
	conn   *devinConn
	toConn io.WriteCloser // what the test writes, the conn reads
	from   *bufio.Scanner // what the conn writes, the way devin would see it
	fromR  *os.File
	run    *subscriptionRun
	seg    chan Event
}

func newDevinPair(t *testing.T) devinPair {
	t.Helper()
	toConnR, toConnW := io.Pipe() // test writes, conn reads
	fromR, fromW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fromR.Close(); fromW.Close() })
	run := &subscriptionRun{pending: map[string]chan mcpToolResult{}}
	from := bufio.NewScanner(fromR)
	from.Buffer(make([]byte, 64<<10), 4<<20)
	p := devinPair{conn: &devinConn{stdin: fromW, pending: map[int64]chan devinReply{}, run: run},
		toConn: toConnW, from: from, fromR: fromR, run: run, seg: run.attach()}
	go p.conn.read(toConnR)
	return p
}

func (p devinPair) write(t *testing.T, line string) {
	t.Helper()
	if _, err := fmt.Fprintln(p.toConn, line); err != nil {
		t.Fatal(err)
	}
}

// next returns the conn's next outbound JSON-RPC message.
func (p devinPair) next(t *testing.T) map[string]any {
	t.Helper()
	_ = p.fromR.SetReadDeadline(time.Now().Add(5 * time.Second))
	if !p.from.Scan() {
		t.Fatal("conn went quiet")
	}
	_ = p.fromR.SetReadDeadline(time.Time{})
	var m map[string]any
	if json.Unmarshal(p.from.Bytes(), &m) != nil {
		t.Fatalf("not json: %s", p.from.Text())
	}
	return m
}

func TestDevinConnCallsAndAnswers(t *testing.T) {
	p := newDevinPair(t)
	id, err := p.conn.send("initialize", map[string]any{"protocolVersion": 1})
	if err != nil {
		t.Fatal(err)
	}
	sent := p.next(t)
	if sent["method"] != "initialize" || sent["id"] != float64(id) {
		t.Fatalf("sent %v", sent)
	}
	// a response resolves the call
	p.write(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":1}}`, id))
	res, err := p.conn.await(context.Background(), id)
	if err != nil || !strings.Contains(string(res), "protocolVersion") {
		t.Fatalf("await: %v %s", err, res)
	}
	// a permission request is declined on its own
	p.write(t, `{"jsonrpc":"2.0","id":77,"method":"session/request_permission","params":{}}`)
	ans := p.next(t)
	if ans["id"] != float64(77) {
		t.Fatalf("answer %v", ans)
	}
	outcome, _ := ans["result"].(map[string]any)["outcome"].(map[string]any)
	if outcome["outcome"] != "cancelled" {
		t.Fatalf("permission answered %v", ans)
	}
	// a client-capability request is refused as unimplemented
	p.write(t, `{"jsonrpc":"2.0","id":78,"method":"fs/read_text_file","params":{"path":"/x"}}`)
	ans = p.next(t)
	if e, _ := ans["error"].(map[string]any); e == nil || e["code"] != float64(-32601) {
		t.Fatalf("fs answered %v", ans)
	}
	// streamed text lands as events
	p.write(t, `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello"}}}}`)
	p.write(t, `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"hm"}}}}`)
	var kinds []EventKind
	for len(kinds) < 2 {
		select {
		case ev := <-p.seg:
			kinds = append(kinds, ev.Kind)
		case <-time.After(2 * time.Second):
			t.Fatalf("events: %v", kinds)
		}
	}
	if kinds[0] != KText || kinds[1] != KThink {
		t.Fatalf("events: %v", kinds)
	}
	// the stream ending fails a call still open
	id2, _ := p.conn.send("session/prompt", map[string]any{})
	p.toConn.Close()
	if _, err := p.conn.await(context.Background(), id2); err == nil {
		t.Fatal("an open call survived the end of the stream")
	}
}

func TestStopFromACP(t *testing.T) {
	for in, want := range map[string]string{
		"end_turn": "stop", "max_tokens": "length", "max_turn_requests": "length", "refusal": "filter", "": "stop",
	} {
		if got := stopFromACP(in); got != want {
			t.Fatalf("%q → %q", in, got)
		}
	}
}

func TestRenderDevinPromptMapsBlocks(t *testing.T) {
	blocks, err := renderDevinPrompt(&Request{
		Messages: []Message{
			{Role: "user", Parts: []Part{{Kind: Text, Text: "look"}, {Kind: Image, MediaType: "image/png", Data: "AA=="}, {Kind: Image, URL: "https://x/y.png"}}},
		},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, b := range blocks {
		kinds = append(kinds, b["type"].(string))
	}
	if kinds[0] != "text" || !strings.Contains(blocks[0]["text"].(string), "external_system_instructions") {
		t.Fatalf("preamble: %v", blocks)
	}
	var img, link map[string]any
	for _, b := range blocks {
		if b["type"] == "image" {
			img = b
		}
		if b["type"] == "resource_link" {
			link = b
		}
	}
	if img["data"] != "AA==" || img["mimeType"] != "image/png" || link["uri"] != "https://x/y.png" {
		t.Fatalf("blocks: %v", blocks)
	}
}
