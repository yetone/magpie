package gateway

import "testing"

// Claude Code's notices that the tool list changed, after a tool's result:
// the deferred tools now available, or no longer, and an MCP server's
// instructions. They are the agent's. Told to the run as the user's message
// the model read "The user sent a new message" of nothing it was asked, and
// a steer sent with them was lost among them (magicalaciw18 on X).
func TestClaudeToolListNoticesAreNotTheUsersMessage(t *testing.T) {
	for name, text := range map[string]string{
		// the words of Claude Code 2.1.x, as its transcripts hold them
		"deferred":   `<system-reminder>\nThe following deferred tools are now available via ToolSearch. Their schemas are NOT loaded — calling them directly will fail:\nmcp__a__b\n</system-reminder>`,
		"gone":       `<system-reminder>\nThe following deferred tools are no longer available (their MCP server disconnected). Do not search for them — ToolSearch will return no match:\nmcp__a__b\n</system-reminder>`,
		"gone-quiet": `<system-reminder>\nThe following deferred tools are no longer available in this session. Do not search for them — ToolSearch will return no match:\nmcp__a__b\n</system-reminder>`,
		"mcp":        `# MCP Server Instructions\n\nThe following MCP servers have provided instructions for how to use their tools and resources:\n\n## a\nuse it`,
		"failed":     `<system-reminder>\nThe following MCP servers are configured but failed to connect — their tools (typically named mcp__<server>__*) are unavailable:\n- a\n</system-reminder>`,
		"connecting": `<system-reminder>\nThe following MCP servers are still connecting — their tools (typically named mcp__<server>__*) are not yet available but will be:\n- a\n</system-reminder>`,
		"disconnect": `<system-reminder>\nThe following MCP servers have disconnected. Their instructions above no longer apply:\n- a\n</system-reminder>`,
	} {
		t.Run(name, func(t *testing.T) {
			// the notice beside the result, in the results' message
			body := `{"model":"claude-sonnet-5","max_tokens":64,"messages":[` +
				`{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"Read","input":{"file_path":"a.go"}}]},` +
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"package a"},{"type":"text","text":"` + text + `"}]}]}`
			_, said, _ := continuing(t, body)
			if len(said) != 0 {
				t.Fatalf("Claude Code was written %q as the user's", said)
			}
		})
	}
}

// A steer sent among such a notice is the user's, alone.
func TestClaudeSteerBesideToolListNoticeGoesOnAlone(t *testing.T) {
	notice := `<system-reminder>\nThe following deferred tools are now available via ToolSearch. Their schemas are NOT loaded:\nmcp__a__b\n</system-reminder>`
	body := `{"model":"claude-sonnet-5","max_tokens":64,"messages":[` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"Read","input":{"file_path":"a.go"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"package a"},{"type":"text","text":"` + notice + `"},{"type":"text","text":"像心理学家一样剖析我"}]}]}`
	_, said, _ := continuing(t, body)
	if len(said) != 1 || said[0] != "像心理学家一样剖析我" {
		t.Fatalf("Claude Code was written %q, want the steer alone", said)
	}
}

// A steer with nothing beside it still reaches Claude as the user's message:
// as a text after the result, and as Claude Code 2.1.296's system message.
func TestClaudeLoneSteerReachesClaude(t *testing.T) {
	head := `{"model":"claude-sonnet-5","max_tokens":64,"messages":[` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"Read","input":{"file_path":"a.go"}}]},`
	for name, body := range map[string]string{
		"text": head + `{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"package a"},{"type":"text","text":"像心理学家一样剖析我"}]}]}`,
		"system": head + `{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"package a"}]},` +
			`{"role":"system","content":[{"type":"text","text":"The user sent a new message while you were working:\n像心理学家一样剖析我\n\nIMPORTANT: After completing your current task, you MUST address the user's message above."}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, said, _ := continuing(t, body)
			if len(said) != 1 || said[0] != "像心理学家一样剖析我" {
				t.Fatalf("Claude Code was written %q, want the steer", said)
			}
		})
	}
}
