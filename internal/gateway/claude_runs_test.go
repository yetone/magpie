package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/claudetui"
	"github.com/yetone/magpie/internal/provider"
)

// listClaudeRuns asks srv for its Claude Code runs as `magpie claude-code
// runs` does, over loopback.
func listClaudeRuns(t *testing.T, srv *Server) ClaudeRuns {
	t.Helper()
	r := httptest.NewRequest("GET", ClaudeRunsPath, nil)
	r.RemoteAddr, r.Host = "127.0.0.1:50123", "127.0.0.1:3425"
	r.Header.Set("User-Agent", "Go-http-client/1.1")
	w := httptest.NewRecorder()
	lanGuard(srv.Handler()).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", ClaudeRunsPath, w.Code, w.Body.String())
	}
	var out ClaudeRuns
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Object != "claude-runs" {
		t.Fatalf("%s answered %s (%v)", ClaudeRunsPath, w.Body.String(), err)
	}
	return out
}

// runsUntil asks for the runs until ok holds of them, 10s at most.
func runsUntil(t *testing.T, srv *Server, what string, ok func(ClaudeRuns) bool) ClaudeRuns {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		l := listClaudeRuns(t, srv)
		if ok(l) {
			return l
		}
		if time.Now().After(deadline) {
			t.Fatalf("never %s: %+v", what, l)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A Claude account's conversations, a subagent's each, run on Claude Code
// processes of their own that magpie keeps for the next turn or while the
// client's tools run, and a task manager shows them as Claude Code's
// version and "magpie" (@Candydidi3 on X: "magpie里拉起很多子进程").
// ClaudeRunsPath names each: its pid, model and account, whether it
// answers, waits for its next turn or on tools, since when and until
// when magpie keeps it, and drops it once it ends.
func TestClaudeRunsNamesEachProcess(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")

	t.Run("kept for its next turn", func(t *testing.T) {
		fakeClaude(t)
		s := New()
		t.Cleanup(s.subscription.abortAll)
		p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "me@example.com"}}
		body := `{"model":"claude-opus-4-1","max_tokens":100,"tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"a subagent's task"}]}`
		rec := httptest.NewRecorder()
		var u Usage
		before := time.Now()
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		if code, msg := s.serveClaudeSubscription(rec, req, provider.Anthropic, p, "claude-opus-4-1", []byte(body), &u); code != 200 {
			t.Fatalf("%d %s", code, msg)
		}
		m := regexp.MustCompile(`pid (\d+) turn 1`).FindStringSubmatch(rec.Body.String())
		if m == nil {
			t.Fatalf("the reply names no pid: %s", rec.Body.String())
		}
		pid, _ := strconv.Atoi(m[1])
		l := runsUntil(t, s, "idle", func(l ClaudeRuns) bool { return len(l.Runs) == 1 && l.Runs[0].State == "idle" })
		r := l.Runs[0]
		if r.PID != pid || !alive(pid) || r.Model != "claude-opus-4-1" || r.Account != "me@example.com" {
			t.Errorf("the run is listed as %+v; Claude Code is pid %d", r, pid)
		}
		if r.Started.Before(before) || r.Since.Before(r.Started) || time.Since(r.Since) > time.Minute {
			t.Errorf("started %v, idle since %v (asked at %v)", r.Started, r.Since, before)
		}
		if left := time.Until(r.Ends); left < idleLongest-time.Minute || left > idleLongest {
			t.Errorf("it ends in %s, not the %s an idle run is kept", left, idleLongest)
		}
		if l.Live < 1 || l.IdleMost != idleMost || l.IdleFor != idleLongest {
			t.Errorf("live %d, idleMost %d, idleFor %s", l.Live, l.IdleMost, l.IdleFor)
		}
		s.subscription.abortAll()
		runsUntil(t, s, "dropped once it ended", func(l ClaudeRuns) bool { return len(l.Runs) == 0 })
	})

	t.Run("waiting on tools, and answering", func(t *testing.T) {
		dir := fakeClaudeCalling(t)
		s := New()
		t.Cleanup(s.subscription.abortAll)
		pid := askForCall(t, s, context.Background(), "subagent: read the file")
		if pid == 0 {
			t.Fatal("no tool call")
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
			body := `{"model":"claude-sonnet-5","max_tokens":100,"tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hang on"}]}`
			var u Usage
			req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)).WithContext(ctx)
			s.serveClaudeSubscription(httptest.NewRecorder(), req, provider.Anthropic, p, "claude-sonnet-5", []byte(body), &u)
		}()
		t.Cleanup(func() { cancel(); <-done })
		var hung int
		l := runsUntil(t, s, "two runs, one answering", func(l ClaudeRuns) bool {
			b, err := os.ReadFile(filepath.Join(dir, "hang.pid"))
			hung, _ = strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil || hung <= 0 || len(l.Runs) != 2 {
				return false
			}
			// the hung run's pid is listed once launch has set its tree,
			// a moment after Claude Code wrote hang.pid
			for _, r := range l.Runs {
				if r.PID == hung {
					return true
				}
			}
			return false
		})
		by := map[int]ClaudeRun{}
		for _, r := range l.Runs {
			by[r.PID] = r
		}
		if r := by[pid]; r.State != "tools" || r.Model != "claude-sonnet-5" || r.Account != "u" || !r.Ends.After(time.Now()) || time.Until(r.Ends) > runTimeout {
			t.Errorf("the run waiting on tools, pid %d, is listed as %+v", pid, r)
		}
		if r := by[hung]; r.State != "answering" || r.Since.IsZero() {
			t.Errorf("the run answering, pid %d, is listed as %+v", hung, r)
		}
		cancel()
		<-done
		runsUntil(t, s, "the given-up turn's run dropped", func(l ClaudeRuns) bool {
			return len(l.Runs) == 1 && l.Runs[0].PID == pid
		})
	})

	t.Run("to this computer's programs only", func(t *testing.T) {
		srv := New()
		for _, c := range profileCallersRefused("magpie-no-such-key") {
			r := httptest.NewRequest("GET", ClaudeRunsPath, nil)
			r.RemoteAddr, r.Host = c.remote, c.host
			for i := 0; i+1 < len(c.hdr); i += 2 {
				r.Header.Set(c.hdr[i], c.hdr[i+1])
			}
			w := httptest.NewRecorder()
			lanGuard(srv.Handler()).ServeHTTP(w, r)
			if w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
				t.Errorf("%s: %d %s", c.name, w.Code, w.Body.String())
			}
		}
	})
}

// screensOf asks srv for its runs with their screens, as `magpie
// claude-code screen` does.
func screensOf(t *testing.T, srv *Server) ClaudeRuns {
	t.Helper()
	r := httptest.NewRequest("GET", ClaudeRunsPath+"?screen=1", nil)
	r.RemoteAddr, r.Host = "127.0.0.1:50123", "127.0.0.1:3425"
	r.Header.Set("User-Agent", "Go-http-client/1.1")
	w := httptest.NewRecorder()
	lanGuard(srv.Handler()).ServeHTTP(w, r)
	var out ClaudeRuns
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("%s?screen=1: %d %s", ClaudeRunsPath, w.Code, w.Body.String())
	}
	return out
}

// A hidden interactive Claude Code that seems stuck shows where: its
// screen as it stands, asked of its helper (claudetui.ScreenRequest), row
// by row as drawn, with the home folders and secrets on it masked as the
// logs mask them (FremontDango on Discord, #8075). A -p run says it draws
// none, and a helper that doesn't answer is said so in screenWait, not
// waited on.
func TestClaudeRunsShowTheScreen(t *testing.T) {
	was := screenWait
	screenWait = 500 * time.Millisecond
	t.Cleanup(func() { screenWait = was })
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	token := "sk-ant-oat01-" + strings.Repeat("Ab3", 30)
	drawn := []string{
		"╭─── Claude Code ───╮",
		"│   cwd: " + filepath.Join(home, "work", "proj") + "   │",
		"│   C:\\Users\\alice\\proj  /home/bob/x   │",
		"│ > key " + token + " │",
		"╰───────────────────╯",
	}
	s := New()
	b := s.subscription
	tui := func(token string, answer bool, started time.Time) {
		input, stdin := io.Pipe()
		stdout, output := io.Pipe()
		run := &subscriptionRun{bridge: b, token: token, stdin: stdin, tui: true, model: "claude-opus-5-5", started: started}
		b.mu.Lock()
		b.runs[token] = run
		b.mu.Unlock()
		t.Cleanup(func() { stdin.Close(); input.Close(); output.Close(); stdout.Close() })
		go run.readOutput(stdout)
		go func() {
			sc := bufio.NewScanner(input)
			for sc.Scan() {
				var ctl struct {
					RequestID string `json:"request_id"`
					Request   struct {
						Subtype string `json:"subtype"`
					} `json:"request"`
				}
				if !answer || json.Unmarshal(sc.Bytes(), &ctl) != nil || ctl.Request.Subtype != claudetui.ScreenRequest {
					continue
				}
				line, _ := json.Marshal(map[string]any{"type": "control_response", "response": map[string]any{
					"subtype": "success", "request_id": ctl.RequestID, "response": map[string]any{"rows": drawn}}})
				_, _ = output.Write(append(line, '\n'))
			}
		}()
	}
	now := time.Now()
	tui("drawn", true, now.Add(-3*time.Second))
	tui("stuck", false, now.Add(-2*time.Second))
	b.mu.Lock()
	b.runs["print"] = &subscriptionRun{bridge: b, token: "print", model: "claude-sonnet-5", started: now.Add(-time.Second)}
	b.mu.Unlock()
	t.Cleanup(b.abortAll)

	if l := listClaudeRuns(t, s); len(l.Runs) != 3 || l.Runs[0].Screen != nil || l.Runs[0].ScreenWhy != "" {
		t.Fatalf("screens read unasked: %+v", l.Runs)
	}
	start := time.Now()
	l := screensOf(t, s)
	if d := time.Since(start); d > screenWait+5*time.Second {
		t.Errorf("the looks took %s, screenWait %s", d, screenWait)
	}
	if len(l.Runs) != 3 {
		t.Fatalf("runs %+v", l.Runs)
	}
	got := l.Runs[0].Screen
	if len(got) != len(drawn) || got[0] != drawn[0] || got[4] != drawn[4] || l.Runs[0].ScreenWhy != "" {
		t.Fatalf("the screen read %q (%s), drawn %q", got, l.Runs[0].ScreenWhy, drawn)
	}
	whole := strings.Join(got, "\n")
	for _, no := range []string{home, "alice", "bob", token} {
		if strings.Contains(whole, no) {
			t.Errorf("the screen keeps %q: %q", no, got)
		}
	}
	if got[1] != "│   cwd: "+filepath.Join("~", "work", "proj")+"   │" {
		t.Errorf("the home folder's row reads %q", got[1])
	}
	if !strings.HasPrefix(got[3], "│ > key ") {
		t.Errorf("the key's row reads %q", got[3])
	}
	if r := l.Runs[1]; r.Screen != nil || !strings.Contains(r.ScreenWhy, "couldn't be read") {
		t.Errorf("a helper that doesn't answer: %+v", r)
	}
	if r := l.Runs[2]; r.Screen != nil || !strings.Contains(r.ScreenWhy, "draws no screen") {
		t.Errorf("a -p run: %+v", r)
	}
}
