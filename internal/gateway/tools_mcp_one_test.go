package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gwtool"
	"github.com/yetone/magpie/internal/plugin"
)

func toolsPostTo(h http.Handler, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.RemoteAddr, r.Host = "127.0.0.1:50000", "127.0.0.1:3425"
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// One plugin's tools are served at ToolsMCPOne<its short name>, each by
// its own name: the address of the Library server the library gives a
// plugin with tools (chenxiang0010: Search1API in the MCP hub). Another
// plugin's tool isn't listed or run there, and the hub's way in
// (PluginToolsOf, RunPluginToolOf) finds the plugin by its name, whatever
// version its spec pins.
func TestToolsMCPOnePlugin(t *testing.T) {
	fresh(t)
	ran := fakePluginTools(t, []plugin.PluginTools{
		{Spec: "@magpie-community/tools-search1api@0.1.1", Tools: []plugin.Tool{
			{Name: "search", Description: "Search the web.", Schema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)},
			{Name: "crawl", Description: "A page."},
		}},
		{Spec: "opencode-weather-tools", Tools: []plugin.Tool{{Name: "now"}}},
	})
	h := lanGuard(New().Handler())
	at := ToolsMCPOne + "search1api"
	m := readMCP(t, toolsPostTo(h, at, toolsList))
	var names []string
	for _, x := range m.Result.Tools {
		names = append(names, x.Name)
	}
	if strings.Join(names, " ") != "search crawl" {
		t.Fatalf("search1api's tools = %v (%+v)", names, m)
	}
	m = readMCP(t, toolsPostTo(h, at, toolsCall("search", `{"query":"magpie"}`)))
	if m.Result.IsError || len(m.Result.Content) != 1 || m.Result.Content[0].Text != "ran search" {
		t.Fatalf("call = %+v", m)
	}
	if m := readMCP(t, toolsPostTo(h, at, toolsCall("now", `{}`))); m.Error == nil || m.Error.Code != -32602 {
		t.Errorf("another plugin's tool at search1api's address: %+v", m)
	}
	if m := readMCP(t, toolsPostTo(h, ToolsMCPOne+"nope", toolsList)); len(m.Result.Tools) != 0 {
		t.Errorf("a plugin not on lists %+v", m)
	}

	ctx := context.Background()
	ts, err := PluginToolsOf(ctx, "@magpie-community/tools-search1api")
	if err != nil || len(ts) != 2 {
		t.Fatalf("PluginToolsOf = %v %v", ts, err)
	}
	if said, err := RunPluginToolOf(ctx, "@magpie-community/tools-search1api", "crawl", json.RawMessage(`{}`), "claude"); err != nil || said != "ran crawl" {
		t.Fatalf("RunPluginToolOf = %q %v", said, err)
	}
	if _, err := RunPluginToolOf(ctx, "@magpie-community/tools-search1api", "now", json.RawMessage(`{}`), "claude"); err == nil {
		t.Error("RunPluginToolOf ran another plugin's tool")
	}
	if got := (*ran)[len(*ran)-1]; got != `@magpie-community/tools-search1api@0.1.1 crawl {} claude` {
		t.Errorf("ran %q", got)
	}

	// off, nothing is listed or run
	stop := gwtool.Gate()
	defer stop()
	if ts, _ := PluginToolsOf(ctx, "@magpie-community/tools-search1api"); len(ts) != 0 {
		t.Errorf("off, PluginToolsOf = %v", ts)
	}
	if _, err := RunPluginToolOf(ctx, "@magpie-community/tools-search1api", "search", nil, ""); err == nil || !strings.Contains(err.Error(), "Plugin tools built-in is off") {
		t.Errorf("off, RunPluginToolOf: %v", err)
	}
}
