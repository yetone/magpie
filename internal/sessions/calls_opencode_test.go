package sessions

import (
	"path/filepath"
	"testing"
	"time"
)

// ocCalls are the OpenCode calls Calls gives, by message id.
func ocCalls(t *testing.T) map[string]Call {
	t.Helper()
	out := map[string]Call{}
	for _, c := range Calls(time.Time{}) {
		if c.Agent == "opencode" {
			if _, dup := out[c.Msg]; dup {
				t.Fatalf("message %s counted twice", c.Msg)
			}
			out[c.Msg] = c
		}
	}
	return out
}

// checkOCCalls checks the fixtures' replies as calls: the one through
// magpie's gateway (msg_0003, provider "magpie") left to the gateway's log,
// the subagent's in the session it ran under, the reasoning in the output.
func checkOCCalls(t *testing.T, cs map[string]Call) {
	t.Helper()
	if _, ok := cs["msg_0003"]; ok {
		t.Fatalf("a reply through magpie's gateway counted again: %+v", cs["msg_0003"])
	}
	a, ok := cs["msg_0002"]
	if !ok {
		t.Fatalf("no call for OpenCode's own reply: %+v", cs)
	}
	if a.Model != "gpt-6-astra" || a.Upstream != "openai" || a.Session != ocMain || a.Cwd != "/work/oc" ||
		a.Tokens != (Tokens{1000, 120, 5000, 0}) || a.Reasoning != 20 || a.Millis != 24000 ||
		!a.Time.Equal(at("2026-09-26T10:00:30Z")) {
		t.Fatalf("astra: %+v", a)
	}
	// the effort picked in OpenCode's model menu, its reply's variant (#680)
	if a.Effort != "high" {
		t.Fatalf("astra's effort %q, want high", a.Effort)
	}
	o, ok := cs["msg_0004"]
	if !ok || o.Model != "claude-opus-5-5" || o.Upstream != "anthropic" || o.Session != ocMain ||
		o.Tokens != (Tokens{40, 60, 700, 300}) || o.Millis != 30000 || o.Effort != "" {
		t.Fatalf("the subagent's opus: %+v", o)
	}
	if len(cs) != 2 {
		t.Fatalf("want 2 OpenCode calls, got %+v", cs)
	}
}

func TestOpenCodeCallsJSON(t *testing.T) {
	setupMore(t)
	checkOCCalls(t, ocCalls(t))
}

func TestOpenCodeCallsDB(t *testing.T) {
	data, _ := setupMore(t)
	db := ocMakeDB(t, filepath.Join(data, "opencode", "opencode.db"))
	checkOCCalls(t, ocCalls(t))

	// a session that hasn't changed (no message added, none updated) is
	// not read again: what was read is kept
	if _, err := db.Exec(`UPDATE message SET data = json_set(data, '$.tokens.input', 9999) WHERE id = 'msg_0002'`); err != nil {
		t.Fatal(err)
	}
	if a := ocCalls(t)["msg_0002"]; a.Input != 1000 {
		t.Fatalf("an unchanged session read again: %+v", a)
	}
	// nor after a restart: the call cache on disk has it
	resetCalls()
	if a := ocCalls(t)["msg_0002"]; a.Input != 1000 {
		t.Fatalf("the call cache wasn't kept: %+v", a)
	}
	// a message updated as its reply goes on is read again
	if _, err := db.Exec(`UPDATE message SET time_updated = time_updated + 1 WHERE id = 'msg_0002'`); err != nil {
		t.Fatal(err)
	}
	if a := ocCalls(t)["msg_0002"]; a.Input != 9999 {
		t.Fatalf("an updated reply: %+v", a)
	}

	// a failed reply is a failed call; one only stopped is none
	ocInsert(t, db, ocChild, "msg_0005", 1790416990000, 1790417000000,
		`{"id":"msg_0005","role":"assistant","modelID":"claude-opus-5-5","providerID":"anthropic","variant":"default","tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}},"error":{"name":"APIError","data":{"message":"Overloaded","statusCode":529}},"time":{"created":1790416990000,"completed":1790417000000}}`)
	ocInsert(t, db, ocChild, "msg_0006", 1790417010000, 1790417020000,
		`{"id":"msg_0006","role":"assistant","modelID":"claude-opus-5-5","providerID":"anthropic","tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}},"error":{"name":"MessageAbortedError","data":{"message":"aborted"}},"time":{"created":1790417010000}}`)
	cs := ocCalls(t)
	// "default" is no effort picked
	if e := cs["msg_0005"]; e.Error != "APIError" || e.ErrorText != "Overloaded" || e.Session != ocMain || e.Effort != "" {
		t.Fatalf("a failed reply: %+v", e)
	}
	if _, ok := cs["msg_0006"]; ok {
		t.Fatalf("a stopped reply counted: %+v", cs["msg_0006"])
	}
}

func TestOpenCodeCallsV2(t *testing.T) {
	data, _ := setupMore(t)
	db := ocMakeV2(t, filepath.Join(data, "opencode", "opencode.db"))
	checkOCCalls(t, ocCalls(t))

	// a compaction after a reply through magpie went through magpie too
	if _, err := db.Exec(`INSERT INTO session_message VALUES ('msg_0005', ?, 'compaction', 9, 1790417000000, 1790417010000, ?)`, ocMain,
		`{"status":"completed","reason":"auto","cost":0,"tokens":{"input":5,"output":1,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":1790417000000}}`); err != nil {
		t.Fatal(err)
	}
	// and one after OpenCode's own reply is OpenCode's
	if _, err := db.Exec(`INSERT INTO session_message VALUES ('msg_0007', ?, 'assistant', 10, 1790417020000, 1790417030000, ?), ('msg_0008', ?, 'compaction', 11, 1790417040000, 1790417050000, ?)`,
		ocMain, `{"model":{"id":"big-pickle","providerID":"opencode","variant":"xhigh"},"content":[],"cost":0,"tokens":{"input":7,"output":3,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":1790417020000,"completed":1790417030000}}`,
		ocMain, `{"status":"completed","reason":"auto","cost":0,"tokens":{"input":11,"output":2,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":1790417040000}}`); err != nil {
		t.Fatal(err)
	}
	cs := ocCalls(t)
	if _, ok := cs["msg_0005"]; ok {
		t.Fatalf("a compaction through magpie counted: %+v", cs["msg_0005"])
	}
	if c := cs["msg_0007"]; c.Model != "big-pickle" || c.Upstream != "opencode" || c.Tokens != (Tokens{7, 3, 0, 0}) || c.Effort != "xhigh" {
		t.Fatalf("an OpenCode Zen reply: %+v", c)
	}
	// a compaction is made with the model and effort last replied with
	if c := cs["msg_0008"]; c.Model != "big-pickle" || c.Upstream != "opencode" || c.Tokens != (Tokens{11, 2, 0, 0}) || c.Effort != "xhigh" {
		t.Fatalf("its compaction: %+v", c)
	}
}
