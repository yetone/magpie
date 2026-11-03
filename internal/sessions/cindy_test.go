package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Cindy starts Pi with --session-dir <userData>/pi-agent-home/sessions
// (Cindy's resolvePiAgentHome, read in its app.asar), so Pi's SDK files,
// <time>_<id>.jsonl, sit flat there and never in ~/.pi/agent. No Pi
// session was run in Cindy here, so the files are Pi's own
// (testdata/pi-calls, Pi 1.1.0's, and testdata/pi), put where Cindy's Pi
// writes them: the global build's folder and the Chinese build's.
func cindySetup(t *testing.T) (global, cn string) {
	t.Helper()
	manageSetup(t)
	dirs := CindyDirs()
	if len(dirs) != 2 {
		t.Fatalf("Cindy's folders: %v", dirs)
	}
	cn = filepath.Join(dirs[0], "pi-agent-home", "sessions")
	global = filepath.Join(dirs[1], "pi-agent-home", "sessions")
	put := func(from, dir string) {
		t.Helper()
		data, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(dir, 0o755)
		p := filepath.Join(dir, filepath.Base(from))
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-24 * time.Hour)
		os.Chtimes(p, old, old)
	}
	calls, _ := filepath.Glob(filepath.Join("testdata", "pi-calls", "sessions", "*", "*.jsonl"))
	if len(calls) != 3 {
		t.Fatalf("pi-calls: %v", calls)
	}
	for _, f := range calls {
		put(f, global)
	}
	put(filepath.Join("testdata", "pi", "sessions", "--work-pi--", "2026-09-24T08-00-00-000Z_"+piFirst+".jsonl"), cn)
	// Pi's run folders beside them are not sessions
	os.MkdirAll(filepath.Join(filepath.Dir(global), "runtime", "pi-subagent-runs"), 0o755)
	Reset()
	return global, cn
}

func TestCindyPiSessions(t *testing.T) {
	global, _ := cindySetup(t)
	ss := List(0)
	n := 0
	for _, s := range ss {
		if s.Agent == "pi" {
			t.Fatalf("a Cindy session listed as Pi's: %+v", s)
		}
		if s.Agent == "cindy" {
			n++
		}
	}
	if n != 4 {
		t.Fatalf("%d Cindy sessions, want 4", n)
	}
	p := find(t, ss, "cindy", raftParent)
	if p.Cwd != "/work/relay" || p.Title != "ping" {
		t.Fatalf("cindy: %+v", p)
	}
	if f := find(t, ss, "cindy", raftFork); f.Title != "ping" || f.Tokens != (Tokens{Input: 203, Output: 30, CacheRead: 1000}) {
		t.Fatalf("the fork: %+v", f)
	}
	if c := find(t, ss, "cindy", piFirst); c.Title != "Parser refactor" {
		t.Fatalf("the CN build's: %+v", c)
	}

	if !HasTranscript("cindy") {
		t.Fatal("no transcript for Cindy")
	}
	tr, err := TranscriptOf(p)
	if err != nil {
		t.Fatal(err)
	}
	var said []string
	for _, x := range tr.Parts {
		if x.Role == "user" && x.Kind == "text" {
			said = append(said, x.Text)
		}
	}
	if strings.Join(said, "|") != "ping|again" {
		t.Fatalf("prompts %q in %+v", said, tr.Parts)
	}

	// its calls are Cindy's on the Usage page, those through magpie the
	// gateway's
	by, relay := map[string]int{}, 0
	for _, c := range Calls(time.Time{}) {
		by[c.Agent]++
		if c.Agent == "cindy" && (c.Upstream == "magpie" || c.Upstream == "dead") {
			t.Errorf("a call through magpie or one that failed: %+v", c)
		}
		if c.Agent == "cindy" && (c.Session == raftParent || c.Session == raftFork) && c.Upstream == "relay" {
			relay++
		}
	}
	if relay != 3 || by["pi"] != 0 {
		t.Fatalf("Cindy's relay calls %d, want 3 (as TestPiCalls); calls by agent %v", relay, by)
	}

	found := false
	for _, d := range Dirs() {
		found = found || d == global
	}
	if !found {
		t.Fatalf("Cindy's session folder not among %v", Dirs())
	}
	if !Deletable("cindy") {
		t.Fatal("Cindy's sessions not deletable")
	}
}

// The gateway counts Cindy's Pi requests as Cindy's: a session it recorded
// under the same id is the one read from Cindy's file, listed once.
func TestCindyGatewaySessionListedOnce(t *testing.T) {
	cindySetup(t)
	merged := MergeExternal(List(0), []ExternalSession{
		{Agent: "cindy", ID: raftViaUs, Title: "fail"},
		{Agent: "cindy", ID: "01a1343b-0000-7000-8000-000000000000", Title: "only the gateway's"},
	})
	ids := map[string]int{}
	for _, s := range merged {
		if s.Agent == "cindy" {
			ids[s.ID]++
		}
	}
	if ids[raftViaUs] != 1 || ids["01a1343b-0000-7000-8000-000000000000"] != 1 || len(ids) != 5 {
		t.Fatalf("Cindy's sessions: %v", ids)
	}
	if !NativeIDs(map[string]bool{"cindy": true})["cindy"][raftViaUs] {
		t.Fatal("Cindy's native ids leave its files out")
	}
}

// TestCindyGatewayCallsCountedOnce: Cindy imports magpie's link as the
// provider "magpie-<8 hex>" (its import parser adds eight hex digits of a
// new UUID to the link's id), and its Pi writes that id on each reply. A
// reply through it is the gateway's, already counted there with the
// account that answered; read again here it was listed and priced twice
// (jealousy03888 on Discord: ZCode's GLM-5.3 in Cindy, one gateway row and
// one "local session" row of the same 135/37/84,736 tokens). A provider
// of the user's own whose name only begins with magpie stays Cindy's.
func TestCindyGatewayCallsCountedOnce(t *testing.T) {
	global, _ := cindySetup(t)
	const id = "019a4c2e-5b1d-7f3a-9c6e-2d8f0a1b3c4d"
	lines := []string{
		`{"type":"session","version":3,"id":"` + id + `","timestamp":"2026-11-03T09:30:52.410Z","cwd":"/work/app"}`,
		`{"type":"model_change","id":"a1b2c3d4","parentId":null,"timestamp":"2026-11-03T09:30:52.611Z","provider":"magpie-beedee1a","modelId":"zcode/GLM-5.3"}`,
		`{"type":"thinking_level_change","id":"b2c3d4e5","parentId":"a1b2c3d4","timestamp":"2026-11-03T09:30:52.611Z","thinkingLevel":"high"}`,
		`{"type":"message","id":"c3d4e5f6","parentId":"b2c3d4e5","timestamp":"2026-11-03T09:30:57.802Z","message":{"role":"user","content":[{"type":"text","text":"hi"}],"timestamp":1793611857800}}`,
		`{"type":"message","id":"d4e5f6a7","parentId":"c3d4e5f6","timestamp":"2026-11-03T09:31:03.122Z","message":{"role":"assistant","content":[{"type":"text","text":"Hello!"}],"api":"openai-completions","provider":"magpie-beedee1a","model":"zcode/GLM-5.3","usage":{"input":135,"output":37,"cacheRead":84736,"cacheWrite":0,"reasoning":0,"totalTokens":84908,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"stop","timestamp":1793611857830,"responseId":"chatcmpl-5d2e","durationMs":5292,"thinkingLevel":"high"}}`,
		`{"type":"message","id":"e5f6a7b8","parentId":"d4e5f6a7","timestamp":"2026-11-03T09:32:10.001Z","message":{"role":"user","content":[{"type":"text","text":"again"}],"timestamp":1793611930000}}`,
		`{"type":"message","id":"f6a7b8c9","parentId":"e5f6a7b8","timestamp":"2026-11-03T09:32:12.500Z","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"api":"openai-completions","provider":"magpie-relay","model":"glm-5","usage":{"input":40,"output":5,"cacheRead":0,"cacheWrite":0,"reasoning":0,"totalTokens":45,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"stop","timestamp":1793611930010,"responseId":"chatcmpl-6e3f","durationMs":2400,"thinkingLevel":"high"}}`,
	}
	p := filepath.Join(global, "2026-11-03T09-30-52-410Z_"+id+".jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(p, old, old)
	Reset()
	var own int
	for _, c := range Calls(time.Time{}) {
		if c.Session != id {
			continue
		}
		if c.Upstream == "magpie-beedee1a" {
			t.Errorf("a call through magpie's link in Cindy counted as Cindy's own: %+v", c)
		}
		if c.Upstream == "magpie-relay" && c.Agent == "cindy" && c.Tokens == (Tokens{Input: 40, Output: 5}) {
			own++
		}
	}
	if own != 1 {
		t.Fatalf("the call on the user's own magpie-relay counted %d times, want 1", own)
	}
	// Pi's own files name providers the user chose: the shape is Cindy's alone
	if piGateway("pi", "magpie-beedee1a") || !piGateway("pi", "magpie") || !piGateway("cindy", "magpie-beedee1a") {
		t.Fatal("piGateway's scope")
	}
	for _, s := range []string{"magpie-2", "magpie-BEEDEE1A", "magpie-beedee1", "magpie-beedee1a0", "magpie-beedee1g"} {
		if cindyGatewayProvider(s) {
			t.Errorf("%q taken for magpie's link", s)
		}
	}
}
