package gateway

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/claudetui"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// claudeSilentAt is a Claude subscription with one account whose Claude
// Code answers a message of at most 17 images, saying how many it got,
// and one of 18 or more with past's shell lines. Control requests (the
// interactive mode's initialize) are acknowledged.
func claudeSilentAt(t *testing.T, past string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	far := time.Now().Add(24 * time.Hour).UnixMilli()
	oauth := map[string]any{"claudeAiOauth": map[string]any{"accessToken": "tok-me", "refreshToken": "r", "expiresAt": far, "subscriptionType": "max"}}
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), mustJSON(oauth), 0o600)
	os.WriteFile(filepath.Join(home, ".claude.json"), mustJSON(map[string]any{
		"oauthAccount": map[string]any{"emailAddress": "me@example.com", "accountUuid": "u-me"}}), 0o600)
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), mustJSON([]map[string]any{
		{"agent": "claude", "user": "me@example.com", "plan": "max", "on": true, "seen": time.Now(), "auth": oauth},
	}), 0o600)
	dir := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = auth ]; then echo '{"loggedIn":true,"authMethod":"oauth_token","apiProvider":"firstParty"}'; exit 0; fi
while read -r line; do
  case $line in *'"type":"control_request"'*)
    id=$(printf '%s' "$line" | sed 's/.*"request_id":"\([^"]*\)".*/\1/')
    echo '{"type":"control_response","response":{"subtype":"success","request_id":"'$id'"}}'
    continue ;;
  esac
  n=$(printf '%s' "$line" | grep -o '"type":"image"' | wc -l | tr -d ' ')
  if [ "$n" -le 17 ]; then
    echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5-5","usage":{"input_tokens":22933}}}}'
    echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"saw '$n' frames"}}}'
    echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}}'
    echo '{"type":"stream_event","event":{"type":"message_stop"}}'
    echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
  else
` + past + `  fi
done
`
	testenv.Program(t, filepath.Join(dir, "claude"), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	runsEndWithTest(t, dir)
}

// The reporter's request, in each protocol: n copies of a small PNG, as
// data URLs in Chat's image_url parts (steven_4422's), and as each other
// protocol carries an image.
func manyImages(path string, n int, stream bool) string {
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	var parts []string
	switch path {
	case "/v1/chat/completions":
		parts = append(parts, `{"type":"text","text":"Describe each frame."}`)
		for range n {
			parts = append(parts, `{"type":"image_url","image_url":{"url":"data:image/png;base64,`+png+`"}}`)
		}
		return fmt.Sprintf(`{"model":"claude/claude-sonnet-5-5","stream":%v,"messages":[{"role":"user","content":[%s]}]}`, stream, strings.Join(parts, ","))
	case "/v1/messages":
		parts = append(parts, `{"type":"text","text":"Describe each frame."}`)
		for range n {
			parts = append(parts, `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`+png+`"}}`)
		}
		return fmt.Sprintf(`{"model":"claude/claude-sonnet-5-5","max_tokens":100,"stream":%v,"messages":[{"role":"user","content":[%s]}]}`, stream, strings.Join(parts, ","))
	default:
		parts = append(parts, `{"type":"input_text","text":"Describe each frame."}`)
		for range n {
			parts = append(parts, `{"type":"input_image","image_url":"data:image/png;base64,`+png+`"}`)
		}
		return fmt.Sprintf(`{"model":"claude/claude-sonnet-5-5","stream":%v,"input":[{"role":"user","content":[%s]}]}`, stream, strings.Join(parts, ","))
	}
}

// A Claude subscription turn Claude Code ends with no message is a
// failure that says why, in every protocol, streamed or not, by -p and
// by the interactive mode. steven_4422 sent 18 PNGs to
// claude/claude-sonnet-5-5 in a Chat request not streamed and was
// answered 200 with content null and no usage, as if that were the
// answer; 17 were answered. Claude Code's run had ended the turn with
// nothing said: the bridge closed the reply with nothing in it.
func TestClaudeSilentTurnFails(t *testing.T) {
	pasts := []struct{ name, lines, why string }{
		// its result says it succeeded, with no message before it
		{"result", `    echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
`, "ended the turn without an answer"},
		// only words of its own, a message no model wrote
		{"synthetic", `    echo '{"type":"assistant","message":{"id":"s1","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"No response requested."}]}}'
    echo '{"type":"result","subtype":"success","is_error":false,"result":"No response requested."}'
`, "No response requested."},
		// it exits, saying why on its stderr only
		{"exit", `    echo 'claude-tui: Claude Code did not take the message' >&2
    exit 1
`, "did not take the message"},
	}
	asks := []string{"/v1/chat/completions", "/v1/messages", "/v1/responses"}
	for _, tui := range []bool{false, true} {
		for _, past := range pasts {
			for _, path := range asks {
				for _, stream := range []bool{false, true} {
					t.Run(fmt.Sprintf("tui=%v/%s%s/stream=%v", tui, past.name, path, stream), func(t *testing.T) {
						claudeSilentAt(t, past.lines)
						if tui {
							claudeTUIRuns = true
							t.Cleanup(func() { claudeTUIRuns = false })
							helper := filepath.Join(t.TempDir(), "magpie")
							testenv.Program(t, helper, "#!/bin/sh\n[ \"$1\" = \""+claudetui.Command+"\" ] || exit 9\nshift; claude=$1; shift\nexec \"$claude\" \"$@\"\n")
							claudeTUIHelper = helper
							t.Cleanup(func() { claudeTUIHelper = "" })
						}
						s := New()
						t.Cleanup(s.subscription.abortAll)
						send := func(n int) (int, string) {
							type answer struct {
								code int
								body string
							}
							done := make(chan answer, 1)
							go func() {
								code, body := sendTo(s, path, manyImages(path, n, stream))
								done <- answer{code, body}
							}()
							select {
							case a := <-done:
								return a.code, a.body
							case <-time.After(20 * time.Second):
								t.Fatalf("%d images: the reply waited on a turn that had ended", n)
							}
							return 0, ""
						}
						// 17 reach Claude Code, all of them, and are answered
						if code, body := send(17); code != 200 || !strings.Contains(body, "saw 17 frames") {
							t.Fatalf("17 images: status %d: %s", code, body)
						}
						code, body := send(18)
						if code != 502 || !strings.Contains(body, past.why) {
							t.Fatalf("18 images: status %d, not a 502 saying %q:\n%s", code, past.why, body)
						}
						if strings.Contains(body, `"content":null`) || strings.Contains(body, "saw 18") {
							t.Fatalf("18 images: an answer of nothing:\n%s", body)
						}
					})
				}
			}
		}
	}
}

// A reply whose events end with neither its stop nor an error — a run
// ended by magpie (its timeout) with nothing said, or cut part way — is
// a failure, not a finished reply of nothing, streamed or not.
func TestRelayUnendedReplyFails(t *testing.T) {
	for _, said := range []string{"", "half an ans"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("said=%q/stream=%v", said, stream), func(t *testing.T) {
				events := make(chan Event, 4)
				events <- Event{Kind: KStart, Model: "claude-sonnet-5-5"}
				if said != "" {
					events <- Event{Kind: KText, Text: said}
				}
				close(events)
				rec := httptest.NewRecorder()
				r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
				var u Usage
				var ok bool
				aborted := false
				code, msg := relay(rec, r, provider.Chat, "Claude Code", &Request{Model: "claude-sonnet-5-5", Stream: stream}, events, &u,
					func() { aborted = true }, nil, func(_, _ string, o bool) { ok = o })
				if msg == "" || ok {
					t.Fatalf("status %d, failed %q, ended ok %v:\n%s", code, msg, ok, rec.Body.String())
				}
				if stream {
					if !strings.Contains(rec.Body.String(), `"error":{`) || strings.Contains(rec.Body.String(), "[DONE]") {
						t.Fatalf("a stream finished as a reply:\n%s", rec.Body.String())
					}
					return
				}
				if code != 502 || !aborted || strings.Contains(rec.Body.String(), `"choices"`) {
					t.Fatalf("status %d, aborted %v:\n%s", code, aborted, rec.Body.String())
				}
			})
		}
	}
}
