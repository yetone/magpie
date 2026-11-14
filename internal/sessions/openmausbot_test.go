package sessions

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// testdata/openmausbot is OpenMausBot 0.1.155's data folder as it wrote it
// (bots.json, messages.db and usage/2026-11.jsonl, the database
// checkpointed): a bot "Scout" on an OpenAI-compatible engine asked "Say
// hi, then list three fruits" and then "你好，再说一次", and in a second
// conversation "TOOL: who is on the team?", where it said a line, ran
// agents_list_bots and replied; and two conversations holding only a
// bot's greeting (Scout's, and MausBot's, the chief of staff's).
const (
	ombFruits = "301edbda-e68f-4c9c-b9da-d2a9b6ab179c"
	ombTeam   = "ed2913c7-5021-426b-8434-2af0df746c0d"
	ombGreet  = "3ec55eac-63f4-44d9-bbfd-a86ee201ec5f"
	ombChief  = "8e80165a-834e-402b-b640-7928e8a481ef"
)

// ombFixture copies the sample into dir, OpenMausBot's data folder.
func ombFixture(t *testing.T, dir string) {
	t.Helper()
	copyTree(t, "testdata/openmausbot", dir)
}

func ombSession(t *testing.T, list []Session, id string) Session {
	t.Helper()
	for _, s := range list {
		if s.Agent == "openmausbot" && s.ID == id {
			return s
		}
	}
	t.Fatalf("OpenMausBot conversation %q not listed: %+v", id, list)
	return Session{}
}

func ombManaged(t *testing.T, id string) Managed {
	t.Helper()
	for _, m := range ListAgent("openmausbot") {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("OpenMausBot conversation %q not on the page", id)
	return Managed{}
}

func TestOpenMausBotSessions(t *testing.T) {
	setup(t)
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".openmausbot")
	if OpenMausBotDir() != dir {
		t.Fatalf("OpenMausBotDir() = %q, want %q", OpenMausBotDir(), dir)
	}
	ombFixture(t, dir)
	db := filepath.Join(dir, "messages.db")
	before, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}

	list := List(0)
	a := ombSession(t, list, ombFruits)
	if !a.ReadOnly || a.Resume != "" || a.Title != "Say hi, then list three fruits" || a.Cwd != "" {
		t.Errorf("fruits: %+v", a)
	}
	// input is OpenMausBot's whole prompt; no cache was read or written
	if got := model(a, "fake-model-1").Tokens; got != (Tokens{Input: 1202 + 1203, Output: 42 + 43}) {
		t.Errorf("fruits tokens = %+v", got)
	}
	if !a.Start.Equal(time.UnixMilli(1794638499985)) || !a.Last.Equal(time.UnixMilli(1794638502422)) {
		t.Errorf("fruits times %v – %v", a.Start, a.Last)
	}
	b := ombSession(t, list, ombTeam)
	if b.Title != "TOOL: who is on the team?" {
		t.Errorf("team: %+v", b)
	}
	if got := model(b, "fake-model-1").Tokens; got != (Tokens{Input: 2403, Output: 83}) {
		t.Errorf("team tokens = %+v", got)
	}
	for _, s := range list {
		if s.Agent == "openmausbot" && (s.ID == ombGreet || s.ID == ombChief) {
			t.Errorf("a conversation with only a bot's greeting was listed: %+v", s)
		}
	}

	// the Sessions page: listed, never deletable; a reply a turn, though
	// the bot spoke before its tool and after it
	managed := ListAgent("openmausbot")
	if len(managed) != 2 {
		t.Fatalf("ListAgent(openmausbot) = %+v", managed)
	}
	for _, m := range managed {
		if m.Deletable {
			t.Errorf("an OpenMausBot conversation is deletable: %+v", m)
		}
		want := map[string]int{ombFruits: 4, ombTeam: 2}[m.ID]
		if m.Messages != want || m.Size == 0 {
			t.Errorf("%s on the page: %d messages (want %d), %d bytes", m.ID, m.Messages, want, m.Size)
		}
	}
	if _, err := Delete("openmausbot", ombFruits); err == nil {
		t.Error("Delete took a conversation out of OpenMausBot's database")
	}

	st := StatsAt(0, time.UnixMilli(1794638536426).Add(24*time.Hour))
	var sum Summary
	for _, s := range st.Sessions {
		if s.Key == "openmausbot:"+ombTeam {
			sum = s
		}
	}
	if sum.Prompts != 1 || sum.Replies != 1 || sum.ToolCalls != 1 || sum.tools.of("agents_list_bots") != 1 {
		t.Errorf("team stats: prompts %d replies %d tools %d %v", sum.Prompts, sum.Replies, sum.ToolCalls, sum.tools)
	}

	found := false
	for _, d := range Dirs() {
		found = found || d == dir
	}
	if !found {
		t.Errorf("Dirs() %v leaves out OpenMausBot's", Dirs())
	}
	after, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("reading OpenMausBot's conversations changed its database")
	}
}

// A bot on Claude Code (or Codex, Pi, another agent CLI) runs that CLI,
// which keeps the turn in its own session: its tokens are counted there,
// not again here. A bot on a model API's are counted here, cache reads
// and writes apart from the fresh input OpenMausBot's input includes.
// The rows are the sample's own, with their engine and cache changed.
func TestOpenMausBotCountsCLITurnsOnce(t *testing.T) {
	setup(t)
	dir := filepath.Join(t.TempDir(), "omb")
	t.Setenv("OMB_DATA_DIR", dir)
	ombFixture(t, dir)
	path := filepath.Join(dir, "usage", "2026-11.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(raw), "\n")
	cli := strings.Replace(lines[0], `"driverKind":"openai-compat","model":"fake-model-1"`, `"driverKind":"claudeAgent","model":"claude-opus-5-5"`, 1)
	cached := strings.Replace(lines[1], `"input":1203,"output":43,`, `"input":1203,"output":43,"cachedInput":1000,"cacheWrite":100,`, 1)
	if cli == lines[0] || cached == lines[1] {
		t.Fatal("the sample's rows changed shape")
	}
	if err := os.WriteFile(path, []byte(cli+cached+strings.Join(lines[2:], "")), 0o644); err != nil {
		t.Fatal(err)
	}
	bots := filepath.Join(dir, "bots.json")
	b, err := os.ReadFile(bots)
	if err != nil {
		t.Fatal(err)
	}
	pinned := strings.Replace(string(b), `"title":"Say hi, then list three fruits",`, `"title":"Say hi, then list three fruits","cwd":"/work/fruits",`, 1)
	if pinned == string(b) {
		t.Fatal("the sample's bots.json changed shape")
	}
	if err := os.WriteFile(bots, []byte(pinned), 0o644); err != nil {
		t.Fatal(err)
	}

	a := ombSession(t, List(0), ombFruits)
	if got := model(a, "claude-opus-5-5").Tokens; !got.zero() {
		t.Errorf("a Claude Code turn's tokens counted again: %+v", got)
	}
	if got := model(a, "fake-model-1").Tokens; got != (Tokens{Input: 103, Output: 43, CacheRead: 1000, CacheWrite: 100}) {
		t.Errorf("an API turn's tokens = %+v", got)
	}
	if a.Cwd != "/work/fruits" {
		t.Errorf("the conversation's folder = %q", a.Cwd)
	}
}

// OpenMausBot writes its WAL all the while it runs: a conversation is
// read again when its own rows, or its title in bots.json, change, though
// the database file's size and time don't.
func TestOpenMausBotReadAgainWhenItChanges(t *testing.T) {
	setup(t)
	dir := filepath.Join(t.TempDir(), "omb")
	t.Setenv("OMB_DATA_DIR", dir)
	ombFixture(t, dir)
	if s := ombManaged(t, ombTeam); s.Messages != 2 {
		t.Fatalf("team: %+v", s)
	}
	// a new title alone changes neither the database nor its rows
	bots := filepath.Join(dir, "bots.json")
	b, err := os.ReadFile(bots)
	if err != nil {
		t.Fatal(err)
	}
	renamed := bytes.Replace(b, []byte(`"title":"TOOL: who is on the team?"`), []byte(`"title":"The team"`), 1)
	if bytes.Equal(renamed, b) {
		t.Fatal("the sample's bots.json changed shape")
	}
	if err := os.WriteFile(bots, renamed, 0o644); err != nil {
		t.Fatal(err)
	}
	if s := ombManaged(t, ombTeam); s.Messages != 2 || s.Title != "The team" {
		t.Fatalf("a new title not read: %+v", s)
	}

	path := filepath.Join(dir, "messages.db")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO messages (thread_id, id, at, role, kind, text, json)
	 SELECT thread_id, 'again', at + 1000, role, kind, text, json FROM messages WHERE thread_id = ? AND role = 'user'`, ombTeam); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	s := ombManaged(t, ombTeam)
	if s.Messages != 3 || s.Title != "The team" {
		t.Errorf("team not read again: %+v", s)
	}
}

// ombParts is a transcript's parts as role/kind/name: text, for a
// comparison that reads as the conversation does.
func ombParts(t *testing.T, s Session) []string {
	t.Helper()
	tr, err := TranscriptOf(s)
	if err != nil {
		t.Fatalf("TranscriptOf(%s): %v", s.ID, err)
	}
	var out []string
	for _, p := range tr.Parts {
		out = append(out, p.Role+"/"+p.Kind+"/"+p.Name+": "+p.Text)
	}
	return out
}

// An OpenMausBot conversation is shown as the app shows it (@qingqingdilaiP
// on X: the row had no Show conversation): the person's messages, the
// bot's lines and the tool its turn ran with what it answered, in order,
// and not the turn's digest. It is found by its id as the page asks, its
// Markdown and a search read the same rows, and the database is only read.
func TestOpenMausBotTranscript(t *testing.T) {
	setup(t)
	dir := filepath.Join(t.TempDir(), "omb")
	t.Setenv("OMB_DATA_DIR", dir)
	ombFixture(t, dir)
	db := filepath.Join(dir, "messages.db")
	before, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{ombFruits, ombTeam} {
		if s := ombSession(t, List(0), id); !s.Transcript {
			t.Errorf("%s: no conversation to show: %+v", id, s)
		}
		if m := ombManaged(t, id); !m.Transcript {
			t.Errorf("%s on the Sessions page: no conversation to show", id)
		}
	}
	team, ok := Find("openmausbot", ombTeam)
	if !ok {
		t.Fatal("Find(openmausbot, team) found nothing")
	}
	got := ombParts(t, team)
	want := []string{
		"user/text/: TOOL: who is on the team?",
		"assistant/text/: Checking the team.",
		"assistant/tool_use/agents_list_bots: agents_list_bots",
	}
	if len(got) != 5 || !slices.Equal(got[:3], want) || !strings.HasPrefix(got[3], "tool/tool_result/agents_list_bots: {") ||
		!strings.Contains(got[3], "Bots you can reach") || got[4] != "assistant/text/: Hello from the fake model, reply 2." {
		t.Errorf("team transcript:\n%s", strings.Join(got, "\n"))
	}
	fruits, ok := Find("openmausbot", ombFruits)
	if !ok {
		t.Fatal("Find(openmausbot, fruits) found nothing")
	}
	if got, want := ombParts(t, fruits), []string{
		"user/text/: Say hi, then list three fruits",
		"assistant/text/: Hello from the fake model, reply 2.",
		"user/text/: 你好，再说一次",
		"assistant/text/: Hello from the fake model, reply 3.",
	}; !slices.Equal(got, want) {
		t.Errorf("fruits transcript:\n%s", strings.Join(got, "\n"))
	}

	var md bytes.Buffer
	if err := WriteMarkdown(&md, fruits, "OpenMausBot"); err != nil || !strings.Contains(md.String(), "你好，再说一次") || !strings.Contains(md.String(), "reply 3.") {
		t.Errorf("Markdown (%v):\n%s", err, md.String())
	}
	for q, want := range map[string]bool{"再说一次": true, "reply 3": true, "no such words": false} {
		if _, found, err := SearchSession(context.Background(), fruits, NeedleOf(q)); err != nil || found != want {
			t.Errorf("search %q: found %v (%v), want %v", q, found, err, want)
		}
	}

	after, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("showing an OpenMausBot conversation changed its database")
	}
}

// A tool OpenMausBot kept the arguments of is shown with them; a line the
// app adds itself (a notice, an activity of no turn's item) is not said.
// The rows are written as OpenMausBot writes them (server/index.js's
// item.started and item.completed, cloud-credit-moves.js's notice).
func TestOpenMausBotTranscriptToolsAndNotices(t *testing.T) {
	setup(t)
	dir := filepath.Join(t.TempDir(), "omb")
	t.Setenv("OMB_DATA_DIR", dir)
	ombFixture(t, dir)
	db, err := sql.Open("sqlite", filepath.Join(dir, "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	add := func(id string, at int64, js string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO messages (thread_id, id, at, role, kind, text, json) VALUES (?, ?, ?, 'bot', 'activity', NULL, ?)`, ombTeam, id, at, js); err != nil {
			t.Fatal(err)
		}
	}
	add("notice", 1794638536500, `{"id":"notice","at":1794638536500,"role":"bot","kind":"activity","tool":{"name":"notice: Scout moved to another engine","ok":true}}`)
	add("read", 1794638536600, `{"id":"read","at":1794638536600,"role":"bot","kind":"activity","tool":{"name":"read_file","spoken":"reading a file","summary":"notes.md","input":{"path":"notes.md"},"itemId":"call_2","ok":false,"output":"no such file"},"turnId":"68914cf1-5ffb-49d6-ae64-88cf1b5cd435"}`)
	team, ok := Find("openmausbot", ombTeam)
	if !ok {
		t.Fatal("Find(openmausbot, team) found nothing")
	}
	got := ombParts(t, team)
	if len(got) != 7 || got[5] != "assistant/tool_use/read_file: {\n  \"path\": \"notes.md\"\n}" || got[6] != "tool/tool_result/read_file: no such file" {
		t.Errorf("team transcript:\n%s", strings.Join(got, "\n"))
	}
	for _, p := range got {
		if strings.Contains(p, "notice") {
			t.Errorf("OpenMausBot's own notice was said: %q", p)
		}
	}
}
