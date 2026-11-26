package usage

import (
	"slices"
	"testing"
	"time"
)

// ferock on Discord: an agent's call to a tool of magpie's MCP servers
// (generate_image) is a row of the request log of its own, apart from
// the model requests, picked by the MCP tool purpose, naming the tool,
// its server, the provider and model it used, how long it took and how it
// ended, and counted in nothing: its image request is the call that is.
func TestMCPCallRowsListedNotCounted(t *testing.T) {
	reportedHome(t)
	spy := &ledgerSpy{}
	t.Cleanup(Observe(spy))
	now := time.Now().Add(-time.Hour)
	Append(Record{Time: now, Agent: "claude", Provider: "openai", Host: "api.openai.com", Model: "gpt-image-2", Requested: "gpt-image-2", Operation: "generate_content", Endpoint: "/v1/images/generations", Status: 200, Millis: 9000})
	Append(Record{Time: now.Add(time.Second), Agent: "claude", Operation: "execute_tool", Provider: "openai", Model: "gpt-image-2", Requested: "generate_image",
		Host: "magpie-image", Millis: 9100, Endpoint: "/mcp/magpie/image", Kind: KindMCP, Source: SourceMCP})
	Append(Record{Time: now.Add(2 * time.Second), Agent: "claude", Operation: "execute_tool", Requested: "github__create_issue",
		Host: "github", Millis: 40, Error: "it didn't answer", Endpoint: "/mcp/magpie/hub/claude", Kind: KindMCP, Source: SourceMCP})

	page := QueryPage(All, Filter{}, 0, 50)
	if page.Total != 3 || len(page.Rows) != 3 {
		t.Fatalf("total %d, %d rows: %+v", page.Total, len(page.Rows), page.Rows)
	}
	hub, image := page.Rows[0], page.Rows[1] // newest first
	if hub.Source != SourceMCP || !hub.IsProbe() || hub.Requested != "github__create_issue" || hub.Host != "github" || !hub.Failed() || hub.Provider != "" {
		t.Fatalf("hub row %+v", hub)
	}
	if image.Source != SourceMCP || image.Requested != "generate_image" || image.Provider != "openai" || image.Model != "gpt-image-2" ||
		image.Host != "magpie-image" || image.Millis != 9100 || image.Failed() || image.Endpoint != "/mcp/magpie/image" {
		t.Fatalf("image row %+v", image)
	}
	if page.Sum.Calls != 1 || page.Sum.Errors != 0 || page.Through.Calls != 1 {
		t.Fatalf("sum %+v through %+v", page.Sum, page.Through)
	}
	if !slices.Contains(page.Purposes, "kind:mcp") || PurposeOf(KindMCP) != "kind:mcp" || PurposeKinds()[KindMCP].Name != "MCP tool" {
		t.Fatalf("purposes %v", page.Purposes)
	}
	for dim, shares := range page.By {
		for _, s := range shares {
			if s.ID == "generate_image" || s.ID == "github__create_issue" || (dim == "provider" && s.Calls != 1) {
				t.Fatalf("%s counts a tool call: %+v", dim, shares)
			}
		}
	}
	if s := Summarize(All); s.Calls != 1 || s.Errors != 0 {
		t.Fatalf("summary %+v", s.Totals)
	}
	if rs := Load(time.Time{}); len(rs) != 1 {
		t.Fatalf("Load %+v", rs)
	}
	if len(spy.rows) != 1 || spy.rows[0].IsProbe() {
		t.Fatalf("observer saw %+v", spy.rows)
	}
	// the purpose picks the tool calls alone, and counts nothing still
	p := QueryPage(All, Filter{Purpose: "kind:mcp"}, 0, 50)
	if p.Total != 2 || p.Sum.Calls != 0 || p.Rows[0].Source != SourceMCP || p.Rows[1].Source != SourceMCP {
		t.Fatalf("purpose filter: total %d sum %+v rows %+v", p.Total, p.Sum, p.Rows)
	}
	// a tool call's server is no provider's host: a probe on the host is
	// not listed under the provider the tool used
	if hosts := readLogSnapshot().hosts; hosts["magpie-image"] != "" || hosts["github"] != "" {
		t.Fatalf("hosts %v", hosts)
	}
}

// tomtanir and lyl50478600 on X: how many times, and how often, each MCP
// tool was called. The page of the MCP tool purpose counts its calls by
// server and tool, the most called first, with those that failed and a
// rate a day from the tool's first call; a page of anything else counts
// none, and the agent picked is kept to.
func TestMCPToolCallsCounted(t *testing.T) {
	reportedHome(t)
	// noon, so the calls of the last hour are today's
	y, m, d := time.Now().Date()
	now := holdClock(t, time.Date(y, m, d, 12, 0, 0, 0, time.Local))
	call := func(at time.Time, agent, server, tool, failed string) {
		Append(Record{Time: at, Agent: agent, Operation: "execute_tool", Requested: tool, Host: server, Millis: 30, Error: failed,
			Endpoint: "/mcp/magpie/hub/" + agent, Kind: KindMCP, Source: SourceMCP})
	}
	day := 24 * time.Hour
	for i := range 6 {
		call(now.Add(-4*day+time.Duration(i)*time.Hour), "claude", "github", "create_issue", "")
	}
	call(now.Add(-2*day), "claude", "github", "create_issue", "it didn't answer")
	call(now.Add(-time.Hour), "claude", "fetch", "fetch", "")
	call(now.Add(-time.Minute), "codex", "fetch", "fetch", "")
	Append(Record{Time: now.Add(-time.Minute), Agent: "claude", Provider: "openai", Model: "gpt-5", Requested: "gpt-5", Status: 200, Input: 10, Output: 5})

	p := QueryPage(All, Filter{Purpose: "kind:mcp"}, 0, 50)
	if len(p.Tools) != 2 {
		t.Fatalf("tools %+v", p.Tools)
	}
	gh, fetch := p.Tools[0], p.Tools[1]
	if gh.Server != "github" || gh.Tool != "create_issue" || gh.Calls != 7 || gh.Failed != 1 || !gh.Last.Equal(now.Add(-2*day)) {
		t.Errorf("github %+v", gh)
	}
	if d := gh.PerDay; d < 7/4.01 || d > 7/3.99 {
		t.Errorf("github is called %v a day, want 7 over 4 days", d)
	}
	if fetch.Server != "fetch" || fetch.Tool != "fetch" || fetch.Calls != 2 || fetch.Failed != 0 || fetch.PerDay != 2 {
		t.Errorf("fetch %+v (called within the day: 2 a day)", fetch)
	}

	// the agent picked: its calls alone
	p = QueryPage(All, Filter{Purpose: "kind:mcp", Agent: "codex"}, 0, 50)
	if len(p.Tools) != 1 || p.Tools[0].Tool != "fetch" || p.Tools[0].Calls != 1 {
		t.Errorf("codex's tools %+v", p.Tools)
	}
	// a day picked: that day's calls, the rate of the one day
	p = QueryPage(All, Filter{Purpose: "kind:mcp", Day: now.Add(-2 * day).Format(time.DateOnly)}, 0, 50)
	if len(p.Tools) != 1 || p.Tools[0].Calls != 1 || p.Tools[0].Failed != 1 || p.Tools[0].PerDay != 1 {
		t.Errorf("the day's tools %+v", p.Tools)
	}
	// the period: today holds fetch's two alone
	p = QueryPage(Today, Filter{Purpose: "kind:mcp"}, 0, 50)
	if len(p.Tools) != 1 || p.Tools[0].Tool != "fetch" || p.Tools[0].Calls != 2 {
		t.Errorf("the last day's tools %+v", p.Tools)
	}
	// any other page counts no tools
	if p := QueryPage(All, Filter{}, 0, 50); p.Tools != nil {
		t.Errorf("the page of every purpose counts tools: %+v", p.Tools)
	}
}
