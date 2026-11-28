//go:build darwin || linux

package tuidrive

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestDrivesAgy drives a real agy (Antigravity's CLI, MAGPIE_TEST_AGY_BIN)
// in this package's terminal, as claudetui drives Claude Code, to show that
// nothing here is Claude Code's: agy is started in a sandbox HOME on an API
// key against a fake Gemini on loopback, the message is pasted into its
// prompt, and its answer is read from the transcript agy writes, not from
// the screen (a call agy refused still draws "● Used …"). Every other host
// goes to a proxy that isn't there, so the test never reaches the network
// and no account is used. agy 1.5.17's screens: "? for shortcuts" in the
// footer when it waits for a message, "esc to cancel" while a turn runs.
func TestDrivesAgy(t *testing.T) {
	agy := os.Getenv("MAGPIE_TEST_AGY_BIN")
	if agy == "" {
		t.Skip("MAGPIE_TEST_AGY_BIN names no agy")
	}
	var (
		mu   sync.Mutex
		said []string
	)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"models":[]}`)
			return
		}
		var body struct {
			Contents []struct {
				Role  string `json:"role"`
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"contents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		last := ""
		for _, c := range body.Contents {
			for _, p := range c.Parts {
				if c.Role == "user" && p.Text != "" {
					last = p.Text
				}
			}
		}
		mu.Lock()
		said = append(said, last)
		mu.Unlock()
		reply := "a title"
		if strings.Contains(last, "hello tuidrive") {
			reply = "pong from the fake"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := map[string]any{
			"candidates":    []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": reply}}}, "finishReason": "STOP", "index": 0}},
			"usageMetadata": map[string]any{"promptTokenCount": 10, "candidatesTokenCount": 4, "totalTokenCount": 14},
		}
		b, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\r\n\r\n", b)
	}))
	defer fake.Close()

	home := t.TempDir()
	ws := filepath.Join(home, "ws")
	ac := filepath.Join(home, ".gemini", "antigravity-cli")
	for _, d := range []string{ws, filepath.Join(ac, "cache"), filepath.Join(home, ".gemini", "config")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	wsReal, _ := filepath.EvalSymlinks(ws)
	write := func(p string, v any) {
		b, _ := json.Marshal(v)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// agy asks nothing before its prompt: onboarding done, the folder trusted
	write(filepath.Join(ac, "settings.json"), map[string]any{"trustedWorkspaces": []string{wsReal}, "modelProvider": "gemini"})
	write(filepath.Join(ac, "cache", "onboarding.json"), map[string]any{"consumerOnboardingComplete": true, "onboardingComplete": true})
	write(filepath.Join(home, ".gemini", "config", "mcp_config.json"), map[string]any{"mcpServers": map[string]any{}})

	none := "http://127.0.0.1:9"
	env := []string{
		"HOME=" + home, "PATH=/usr/bin:/bin", "TERM=xterm-256color", "LANG=en_US.UTF-8",
		"GEMINI_API_KEY=fake-key", "GOOGLE_GEMINI_BASE_URL=" + fake.URL,
		"HTTPS_PROXY=" + none, "HTTP_PROXY=" + none, "https_proxy=" + none, "http_proxy=" + none,
		"NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost",
		// the sign-in in a file under HOME, never the system keyring
		"SSH_CONNECTION=127.0.0.1 0 127.0.0.1 0",
		"AGY_CLI_DISABLE_AUTO_UPDATE=true",
	}
	// agy works in the folder it is started in
	t.Chdir(ws)
	term := New()
	// what agy 1.5.17 asks before it draws, answered as xterm does
	term.Replies = []Reply{
		{"\x1b[>c", "\x1b[>1;10;0c"},
		{"\x1b]11;?\x07", "\x1b]11;rgb:0000/0000/0000\x1b\\"},
		{"\x1b]10;?\x07", "\x1b]10;rgb:ffff/ffff/ffff\x1b\\"},
	}
	if err := term.Start(agy, []string{"--settings", `{"modelProvider":"gemini"}`, "--model", "gemini-3.1-pro-low"}, env); err != nil {
		t.Fatal(err)
	}
	defer term.Stop()

	ready := func(s string) bool {
		return strings.Contains(s, "? for shortcuts") && !strings.Contains(s, "esc to cancel")
	}
	waitFor := func(what string, ok func() bool, d time.Duration) {
		t.Helper()
		end := time.After(d)
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for !ok() {
			select {
			case <-end:
				t.Fatalf("%s: not in %v (%d bytes drawn); the screen:\n%s\nas written:\n%q", what, d, term.BytesRead(), term.Now(), raw(term))
			case <-term.Exited():
				t.Fatalf("%s: agy ended (%v):\n%s", what, term.Exit(), term.LastScreen())
			case <-term.Changed():
			case <-tick.C:
			}
		}
	}
	waitFor("agy's prompt", func() bool { return ready(term.Now()) }, 60*time.Second)

	if _, err := term.Write([]byte("\x1b[200~hello tuidrive\x1b[201~")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := term.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	asked := func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, s := range said {
			if strings.Contains(s, "hello tuidrive") {
				return true
			}
		}
		return false
	}
	waitFor("the message reaching the model", asked, 60*time.Second)

	answer := func() string {
		logs, _ := filepath.Glob(filepath.Join(ac, "brain", "*", ".system_generated", "logs", "transcript_full.jsonl"))
		for _, p := range logs {
			f, err := os.Open(p)
			if err != nil {
				continue
			}
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1<<20), 16<<20)
			for sc.Scan() {
				var row struct{ Type, Content string }
				if json.Unmarshal(sc.Bytes(), &row) == nil && row.Type == "PLANNER_RESPONSE" && row.Content != "" {
					f.Close()
					return row.Content
				}
			}
			f.Close()
		}
		return ""
	}
	waitFor("the answer in agy's transcript", func() bool { return answer() != "" }, 60*time.Second)
	if got := answer(); got != "pong from the fake" {
		t.Fatalf("the transcript's answer is %q", got)
	}
	waitFor("agy's prompt after the turn", func() bool { return ready(term.Now()) }, 60*time.Second)
	// agy draws in place with relative cursor moves, not on the
	// alternate screen: the text as written has the answer in pieces, the
	// grid has it as shown
	if !strings.Contains(term.Now(), "pong from the fake") {
		t.Errorf("the answer is not on the screen:\n%s", term.Now())
	}
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func raw(t *Term) (s string) {
	t.Locked(func() { s = tail(string(t.screen), 3000) })
	return s
}
