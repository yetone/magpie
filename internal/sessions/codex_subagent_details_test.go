package sessions

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func codexDetailsLine(t *testing.T, typ string, payload any) string {
	t.Helper()
	b, err := json.Marshal(struct {
		Timestamp string `json:"timestamp"`
		Type      string `json:"type"`
		Payload   any    `json:"payload"`
	}{callT0.Format(time.RFC3339Nano), typ, payload})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func codexDetailsRollout(t *testing.T, codex, id string, lines ...string) string {
	t.Helper()
	path := filepath.Join(codex, "sessions", "2026", "09", "20", "rollout-2026-09-20T10-00-00-"+id+".jsonl")
	writeLines(t, path, lines...)
	return path
}

func codexDetailsMeta(t *testing.T, id, session, parent, nickname string) string {
	t.Helper()
	payload := map[string]any{"id": id, "session_id": session, "source": "cli"}
	if parent != "" {
		payload["source"] = map[string]any{"subagent": map[string]any{"thread_spawn": map[string]string{"parent_thread_id": parent}}}
	}
	if nickname != "" {
		payload["agent_nickname"] = nickname
	}
	return codexDetailsLine(t, "session_meta", payload)
}

func codexDetailsSpawn(t *testing.T, callID, taskName string) string {
	t.Helper()
	args, err := json.Marshal(map[string]string{"task_name": taskName, "message": "anonymous fixture task body"})
	if err != nil {
		t.Fatal(err)
	}
	payload := struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		CallID    string `json:"call_id"`
		Arguments string `json:"arguments"`
	}{"function_call", "spawn_agent", callID, string(args)}
	return codexDetailsLine(t, "response_item", payload)
}

func codexDetailsSubagentEvent(t *testing.T, callID, childID string) string {
	return codexDetailsSubagentActivity(t, callID, childID, "started")
}

func codexDetailsSubagentActivity(t *testing.T, callID, childID, kind string) string {
	t.Helper()
	payload := struct {
		Type string `json:"type"`
		Item struct {
			Type          string `json:"type"`
			Kind          string `json:"kind"`
			ID            string `json:"id"`
			AgentThreadID string `json:"agent_thread_id"`
		} `json:"item"`
	}{Type: "item_completed"}
	payload.Item.Type, payload.Item.Kind = "SubAgentActivity", kind
	payload.Item.ID, payload.Item.AgentThreadID = callID, childID
	return codexDetailsLine(t, "event_msg", payload)
}

func TestCodexSubagentTaskTitleIgnoresOtherActivityKinds(t *testing.T) {
	d := setupCalls(t)
	codexDetailsRollout(t, d.codex, "main",
		codexDetailsMeta(t, "main", "shared-session", "", ""),
		codexDetailsSpawn(t, "call-interacted", "Task name from an interacted event"),
		codexDetailsSubagentActivity(t, "call-interacted", "child-interacted", "interacted"),
		codexDetailsSpawn(t, "call-completed", "Task name from a completed event"),
		codexDetailsSubagentActivity(t, "call-completed", "child-completed", "completed"),
		codexDetailsSpawn(t, "call-interrupted", "Task name from an interrupted event"),
		codexDetailsSubagentActivity(t, "call-interrupted", "child-interrupted", "interrupted"),
	)
	for i, id := range []string{"child-interacted", "child-completed", "child-interrupted"} {
		codexDetailsRollout(t, d.codex, id,
			codexDetailsMeta(t, id, "shared-session", "main", "Child"),
			codexDetailsUsage(t, id, i+1),
		)
		if got := Titles([]string{"codex:" + id})["codex:"+id]; got != "" {
			t.Fatalf("non-start activity named a child: %q", got)
		}
	}
}

func codexDetailsUsage(t *testing.T, id string, at int) string {
	t.Helper()
	payload := map[string]any{
		"response_id": "response-" + id,
		"session_id":  "shared-session",
		"thread_id":   id,
		"turn_id":     "turn-" + id,
		"usage":       map[string]int{"input_tokens": 12, "cached_input_tokens": 0, "cache_write_input_tokens": 0, "output_tokens": 3, "reasoning_output_tokens": 1, "total_tokens": 15},
	}
	b, err := json.Marshal(map[string]any{"timestamp": callT0.Add(time.Duration(at) * time.Second).Format(time.RFC3339Nano), "type": "token_usage_record", "payload": payload})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func codexDetailsSession(t *testing.T, id string) Session {
	t.Helper()
	for _, got := range List(All) {
		if got.Agent == "codex" && got.ID == id {
			return got
		}
	}
	t.Fatalf("Codex session %q is missing", id)
	return Session{}
}

func TestCodexSubagentTaskTitleAndNickname(t *testing.T) {
	d := setupCalls(t)
	codexDetailsRollout(t, d.codex, "main-a",
		codexDetailsMeta(t, "main-a", "shared-session", "", ""),
		codexDetailsSpawn(t, "call-child-b", "Use the explicit task name"),
		codexDetailsSubagentEvent(t, "call-child-b", "child-b"),
		codexDetailsSpawn(t, "call-child-c", "A task name must not replace a title"),
		codexDetailsSubagentEvent(t, "call-child-c", "child-c"),
		codexDetailsSpawn(t, "call-unmatched", "This task name has no exact child link"),
		codexDetailsSubagentEvent(t, "different-call-id", "child-d"),
	)
	codexDetailsRollout(t, d.codex, "child-b",
		codexDetailsMeta(t, "child-b", "shared-session", "main-a", "Scout"),
		codexDetailsUsage(t, "child-b", 1),
	)
	codexDetailsRollout(t, d.codex, "child-c",
		codexDetailsMeta(t, "child-c", "shared-session", "main-a", "Reviewer"),
		codexDetailsLine(t, "response_item", map[string]any{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": "Existing user title"}}}),
		codexDetailsUsage(t, "child-c", 2),
	)
	codexDetailsRollout(t, d.codex, "child-d",
		codexDetailsMeta(t, "child-d", "shared-session", "main-a", "Helper"),
		codexDetailsUsage(t, "child-d", 3),
	)

	// A child-only title lookup must refresh its confirmed parent as well.
	if got := Titles([]string{"codex:child-b"})["codex:child-b"]; got != "Use the explicit task name" {
		t.Fatalf("Titles did not resolve the linked parent task name: %q", got)
	}
	if got := codexDetailsSession(t, "child-b"); got.Title != "Use the explicit task name" || got.Nickname != "Scout" {
		t.Fatalf("linked task title and native nickname = %q / %q", got.Title, got.Nickname)
	}
	if got := codexDetailsSession(t, "child-c"); got.Title != "Existing user title" || got.Nickname != "Reviewer" {
		t.Fatalf("task name replaced an existing title or nickname was lost: %q / %q", got.Title, got.Nickname)
	}
	if got := codexDetailsSession(t, "child-d"); got.Title != "" || got.Nickname != "Helper" {
		t.Fatalf("unmatched task name was guessed or native nickname was lost: %q / %q", got.Title, got.Nickname)
	}

	stats := StatsAt(7, callT0.Add(24*time.Hour))
	for _, summary := range stats.Sessions {
		if summary.Agent != "codex" {
			continue
		}
		switch summary.ID {
		case "child-b":
			if summary.Title != "Use the explicit task name" || summary.Nickname != "Scout" {
				t.Errorf("summary lost child task details: %q / %q", summary.Title, summary.Nickname)
			}
		case "child-c":
			if summary.Title != "Existing user title" || summary.Nickname != "Reviewer" {
				t.Errorf("summary changed existing title or lost nickname: %q / %q", summary.Title, summary.Nickname)
			}
		}
	}
}

func TestCodexSubagentTaskTitleRefreshesWhenParentGrows(t *testing.T) {
	d := setupCalls(t)
	parent := codexDetailsRollout(t, d.codex, "main-a",
		codexDetailsMeta(t, "main-a", "shared-session", "", ""),
		codexDetailsSpawn(t, "call-child", "Incremental task name"),
	)
	codexDetailsRollout(t, d.codex, "child",
		codexDetailsMeta(t, "child", "shared-session", "main-a", "Child"),
		codexDetailsUsage(t, "child", 1),
	)
	if got := Titles([]string{"codex:child"})["codex:child"]; got != "" {
		t.Fatalf("unmatched spawn call named the child: %q", got)
	}
	appendText(t, parent, codexDetailsSubagentEvent(t, "call-child", "child")+"\n")
	if got := Titles([]string{"codex:child"})["codex:child"]; got != "Incremental task name" {
		t.Fatalf("parent append did not refresh the child title: %q", got)
	}
}

func TestCodexSubagentConflictingSpawnLinksDoNotNameChildren(t *testing.T) {
	d := setupCalls(t)
	codexDetailsRollout(t, d.codex, "main-a",
		codexDetailsMeta(t, "main-a", "shared-session", "", ""),
		codexDetailsSpawn(t, "call-name", "First task name"),
		codexDetailsSpawn(t, "call-name", "Conflicting task name"),
		codexDetailsSubagentEvent(t, "call-name", "child-name"),
		codexDetailsSpawn(t, "call-child", "Ambiguous child link"),
		codexDetailsSubagentEvent(t, "call-child", "child-one"),
		codexDetailsSubagentEvent(t, "call-child", "child-two"),
	)
	for _, id := range []string{"child-name", "child-one", "child-two"} {
		codexDetailsRollout(t, d.codex, id,
			codexDetailsMeta(t, id, "shared-session", "main-a", ""),
			codexDetailsUsage(t, id, 1),
		)
		if got := Titles([]string{"codex:" + id})["codex:"+id]; got != "" {
			t.Errorf("conflicting spawn metadata named %s: %q", id, got)
		}
	}
}

func TestCodexSubagentNicknameInvalidatesOldParseCache(t *testing.T) {
	d := setupCalls(t)
	path := codexDetailsRollout(t, d.codex, "child",
		codexDetailsMeta(t, "child", "shared-session", "main-a", "Fresh nickname"),
		codexDetailsUsage(t, "child", 1),
	)
	writeCache(&save{cacheFile: cacheFile{Version: cacheVersion - 1, Zone: zone(), Files: map[string]*state{path: {
		ID: "child", StateMore: &StateMore{CodexNickname: "stale nickname"}, Size: 1, Mod: 1,
	}}}, path: CachePath()})
	Reset()
	if got := codexDetailsSession(t, "child"); got.Nickname != "Fresh nickname" {
		t.Fatalf("older parse cache hid the native nickname: %q", got.Nickname)
	}
}

// codexLegacySubagentActivity is the started activity as a legacy rollout
// keeps it (Codex's default history before paginated, openai/codex#47900):
// an event_msg sub_agent_activity whose event_id is the spawn call's id.
func codexLegacySubagentActivity(t *testing.T, callID, childID, kind string) string {
	t.Helper()
	return codexDetailsLine(t, "event_msg", map[string]any{
		"type": "sub_agent_activity", "model": "gpt-5.5", "reasoning_effort": "medium", "event_id": callID,
		"occurred_at_ms": callT0.UnixMilli(), "agent_thread_id": childID, "agent_path": "/root/" + childID, "kind": kind,
	})
}

func TestCodexSubagentTaskTitleFromLegacyActivity(t *testing.T) {
	d := setupCalls(t)
	codexDetailsRollout(t, d.codex, "main-legacy",
		codexDetailsMeta(t, "main-legacy", "shared-session", "", ""),
		codexDetailsSpawn(t, "call-started", "Legacy started task"),
		codexLegacySubagentActivity(t, "call-started", "child-started", "started"),
		codexDetailsSpawn(t, "call-interacted", "Legacy interacted task"),
		codexLegacySubagentActivity(t, "call-interacted", "child-interacted", "interacted"),
	)
	for i, c := range []struct{ id, want string }{{"child-started", "Legacy started task"}, {"child-interacted", ""}} {
		codexDetailsRollout(t, d.codex, c.id,
			codexDetailsMeta(t, c.id, "shared-session", "main-legacy", ""),
			codexDetailsUsage(t, c.id, i+1),
		)
		if got := Titles([]string{"codex:" + c.id})["codex:"+c.id]; got != c.want {
			t.Errorf("legacy activity title for %s = %q, want %q", c.id, got, c.want)
		}
		if got := codexDetailsSession(t, c.id); got.Title != c.want {
			t.Errorf("legacy activity session title for %s = %q, want %q", c.id, got.Title, c.want)
		}
	}
}

func TestCodexSubagentTaskTitleComesFromItsOwnParent(t *testing.T) {
	d := setupCalls(t)
	spawnFirst := []string{
		codexDetailsSpawn(t, "call-first", "First child task"),
		codexDetailsSubagentEvent(t, "call-first", "child-first"),
	}
	codexDetailsRollout(t, d.codex, "main-a", append([]string{codexDetailsMeta(t, "main-a", "shared-session", "", "")}, spawnFirst...)...)
	// a forked child repeats its parent's history, the first child's spawn
	// included, and its own copy of a spawn names no child of main-a
	codexDetailsRollout(t, d.codex, "child-fork", append(append([]string{codexDetailsMeta(t, "child-fork", "shared-session", "main-a", "")}, spawnFirst...),
		codexDetailsSpawn(t, "call-copied", "Only in the fork's copy"),
		codexDetailsSubagentEvent(t, "call-copied", "child-copied"),
		codexDetailsUsage(t, "child-fork", 1))...)
	codexDetailsRollout(t, d.codex, "child-first",
		codexDetailsMeta(t, "child-first", "shared-session", "main-a", ""),
		codexDetailsUsage(t, "child-first", 2),
	)
	codexDetailsRollout(t, d.codex, "child-copied",
		codexDetailsMeta(t, "child-copied", "shared-session", "main-a", ""),
		codexDetailsUsage(t, "child-copied", 3),
	)
	if got := codexDetailsSession(t, "child-first"); got.Title != "First child task" {
		t.Errorf("a fork's copy of the parent's spawn erased the child's title: %q", got.Title)
	}
	if got := codexDetailsSession(t, "child-copied"); got.Title != "" {
		t.Errorf("a spawn its parent never made named the child: %q", got.Title)
	}
}
