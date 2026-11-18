package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/gwtool"
	"github.com/yetone/magpie/internal/plugin"
)

// fakePluginTools has the gateway's plugin tools be pts and record the
// calls run, with no plugin host.
func fakePluginTools(t *testing.T, pts []plugin.PluginTools) *[]string {
	t.Helper()
	var ran []string
	listed := 0
	oldList, oldRun, oldOn := pluginTools, runPluginTool, anyPluginOn
	pluginTools = func(context.Context) ([]plugin.PluginTools, error) { listed++; return pts, nil }
	runPluginTool = func(_ context.Context, spec, name string, args json.RawMessage, agent string) (string, error) {
		ran = append(ran, spec+" "+name+" "+string(args)+" "+agent)
		return "ran " + name, nil
	}
	anyPluginOn = func() bool { return true }
	t.Cleanup(func() { pluginTools, runPluginTool, anyPluginOn = oldList, oldRun, oldOn })
	return &ran
}

func toolsPost(h http.Handler, body string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, ToolsMCPPath, strings.NewReader(body))
	r.RemoteAddr, r.Host = "127.0.0.1:50000", "127.0.0.1:3425"
	r.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

const toolsList = `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`

func toolsCall(name, args string) string {
	return `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
}

// The plugin tools MCP server lists the tools of the plugins that are on,
// each as <plugin>_<tool> with its JSON Schema, two of one name told
// apart, and runs one in the plugin that gives it with the agent's
// arguments, naming the agent. A name no plugin gives is an error, and a
// web page is turned away.
func TestToolsMCPServesPluginTools(t *testing.T) {
	fresh(t)
	ran := fakePluginTools(t, []plugin.PluginTools{
		{Spec: "@acme/opencode-weather-tools@1.2.0", Tools: []plugin.Tool{
			{Name: "now", Description: "The weather now.", Schema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)},
		}},
		{Spec: "/Users/me/weather-tools", Tools: []plugin.Tool{{Name: "now", Description: "Mine."}}},
	})
	h := lanGuard(New().Handler())
	if m := readMCP(t, toolsPost(h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)); m.Result.ServerInfo.Name != "magpie-tools" || m.Result.ProtocolVersion != "2025-03-26" {
		t.Fatalf("initialize = %+v", m)
	}
	m := readMCP(t, toolsPost(h, toolsList))
	var names []string
	for _, x := range m.Result.Tools {
		names = append(names, x.Name)
	}
	if strings.Join(names, " ") != "weather-tools_now weather-tools_now_2" {
		t.Fatalf("tools = %v", names)
	}
	if r := m.Result.Tools[0].InputSchema.Required; len(r) != 1 || r[0] != "city" {
		t.Errorf("schema required = %v", r)
	}
	m = readMCP(t, toolsPost(h, toolsCall("weather-tools_now_2", `{"city":"Oslo"}`), "User-Agent", "claude-cli/2.1.0"))
	if m.Result.IsError || len(m.Result.Content) != 1 || m.Result.Content[0].Text != "ran now" {
		t.Fatalf("call = %+v", m)
	}
	if len(*ran) != 1 || !strings.HasPrefix((*ran)[0], `/Users/me/weather-tools now {"city":"Oslo"} `) {
		t.Errorf("ran %q", *ran)
	}
	if m := readMCP(t, toolsPost(h, toolsCall("nope_now", `{}`))); m.Error == nil || m.Error.Code != -32602 {
		t.Errorf("an unknown tool: %+v", m)
	}
	if w := toolsPost(h, toolsList, "Origin", "https://evil.example"); w.Code != http.StatusForbidden {
		t.Errorf("a web page: %d", w.Code)
	}
	if len(*ran) != 1 {
		t.Errorf("ran %q", *ran)
	}
}

// Off (plugins/tools not serving the tool point in a gateway that gates
// it), the server lists no tool and runs none, and says the plugin is off;
// the plugin host isn't asked. With no plugin on at all, it isn't started
// to list nothing.
func TestToolsMCPOff(t *testing.T) {
	fresh(t)
	ran := fakePluginTools(t, []plugin.PluginTools{{Spec: "opencode-x", Tools: []plugin.Tool{{Name: "y"}}}})
	asked := 0
	pluginTools = func(context.Context) ([]plugin.PluginTools, error) {
		asked++
		return []plugin.PluginTools{{Spec: "opencode-x", Tools: []plugin.Tool{{Name: "y"}}}}, nil
	}
	h := lanGuard(New().Handler())
	stop := gwtool.Gate()
	defer stop()
	if m := readMCP(t, toolsPost(h, toolsList)); len(m.Result.Tools) != 0 || m.Error != nil {
		t.Errorf("off, listed %+v", m)
	}
	m := readMCP(t, toolsPost(h, toolsCall("x_y", `{}`)))
	if !m.Result.IsError || len(m.Result.Content) == 0 || !strings.Contains(m.Result.Content[0].Text, "Plugin tools built-in is off") {
		t.Errorf("off, a call said %+v", m)
	}
	if len(*ran) != 0 || asked != 0 {
		t.Errorf("off: ran %q, the host asked %d times", *ran, asked)
	}
	on := gwtool.Serve(gwtool.PluginTools)
	if m := readMCP(t, toolsPost(h, toolsList)); len(m.Result.Tools) != 1 {
		t.Errorf("on again, listed %+v", m)
	}
	anyPluginOn = func() bool { return false }
	asked = 0
	if m := readMCP(t, toolsPost(h, toolsList)); len(m.Result.Tools) != 0 || asked != 0 {
		t.Errorf("no plugin on: listed %+v, the host asked %d times", m, asked)
	}
	on()
}

// A gateway key's limit holds each tool call: a key held to a limit
// nothing serves (the Key limits plugin off) is refused, and the tool
// isn't run.
func TestToolsMCPHeldToKeyLimit(t *testing.T) {
	fresh(t)
	ran := fakePluginTools(t, []plugin.PluginTools{{Spec: "opencode-x", Tools: []plugin.Tool{{Name: "y"}}}})
	withoutLimits(t)
	keys, _ := newCaller(t, "Capped")
	setLimit(t, keys[0].ID, &access.Limit{Period: "day", Tokens: 700})
	capped := access.Identity{KeyID: keys[0].ID, KeyName: "Capped", Limit: &access.Limit{Period: "day", Tokens: 700}}
	s := New()
	r := httptest.NewRequest("POST", ToolsMCPPath, nil).WithContext(access.WithIdentity(t.Context(), capped))
	res, rpcErr := s.toolsMCPCall(r, mcpMessage{ID: json.RawMessage("1"), Method: "tools/call", Params: json.RawMessage(`{"name":"x_y","arguments":{}}`)})
	b, _ := json.Marshal(res)
	if rpcErr != nil || !strings.Contains(string(b), `"isError":true`) || !strings.Contains(string(b), "Key limits built-in") {
		t.Errorf("a limited key with limits off: %s %v", b, rpcErr)
	}
	if len(*ran) != 0 {
		t.Errorf("ran %q", *ran)
	}
}
