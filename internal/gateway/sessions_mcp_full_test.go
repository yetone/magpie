package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// read_session cuts a message at 8000 characters, says so and names
// full=true; with full it gives the message whole, and its next read
// keeps full (hdhfhf_hdh219 on X, for magpie sessions show).
func TestSessionsMCPReadFull(t *testing.T) {
	fresh(t)
	claude := filepath.Join(os.Getenv("HOME"), ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	sessions.Reset()
	t.Cleanup(sessions.Reset)
	at := time.Now().Add(-time.Hour).UTC()
	long := "LONG-START " + strings.Repeat("y", 20_000-len("LONG-START  TAIL-WORDS")) + " TAIL-WORDS"
	var lines []string
	for i, text := range []string{"question one", long, "question two"} {
		lines = append(lines, fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":%q},"cwd":"/work/app","sessionId":"s1","uuid":"u%d"}`, at.Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano), text, i))
	}
	path := filepath.Join(claude, "projects", "-work-app", "s1.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := lanGuard(New().Handler())

	text, isErr := sessionsCall(t, h, sessionsReadTool, `{"session":"claude:s1"}`)
	if isErr || strings.Contains(text, "TAIL-WORDS") || !strings.Contains(text, "(cut: 12000 more characters of this message; "+sessionsReadTool+" with from=1, full=true)") {
		t.Fatalf("by default:\n%.400s", text)
	}
	for _, full := range []string{`true`, `"true"`} {
		text, isErr = sessionsCall(t, h, sessionsReadTool, `{"session":"claude:s1","from":1,"max_chars":1000,"full":`+full+`}`)
		if isErr || !strings.Contains(text, "[1] user:\n"+long+"\n") || strings.Contains(text, "(cut:") || !strings.Contains(text, "(more: "+sessionsReadTool+" with from=2, full=true)") {
			t.Fatalf("full %s:\n%.300s…%s", full, text, text[max(0, len(text)-200):])
		}
	}
}
