//go:build darwin || linux

package gui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

// recih: Web.Wait re-executed magpie without ending its gateway, so an
// idle Claude session never reached the shelf and the next turn replayed
// the entire conversation. Exercise Wait, including its real Unix exec,
// with a local Claude Code stand-in that records what it is told.
func TestWebShutdownKeepsClaudeSession(t *testing.T) {
	if n := len(os.Args); n >= 4 && os.Args[n-4] == "web-shutdown-child" {
		webShutdownChild(t, os.Args[n-3], os.Args[n-2], os.Args[n-1])
		return
	}
	for _, mode := range []string{"quit", "restart"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			testenv.SetHome(t, home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
			t.Setenv("MAGPIE_ADDR", "127.0.0.1:0")
			config := filepath.Join(home, ".claude")
			t.Setenv("CLAUDE_CONFIG_DIR", config)
			write := func(path string, value any) {
				t.Helper()
				b, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, b, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(config, ".credentials.json"), map[string]any{"claudeAiOauth": map[string]any{
				"accessToken": "test-token", "refreshToken": "test-refresh", "expiresAt": time.Now().Add(24 * time.Hour).UnixMilli(), "subscriptionType": "max",
			}})
			write(filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": map[string]string{
				"emailAddress": "web-restart@example.com", "accountUuid": "web-restart",
			}})
			if err := settings.Save(settings.Settings{ClaudePrintMode: true}); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			testenv.Program(t, filepath.Join(dir, "claude"), webShutdownClaude)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, self, "-test.run=^TestWebShutdownKeepsClaudeSession$", "-test.v", "--", "web-shutdown-child", mode, "before", home)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s: %v\n%s", mode, err, out)
			}
		})
	}
}

func webShutdownChild(t *testing.T, mode, phase, home string) {
	t.Helper()
	if phase == "after" {
		if _, err := os.Stat(filepath.Join(settings.Dir(), "claude-shelf.json")); err != nil {
			t.Fatalf("Web.Wait did not keep the idle Claude session before %s: %v", mode, err)
		}
	}
	provider.ForgetAccounts()
	gw := serveGateway()
	if gw == nil {
		t.Fatal("test gateway did not start")
	}
	t.Cleanup(stopServing)
	eventually(t, "test gateway did not listen", func() bool { return gw.Addr() != "" })
	msgs := `[{"role":"user","content":"first-turn"}]`
	if phase == "after" {
		msgs = `[{"role":"user","content":"first-turn"},{"role":"assistant","content":"ok"},{"role":"user","content":"next-turn"}]`
	}
	c := &http.Client{Timeout: 10 * time.Second}
	res, err := c.Post("http://"+gw.Addr()+"/v1/messages", "application/json", strings.NewReader(
		`{"model":"claude/claude-sonnet-5","max_tokens":100,"tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":`+msgs+`}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || res.StatusCode != 200 || !strings.Contains(string(body), "ok") {
		t.Fatalf("Claude turn: %d %s, %v", res.StatusCode, body, err)
	}
	if phase == "after" {
		b, err := os.ReadFile(filepath.Join(home, "claude-resumed"))
		if err != nil {
			t.Fatalf("next turn did not resume the saved Claude session: %v", err)
		}
		if !strings.Contains(string(b), "next-turn") || strings.Contains(string(b), "first-turn") {
			t.Fatalf("resumed Claude was told more than the new turn: %s", b)
		}
		return
	}
	w := &Web{srv: &http.Server{}, quit: make(chan struct{}), key: "0123456789abcdef0123456789abcdef"}
	close(w.quit)
	webReexec.Store(mode == "restart")
	// Reexec passes these arguments to this test binary, now as the new
	// magpie. No production process or installed binary is restarted.
	os.Args[len(os.Args)-2] = "after"
	if err := w.Wait(); err != nil {
		t.Fatal(err)
	}
	if mode == "restart" {
		t.Fatal("the Unix restart returned without replacing the process")
	}
	if _, err := os.Stat(filepath.Join(settings.Dir(), "claude-shelf.json")); err != nil {
		t.Fatalf("Web.Wait did not keep the idle Claude session before quit: %v", err)
	}
	backendCtx, endBackend = context.WithCancel(context.Background())
	served.Store(nil)
	webShutdownChild(t, mode, "after", home)
}

const webShutdownClaude = `#!/bin/sh
if [ "$1" = auth ]; then echo '{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","email":"web-restart@example.com","subscriptionType":"max"}'; exit 0; fi
sid=s$$
resumed=
prev=
for a in "$@"; do
  if [ "$prev" = "--resume" ]; then sid=$a; resumed=1; fi
  prev=$a
done
proj=$(pwd -P | sed 's/[^A-Za-z0-9]/-/g')
mkdir -p "$CLAUDE_CONFIG_DIR/projects/$proj"
file="$CLAUDE_CONFIG_DIR/projects/$proj/$sid.jsonl"
echo '{"type":"system","subtype":"init","session_id":"'$sid'"}'
while read -r line; do
  case $line in *'"type":"control_request"'*)
    id=$(printf '%s' "$line" | sed 's/.*"request_id":"\([^"]*\)".*/\1/')
    echo '{"type":"control_response","response":{"subtype":"success","request_id":"'$id'"}}'
    continue ;;
  esac
  printf '%s\n' "$line" >> "$file"
  if [ -n "$resumed" ]; then printf '%s\n' "$line" >> "$HOME/claude-resumed"; fi
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`
