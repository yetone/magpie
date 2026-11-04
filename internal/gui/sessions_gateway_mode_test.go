package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// paultian on Discord: in gateway mode the Sessions page and Usage ›
// Sessions list what came through this gateway from other computers,
// never this computer's own agents' session files, which a machine put in
// gateway mode with Claude Code and Codex installed still listed. Every
// route that reaches a session is asked: the list, one, the transcript,
// Markdown and its export, the stats and the overview, search, manage
// and its trash, delete, restore, purge, carry and moving Claude Code's folder.
// The gateway's session shows on each. Gateway mode off, as before.
func TestGatewayModeListsNoOwnSessions(t *testing.T) {
	h := sandboxHome(t)
	claude := filepath.Join(h, ".claude")
	codex := filepath.Join(h, ".codex")
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	t.Setenv("CODEX_HOME", codex)
	if err := os.MkdirAll(filepath.Join(h, "Downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(claude, "projects", "-work")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	claudeSession := func(id, text string) string {
		ts := at.UTC().Format("2006-01-02T15:04:05.000Z")
		p := filepath.Join(proj, id+".jsonl")
		b := `{"parentUuid":null,"isSidechain":false,"type":"user","message":{"role":"user","content":"` + text + `"},"uuid":"u` + id + `","timestamp":"` + ts + `","cwd":"/work","sessionId":"` + id + `"}` + "\n" +
			`{"parentUuid":"u` + id + `","isSidechain":false,"message":{"model":"claude-sonnet-5","id":"msg_` + id + `","type":"message","role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":10,"output_tokens":2}},"requestId":"req_` + id + `","type":"assistant","uuid":"a` + id + `","timestamp":"` + ts + `","cwd":"/work","sessionId":"` + id + `"}` + "\n"
		if err := os.WriteFile(p, []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(p, at, at)
		return p
	}
	const local, trashed, codexID = "11111111-1111-2222-3333-444444444444", "22222222-1111-2222-3333-444444444444", "019a0000-0000-7000-8000-00000000c0de"
	localFile := claudeSession(local, "my local secret plan")
	claudeSession(trashed, "an old local plan")
	writeCodexAPITestThread(t, codex, codexID, codexID, "", []codexTestCall{{response: "resp_1", model: "gpt-5.5", effort: "high", at: at, input: 100, output: 10}})
	sessions.Reset()
	t.Cleanup(sessions.Reset)

	mux := http.NewServeMux()
	sessionRoutes(mux, folderOnly{})
	sessionManageRoutes(mux, folderOnly{})
	gatewayModeRoutes(mux)
	call := func(method, path, body string) (int, string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w.Code, w.Body.String()
	}
	ok := func(method, path, body string) string {
		t.Helper()
		code, b := call(method, path, body)
		if code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, code, b)
		}
		return b
	}

	// a session another computer's Pi sent through this gateway, its
	// conversation recorded
	ok("POST", "/api/sessions/recording", `{"on":true}`)
	const remote = "ses_remote_pi_1"
	usage.Append(usage.Record{Time: at, Agent: "pi", Session: remote, Provider: "fake", Model: "m", Input: 50, Output: 5, Status: 200})
	if err := sessions.SaveGatewayTurn(sessions.GatewayTurn{Agent: "pi", Session: remote, Time: at,
		Input:  []sessions.Part{{Role: "user", Kind: "text", Text: "rename foo to bar remotely"}},
		Output: []sessions.Part{{Role: "assistant", Kind: "text", Text: "Renamed it."}}}); err != nil {
		t.Fatal(err)
	}

	listed := func() map[string]bool {
		t.Helper()
		var out struct{ Sessions []sessions.Session }
		if err := json.Unmarshal([]byte(ok("GET", "/api/sessions", "")), &out); err != nil {
			t.Fatal(err)
		}
		m := map[string]bool{}
		for _, s := range out.Sessions {
			m[s.Agent+":"+s.ID] = true
		}
		return m
	}

	// gateway mode off (the app's windows, or Settings' Off): every one
	if _, err := sessions.Delete("claude", trashed); err != nil {
		t.Fatal(err)
	}
	if l := listed(); !l["claude:"+local] || !l["codex:"+codexID] || !l["pi:"+remote] {
		t.Fatalf("gateway mode off: listed %v", l)
	}
	for _, key := range []string{"claude:" + local, "codex:" + codexID} {
		ok("GET", "/api/sessions/one?key="+key, "")
	}
	if b := ok("GET", "/api/sessions/transcript?agent=claude&id="+local, ""); !strings.Contains(b, "secret plan") {
		t.Fatalf("gateway mode off: the local transcript: %s", b)
	}
	if b := ok("GET", "/api/sessions/stats?days=0", ""); !strings.Contains(b, `"claude"`) {
		t.Fatalf("gateway mode off: stats leave out Claude Code: %s", b)
	}
	var trash struct {
		Trash []struct{ Key, ID string }
	}
	json.Unmarshal([]byte(ok("GET", "/api/sessions/manage?agent=claude", "")), &trash)
	trashKey := ""
	for _, tr := range trash.Trash {
		if tr.ID == trashed {
			trashKey = tr.Key
		}
	}
	if trashKey == "" {
		t.Fatalf("gateway mode off: the trash has no %s: %+v", trashed, trash)
	}

	// magpie web's page, put in gateway mode by Settings
	webPage.Store(true)
	t.Cleanup(func() {
		webPage.Store(false)
		ownWas.Store(0)
		sessions.Reset()
		forgetStats()
	})
	ok("POST", "/api/settings/gateway-mode", `{"mode":"on"}`)
	if settings.Load().GatewayMode != "on" {
		t.Fatal("gateway mode wasn't saved")
	}
	ownLeft := func(what, body string) {
		t.Helper()
		for _, s := range []string{local, trashed, codexID, "secret plan", "old local plan", `"claude"`, `"codex"`, "/work"} {
			if strings.Contains(body, s) {
				t.Fatalf("gateway mode: %s says %q:\n%s", what, s, body)
			}
		}
	}
	if l := listed(); len(l) != 1 || !l["pi:"+remote] {
		t.Fatalf("gateway mode: listed %v, want the gateway's session alone", l)
	}
	b := ok("GET", "/api/sessions", "")
	ownLeft("the list", b)
	if strings.Contains(b, filepath.ToSlash(claude)) || strings.Contains(b, ".claude") || strings.Contains(b, ".codex") {
		t.Fatalf("gateway mode: the list names this computer's folders: %s", b)
	}
	for _, days := range []string{"0", "7"} {
		ownLeft("stats", ok("GET", "/api/sessions/stats?days="+days, ""))
		b = ok("GET", "/api/sessions/overview?days="+days, "")
		ownLeft("the overview", b)
		if !strings.Contains(b, remote) {
			t.Fatalf("gateway mode: the overview leaves out the gateway's session: %s", b)
		}
	}
	for _, key := range []string{"claude:" + local, "codex:" + codexID} {
		if code, b := call("GET", "/api/sessions/one?key="+key, ""); code == 200 {
			t.Fatalf("gateway mode: one %s: %d %s", key, code, b)
		}
	}
	ok("GET", "/api/sessions/one?key=pi:"+remote, "")
	for _, q := range []string{"agent=claude&id=" + local, "agent=codex&id=" + codexID} {
		for _, p := range []string{"GET /api/sessions/transcript", "GET /api/sessions/markdown", "POST /api/sessions/export"} {
			method, path, _ := strings.Cut(p, " ")
			if code, b := call(method, path+"?"+q, "{}"); code == 200 || strings.Contains(b, "secret") {
				t.Fatalf("gateway mode: %s?%s: %d %s", p, q, code, b)
			}
		}
	}
	if b := ok("GET", "/api/sessions/transcript?agent=pi&id="+remote, ""); !strings.Contains(b, "rename foo") {
		t.Fatalf("gateway mode: the gateway's transcript: %s", b)
	}
	if b := ok("GET", "/api/sessions/markdown?agent=pi&id="+remote, ""); !strings.Contains(b, "rename foo") {
		t.Fatalf("gateway mode: the gateway's Markdown: %s", b)
	}
	ownLeft("search", ok("GET", "/api/sessions/search?agent=claude&q=secret", ""))
	if b := ok("GET", "/api/sessions/search?agent=pi&q=remotely", ""); !strings.Contains(b, remote) {
		t.Fatalf("gateway mode: search leaves out the gateway's session: %s", b)
	}
	for _, agent := range []string{"", "claude", "codex"} {
		b = ok("GET", "/api/sessions/manage?agent="+agent, "")
		ownLeft("manage", b)
		var m struct {
			Agents []struct{ Agent string }
			Reads  readsJSON
		}
		json.Unmarshal([]byte(b), &m)
		if len(m.Agents) != 1 || m.Agents[0].Agent != "pi" || !m.Reads.Gateway || len(m.Reads.Found) != 0 {
			t.Fatalf("gateway mode: manage = %s", b)
		}
	}
	if b := ok("GET", "/api/sessions/reads", ""); !strings.Contains(b, `"gateway":true`) || strings.Contains(b, `"claude"`) {
		t.Fatalf("gateway mode: reads = %s", b)
	}
	b = ok("POST", "/api/sessions/delete", `{"agent":"claude","ids":["`+local+`"]}`)
	if _, err := os.Stat(localFile); err != nil || !strings.Contains(b, `"refused":[{`) {
		t.Fatalf("gateway mode: delete of a local session: %s (%v)", b, err)
	}
	if code, b := call("POST", "/api/sessions/carry", `{"agent":"claude","id":"`+local+`","to":"codex"}`); code == 200 {
		t.Fatalf("gateway mode: carry: %s", b)
	}
	if code, b := call("POST", "/api/sessions/restore", `{"key":"`+trashKey+`"}`); code == 200 {
		t.Fatalf("gateway mode: restore of a local session: %s", b)
	}
	for _, body := range []string{`{"keys":["` + trashKey + `"]}`, `{"all":true}`} {
		if b := ok("POST", "/api/sessions/purge", body); !strings.Contains(b, `"purged":[]`) {
			t.Fatalf("gateway mode: purge of a local session: %s", b)
		}
	}
	if code, b := call("POST", "/api/sessions/relocate-claude", `{"from":"/work","to":"/elsewhere"}`); code == 200 {
		t.Fatalf("gateway mode: relocating Claude Code's folder: %s", b)
	}

	// Off again: this computer's sessions are back at once
	ok("POST", "/api/settings/gateway-mode", `{"mode":"off"}`)
	if l := listed(); !l["claude:"+local] || !l["codex:"+codexID] || !l["pi:"+remote] {
		t.Fatalf("gateway mode turned off: listed %v", l)
	}
	if b := ok("GET", "/api/sessions/manage?agent=claude", ""); !strings.Contains(b, trashed) {
		t.Fatalf("gateway mode turned off: the trash lost the local session: %s", b)
	}
}
