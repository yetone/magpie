package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// #1913 (infinitr0us): Kimi Code's sessions are listed. The sessions in
// testdata/kimi-code-sessions are the #1607 reporter's whole real files
// (wire.jsonl and state.json as Kimi Code wrote them, text redacted by the
// reporter, every id, time and number kept); testdata/kimi-code holds the
// same sessions cut to the lines a call is read from. Their totals are the
// reporter's README's:
//
//   - wd_v14-june-subagent: 1.4, main and one subagent, 32 calls, input
//     200,820 + 2,641,152 cache read, output 12,283; no cwd in state.json
//   - wd_v14-cancel: 1.4, cwd as workDir, 6 calls, 15,726 + 132,096, 2,009
//   - wd_v15-early-no-agentid: 1.5, cwd and createdAt in ms, 10 calls,
//     20,888 + 317,440, 2,176
//   - wd_v15-magpie-failed: 1.5, one failed call through magpie, no tokens
//   - wd_v14-no-turn: opened and closed without a prompt
//   - wd_v10-migrated: from the Python kimi-cli, no time on any record, a
//     title the user gave (isCustomTitle)
const (
	kimiJune   = "session_a0a7c8d3-2a5a-480d-9d42-740257598abd"
	kimiCancel = "session_169766c9-86f5-4577-97a3-a51d402db5b9"
	kimiEarly  = "session_fc16731e-2bfe-4873-a6cb-4b9e73280360"
	kimiOld    = "ses_16cb5fd4-22a9-4c92-9468-19f44eacaea9"
)

// kimiSessionsIn copies the reporter's sessions into a Kimi Code home and
// gives the redacted titles and folders names to tell them by.
func kimiSessionsIn(t *testing.T, dir string) {
	t.Helper()
	copyTree(t, "testdata/kimi-code-sessions", dir)
	sessions := filepath.Join(dir, "sessions")
	kimiSetState(t, sessions, "wd_v14-june-subagent_000000000001", kimiJune, map[string]any{"title": "Port the parser"})
	kimiSetState(t, sessions, "wd_v14-cancel_000000000003", kimiCancel, map[string]any{"title": "Cancelled", "workDir": "/work/kimi-old"})
	kimiSetState(t, sessions, "wd_v15-early-no-agentid_000000000007", kimiEarly, map[string]any{"title": "Review the diff", "cwd": "/work/kimi"})
	kimiSetState(t, sessions, "wd_v10-migrated_000000000006", kimiOld, map[string]any{"title": "Old chat"})
}

// kimiSetState sets fields of a session's state.json, keeping the rest as
// Kimi wrote it.
func kimiSetState(t *testing.T, sessions, slug, id string, set map[string]any) {
	t.Helper()
	p := filepath.Join(sessions, slug, id, "state.json")
	st := map[string]any{}
	if b, err := os.ReadFile(p); err == nil {
		if err := json.Unmarshal(b, &st); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range set {
		st[k] = v
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestKimiCodeSessions(t *testing.T) {
	inZone(t, 0)
	claude, _ := setup(t)
	dir := filepath.Join(filepath.Dir(claude), "home", ".kimi-code")
	kimiSessionsIn(t, dir)
	ss := List(0)
	if n := count(ss, "kimi"); n != 6 {
		t.Fatalf("want the 6 Kimi Code sessions, got %d", n)
	}

	// main and its subagent in one session, from state.json's createdAt
	j := find(t, ss, "kimi", kimiJune)
	if j.Title != "Port the parser" || j.Cwd != "" {
		t.Fatalf("june: %+v", j)
	}
	if j.Tokens != (Tokens{200820, 12283, 2641152, 0, 0}) {
		t.Fatalf("june tokens %+v", j.Tokens)
	}
	if m := model(j, "kimi-code/kimi-for-coding"); m.Tokens != j.Tokens {
		t.Fatalf("june model %+v", j.Models)
	}
	if !j.Start.Equal(at("2026-06-15T05:49:26.333Z")) || !j.Last.Equal(at("2026-06-15T06:05:10.030Z")) {
		t.Fatalf("june times %s %s", j.Start, j.Last)
	}
	// no folder known: the command alone
	if j.Resume != "kimi --session "+kimiJune {
		t.Fatalf("june resume %q", j.Resume)
	}
	if !strings.HasSuffix(j.Path, filepath.Join(kimiJune, "agents", "main", "wire.jsonl")) {
		t.Fatalf("june path %s", j.Path)
	}

	// 1.4's workDir
	c := find(t, ss, "kimi", kimiCancel)
	if c.Cwd != "/work/kimi-old" || c.Tokens != (Tokens{15726, 2009, 132096, 0, 0}) {
		t.Fatalf("cancel: %+v", c)
	}

	// 1.5's cwd, createdAt in ms
	e := find(t, ss, "kimi", kimiEarly)
	if e.Cwd != "/work/kimi" || e.Title != "Review the diff" || e.Tokens != (Tokens{20888, 2176, 317440, 0, 0}) {
		t.Fatalf("early: %+v", e)
	}
	if !e.Start.Equal(time.UnixMilli(1786134177385)) || !e.Last.Equal(at("2026-08-07T20:25:57.753Z")) {
		t.Fatalf("early times %s %s", e.Start, e.Last)
	}
	want := "cd '/work/kimi' && kimi --session " + kimiEarly
	if runtime.GOOS == "windows" {
		want = "Set-Location -LiteralPath '/work/kimi'; kimi --session " + kimiEarly
	}
	if e.Resume != want {
		t.Fatalf("early resume %q", e.Resume)
	}

	// no time in its records: dated by state.json
	o := find(t, ss, "kimi", kimiOld)
	if o.Title != "Old chat" || !o.Start.Equal(at("2026-02-28T04:18:28.186Z")) || !o.Tokens.zero() {
		t.Fatalf("migrated: %+v", o)
	}
}

// #2103 (infinitr0us): a session whose state.json names no folder (1.4's
// early ones, those migrated from the Python kimi-cli) takes it from
// session_index.jsonl, as Kimi documents it; a folder state.json names wins.
func TestKimiCodeSessionFolderFromIndex(t *testing.T) {
	inZone(t, 0)
	claude, _ := setup(t)
	dir := filepath.Join(filepath.Dir(claude), "home", ".kimi-code")
	kimiSessionsIn(t, dir)
	sessions := filepath.Join(dir, "sessions")
	line := func(slug, id, work string) string {
		b, _ := json.Marshal(map[string]string{"sessionId": id, "sessionDir": filepath.Join(sessions, slug, id), "workDir": work})
		return string(b) + "\n"
	}
	index := line("wd_v14-june-subagent_000000000001", kimiJune, "/Users/me/Desktop/june") +
		line("wd_v10-migrated_000000000006", kimiOld, "/Users/me/Desktop/test") +
		line("wd_v15-early-no-agentid_000000000007", kimiEarly, "/elsewhere")
	if err := os.WriteFile(filepath.Join(dir, "session_index.jsonl"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	ss := List(0)
	j := find(t, ss, "kimi", kimiJune)
	if j.Cwd != "/Users/me/Desktop/june" {
		t.Fatalf("june folder %q", j.Cwd)
	}
	want := "cd '/Users/me/Desktop/june' && kimi --session " + kimiJune
	if runtime.GOOS == "windows" {
		want = "Set-Location -LiteralPath '/Users/me/Desktop/june'; kimi --session " + kimiJune
	}
	if j.Resume != want {
		t.Fatalf("june resume %q", j.Resume)
	}
	if o := find(t, ss, "kimi", kimiOld); o.Cwd != "/Users/me/Desktop/test" {
		t.Fatalf("migrated folder %q", o.Cwd)
	}
	if e := find(t, ss, "kimi", kimiEarly); e.Cwd != "/work/kimi" {
		t.Fatalf("state.json's cwd lost to the index: %q", e.Cwd)
	}
	// state.json's workDir, no line in the index
	if c := find(t, ss, "kimi", kimiCancel); c.Cwd != "/work/kimi-old" {
		t.Fatalf("cancel folder %q", c.Cwd)
	}
	// no folder anywhere: still none
	if n := find(t, ss, "kimi", "session_7d423a2a-9c30-4478-8fb0-ceb78f9dbb6a"); n.Cwd != "" {
		t.Fatalf("no-turn folder %q", n.Cwd)
	}
}

// The prompts typed (not a subagent's task, not a skill's), the replies that
// end a turn and the name the user gave, from the main file.
func TestKimiCodeSessionCounts(t *testing.T) {
	inZone(t, 0)
	claude, _ := setup(t)
	dir := filepath.Join(filepath.Dir(claude), "home", ".kimi-code")
	kimiSessionsIn(t, dir)
	byKey := map[string][]file{}
	for _, f := range kimiFiles() {
		byKey[f.key] = append(byKey[f.key], f)
	}
	sum := func(id string) (prompts, replies, tools int, custom, named string) {
		for _, f := range byKey["kimi:"+id] {
			s := parseKimi(f, nil)
			for _, d := range s.Days.all() {
				prompts += d.Prompts
				replies += d.Replies
				for _, n := range d.Tools.all() {
					tools += n
				}
			}
			if f.main {
				custom, named = s.more().Custom, s.more().Named
			}
		}
		return
	}
	if p, r, tl, cu, na := sum(kimiJune); p != 3 || r != 3 || tl != 56 || cu != "" || na != "Port the parser" {
		t.Fatalf("june: prompts %d replies %d tools %d custom %q named %q", p, r, tl, cu, na)
	}
	// a skill's turn isn't a prompt typed
	if p, r, tl, _, _ := sum(kimiEarly); p != 0 || r != 1 || tl != 10 {
		t.Fatalf("early: prompts %d replies %d tools %d", p, r, tl)
	}
	if _, _, _, cu, na := sum(kimiOld); cu != "Old chat" || na != "" {
		t.Fatalf("migrated: custom %q named %q", cu, na)
	}
	// 1.5's titleKind
	for _, c := range []struct {
		st           string
		custom, name string
	}{
		{`{"title":"a","titleKind":"custom"}`, "a", ""},
		{`{"title":"a","titleKind":"generated"}`, "", "a"},
		{`{"title":"a","titleKind":"custom","isCustomTitle":false}`, "", "a"},
		{`{"customTitle":"b"}`, "b", ""},
	} {
		var st kimiState
		json.Unmarshal([]byte(c.st), &st)
		if cu, na := st.names(); cu != c.custom || na != c.name {
			t.Errorf("%s: custom %q named %q", c.st, cu, na)
		}
	}
}

// A fork copies its source's records, then marks where it begins
// (Kimi Code's sessionLifecycleService.fork): what it copied is counted in
// the session it came from, and the fork starts when it was made.
func TestKimiCodeSessionFork(t *testing.T) {
	inZone(t, 0)
	claude, _ := setup(t)
	dir := filepath.Join(filepath.Dir(claude), "home", ".kimi-code")
	kimiSessionsIn(t, dir)
	slug := "wd_v14-cancel_000000000003"
	src := filepath.Join(dir, "sessions", slug, kimiCancel, "agents", "main", "wire.jsonl")
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	// one of the source's own calls and its prompt, made again after the fork
	var call, prompt string
	for _, l := range strings.Split(string(b), "\n") {
		if call == "" && strings.Contains(l, `"type":"usage.record"`) {
			call = l
		}
		if prompt == "" && strings.Contains(l, `"type":"turn.prompt"`) {
			prompt = l
		}
	}
	const made = int64(1791700000000)
	retime := func(l string, when int64) string {
		var r map[string]any
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatal(err)
		}
		r["time"] = when
		out, _ := json.Marshal(r)
		return string(out)
	}
	var u struct {
		Usage kimiUsage `json:"usage"`
	}
	json.Unmarshal([]byte(call), &u)
	own := Tokens{Input: u.Usage.InputOther, Output: u.Usage.Output, CacheRead: u.Usage.InputCacheRead, CacheWrite: u.Usage.InputCacheCreation}

	fork := func(id string, marker bool, created int64) {
		w := filepath.Join(dir, "sessions", slug, id, "agents", "main", "wire.jsonl")
		os.MkdirAll(filepath.Dir(w), 0o755)
		body := strings.TrimRight(string(b), "\n") + "\n"
		if marker {
			body += `{"type":"forked","agentId":"main","time":` + jsonInt(made) + "}\n"
		}
		body += retime(prompt, made+1000) + "\n" + retime(call, made+5000) + "\n"
		if err := os.WriteFile(w, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		kimiSetState(t, filepath.Join(dir, "sessions"), slug, id, map[string]any{
			"id": id, "version": 2, "cwd": "/work/kimi-old", "createdAt": created, "updatedAt": made + 5000,
			"title": "Cancelled, again", "titleKind": "generated", "isCustomTitle": false, "forkedFrom": kimiCancel,
		})
	}
	fork("session_f0000000-0000-4000-8000-000000000001", true, made)
	// written without its marker: cut at when it was made
	fork("session_f0000000-0000-4000-8000-000000000002", false, made)
	// the marker cuts where the time can't: a clock set back when it was made
	fork("session_f0000000-0000-4000-8000-000000000003", true, at("2026-07-01T00:00:00Z").UnixMilli())

	ss := List(0)
	if s := find(t, ss, "kimi", kimiCancel); s.Tokens != (Tokens{15726, 2009, 132096, 0, 0}) {
		t.Fatalf("the source changed: %+v", s.Tokens)
	}
	for _, id := range []string{"session_f0000000-0000-4000-8000-000000000001", "session_f0000000-0000-4000-8000-000000000002", "session_f0000000-0000-4000-8000-000000000003"} {
		f := find(t, ss, "kimi", id)
		if f.Tokens != own || f.Title != "Cancelled, again" {
			t.Fatalf("fork %s: %+v, want its own call %+v", id, f, own)
		}
		if strings.HasSuffix(id, "3") {
			continue
		}
		if !f.Start.Equal(time.UnixMilli(made)) || !f.Last.Equal(time.UnixMilli(made+5000)) {
			t.Fatalf("fork %s times %s %s", id, f.Start, f.Last)
		}
	}
}

func jsonInt(n int64) string { b, _ := json.Marshal(n); return string(b) }

// Kimi Code in a WSL distro: its sessions listed with the distro and its
// calls counted (#1607's reader reaches the distro too).
func TestWSLKimiCode(t *testing.T) {
	home, _ := wslDistro(t)
	kimiSessionsIn(t, filepath.Join(home, ".kimi-code"))
	Reset()
	var e *Session
	for _, s := range List(0) {
		if s.Agent == "kimi" && s.ID == kimiEarly {
			e = &s
		}
	}
	if e == nil || e.WSL != "Ubuntu" || e.Tokens != (Tokens{20888, 2176, 317440, 0, 0}) {
		t.Fatalf("the distro's Kimi session: %+v", e)
	}
	want := `wsl.exe -d 'Ubuntu' --cd '/work/kimi' -e sh -lc 'exec ${SHELL:-sh} -lic ''kimi --session ` + kimiEarly + `'''`
	if e.Resume != want {
		t.Fatalf("resume\n got %s\nwant %s", e.Resume, want)
	}
	n := 0
	for _, c := range Calls(time.Time{}) {
		if c.Agent == "kimi" {
			n++
		}
	}
	if n != 32+6+10 {
		t.Fatalf("the distro's Kimi calls: %d, want 48", n)
	}
	if !strings.Contains(strings.Join(Dirs(), "\n"), filepath.Join(home, ".kimi-code")) {
		t.Fatalf("Kimi's folder in the distro isn't listed: %v", Dirs())
	}
}
