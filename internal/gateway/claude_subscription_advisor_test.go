package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// Claude Code's /advisor through a Claude subscription (recih on X,
// #6804, #6813): the run's Claude Code is offered magpie's tools only, so
// the advisor, Anthropic's server tool, was left out of the request and
// its instructions said to be void. magpie now runs it as it does for a
// vendor's model: it is read as the model's own advisor, its instructions
// kept as Claude Code wrote them, and an advisor magpie doesn't serve is
// still left out.
func TestBridgeRequestKeepsTheAdvisor(t *testing.T) {
	advisedSetup(t)
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	req, err := bridgeRequest(r, provider.Anthropic, "Claude Code", []byte(advisorTurn("claude/claude-sonnet-5-5", "adv/adv-pro")))
	if err != nil {
		t.Fatal(err)
	}
	if req.Advisor != "adv/adv-pro" {
		t.Fatalf("the advisor is %q", req.Advisor)
	}
	if words := allText(req); !strings.Contains(words, advisorNote) || strings.Contains(words, advisorGone) {
		t.Fatalf("the advisor's instructions were changed:\n%s", words)
	}
	for _, c := range []struct{ name, advisor string }{
		{"Claude Desktop", "adv/adv-pro"},
		{"Claude Code", "nobody/x"},
	} {
		req, err := bridgeRequest(r, provider.Anthropic, c.name, []byte(advisorTurn("claude/claude-sonnet-5-5", c.advisor)))
		if err != nil {
			t.Fatal(err)
		}
		if req.Advisor != "" {
			t.Fatalf("%s, advisor %s: advised by %q", c.name, c.advisor, req.Advisor)
		}
	}
}

// allText is every text part of req's messages.
func allText(req *Request) string {
	var b strings.Builder
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			if p.Kind == Text {
				b.WriteString(p.Text)
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

// The run's Claude Code calls magpie's advisor tool: magpie asks the
// advisor model, hands the advice back to the run, and tells the client
// as Anthropic's server does, a server_tool_use and an
// advisor_tool_result with the advice, never a call of a tool of its own.
// The advisor sees what the reply said before the call; a reply's fourth
// call is answered max_uses_exceeded without asking.
func TestClaudeTUIAdvisorAnsweredHere(t *testing.T) {
	u := advisedSetup(t)
	c := newTUICalls(t)
	q, err := bridgeRequest(httptest.NewRequest("POST", "/v1/messages", nil), provider.Anthropic, "Claude Code", []byte(advisorTurn("claude/claude-sonnet-5-5", "adv/adv-pro")))
	if err != nil {
		t.Fatal(err)
	}
	c.run.adviseOn(context.Background(), q, New().adviceRound)
	first := c.run.segment
	// a message's blocks are each a row of their own, by its id
	c.write(tuiRow("msg_a0", `{"type":"text","text":"Let me ask."}`, "tool_use", 4))
	var res []mcpToolResult
	for i := range advisorUses + 1 {
		id := "toolu_adv" + itoa(i)
		c.write(tuiRow("msg_a"+itoa(i), `{"type":"tool_use","id":"`+id+`","name":"mcp__magpie__advisor","input":{}}`, "tool_use", 2))
		res = append(res, c.adviceCall(id))
	}
	c.write(tuiRow("msg_end", `{"type":"text","text":"Done."}`, "end_turn", 2), tuiDone)
	text, calls, stop, _, raw := c.reply(first)
	if len(calls) != 0 || stop != "end_turn" || !strings.Contains(text, "Done.") {
		t.Fatalf("the reply %q %v %q:\n%s", text, calls, stop, raw)
	}
	for i, r := range res[:advisorUses] {
		if r.IsError || r.Content[0]["text"] != "Check the tests first." {
			t.Fatalf("call %d was answered %+v", i, r)
		}
	}
	if last := res[advisorUses]; !last.IsError || !strings.Contains(last.Content[0]["text"].(string), "max_uses_exceeded") {
		t.Fatalf("a call past the limit was answered %+v", last)
	}
	u.mu.Lock()
	asked := append([]string(nil), u.advisor...)
	u.mu.Unlock()
	if len(asked) != advisorUses || !strings.Contains(asked[0], "Let me ask.") {
		t.Fatalf("the advisor model was asked %d times: %v", len(asked), asked)
	}
	if strings.Contains(asked[0], "You have access to an `advisor` tool") || strings.Contains(asked[0], advisorGone) {
		t.Fatalf("the advisor was told of itself: %s", asked[0])
	}
	var uses, results, advice int
	for _, l := range strings.Split(raw, "\n") {
		data, ok := strings.CutPrefix(l, "data: ")
		if !ok {
			continue
		}
		b := gjson.Get(data, "content_block")
		switch {
		case b.Get("type").String() == "server_tool_use" && b.Get("name").String() == advisorTool:
			uses++
		case b.Get("type").String() == "advisor_tool_result":
			results++
			if b.Get("content.text").String() == "Check the tests first." {
				advice++
			}
		case b.Get("type").String() == "tool_use":
			t.Fatalf("the client was told a call of its own:\n%s", raw)
		}
	}
	if uses != advisorUses+1 || results != advisorUses+1 || advice != advisorUses {
		t.Fatalf("told %d uses, %d results, %d with the advice:\n%s", uses, results, advice, raw)
	}
}

// adviceCall makes Claude Code's call of the advisor tool and returns
// what the MCP helper is told.
func (c *tuiCalls) adviceCall(id string) mcpToolResult {
	c.t.Helper()
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"tool_call_id":"`+id+`","name":"advisor","arguments":{}}`))
	r.SetPathValue("token", "run")
	rec := httptest.NewRecorder()
	c.bridge.mcpCall(rec, r)
	var res mcpToolResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		c.t.Fatalf("the advisor tool said %q: %v", rec.Body.String(), err)
	}
	return res
}

// The advisor a Claude subscription's run calls is asked as the client's
// request, held to its gateway key, never as the run's own MCP call from
// this computer, which carries no key. A key held to the subscription's
// model alone that names another model as its advisor is not given that
// model through the advisor: the call fails, and the advisor's provider
// is never asked.
func TestClaudeTUIAdvisorHeldToTheCallersKey(t *testing.T) {
	u := advisedSetup(t)
	c := newTUICalls(t)
	q, err := bridgeRequest(httptest.NewRequest("POST", "/v1/messages", nil), provider.Anthropic, "Claude Code", []byte(advisorTurn("claude/claude-sonnet-5-5", "adv/adv-pro")))
	if err != nil {
		t.Fatal(err)
	}
	if q.Advisor != "adv/adv-pro" {
		t.Fatalf("the advisor is %q", q.Advisor)
	}
	held := access.WithIdentity(context.Background(), access.Identity{KeyID: "friend", KeyName: "Friend", Models: []string{"claude/claude-sonnet-5-5"}})
	c.run.adviseOn(held, q, New().adviceRound)
	c.write(tuiRow("msg_a0", `{"type":"text","text":"Let me ask."}`, "tool_use", 4))
	c.write(tuiRow("msg_a1", `{"type":"tool_use","id":"toolu_adv0","name":"mcp__magpie__advisor","input":{}}`, "tool_use", 2))
	res := c.adviceCall("toolu_adv0")
	c.write(tuiRow("msg_end", `{"type":"text","text":"Done."}`, "end_turn", 2), tuiDone)
	if !res.IsError || res.Content[0]["text"] == "Check the tests first." {
		t.Fatalf("a key held to the subscription's model was advised by adv/adv-pro: %+v", res)
	}
	u.mu.Lock()
	asked := len(u.advisor)
	u.mu.Unlock()
	if asked != 0 {
		t.Fatalf("the advisor's provider was asked %d times for a key that doesn't list it", asked)
	}
}

// The run's web search is asked as the client's request too: the
// searcher's spend counts against the client's gateway key and its limits
// hold it, never as the run's own MCP call, which carries no key.
func TestClaudeRunSearchAskedAsTheCaller(t *testing.T) {
	var asked []access.Identity
	run := &subscriptionRun{pending: map[string]chan mcpToolResult{}, searchName: "web_search"}
	run.search = func(ctx context.Context, query string) (string, []Hit, error) {
		asked = append(asked, access.Caller(ctx))
		return "found", nil, nil
	}
	b := &subscriptionBridge{runs: map[string]*subscriptionRun{"run": run}, calls: map[string]*subscriptionRun{}}
	friend := access.Identity{KeyID: "friend", KeyName: "Friend", Limit: &access.Limit{Period: "day", Tokens: 700}}
	q := &Request{Model: "claude/claude-sonnet-5-5"}
	run.adviseOn(access.WithIdentity(context.Background(), friend), q, New().adviceRound)
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"tool_call_id":"toolu_s","name":"web_search","arguments":{"query":"magpie"}}`))
	r.SetPathValue("token", "run")
	rec := httptest.NewRecorder()
	b.mcpCall(rec, r)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "found") {
		t.Fatalf("the search call was answered %d %s", rec.Code, rec.Body)
	}
	if len(asked) != 1 || asked[0].KeyID != "friend" || asked[0].Limit == nil {
		t.Fatalf("the search was asked as %+v, not the client's key", asked)
	}
}
