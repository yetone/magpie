package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

const sealedHandoff = `{"type":"agent_message","author":"/root","recipient":"/root/worker","content":[{"type":"input_text","text":"Message Type: NEW_TASK\nPayload:\n"},{"type":"encrypted_content","encrypted_content":"gAAAAATest_ciphertext=="}]}`

func TestCodexSealedAgentHandoffGuidesBeforeRouting(t *testing.T) {
	for _, encoding := range []string{"identity", "gzip", "zstd"} {
		t.Run(encoding, func(t *testing.T) {
			f := &fake{t: t}
			setup(t, provider.Responses, f)
			var nativeCalls int
			chatgpt(t, func(w http.ResponseWriter, r *http.Request) { nativeCalls++ })
			body := `{"model":"fake/m1","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Start"}]},` + sealedHandoff + `,{"type":"compaction_trigger"}]}`
			req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(string(encodedBody(t, []byte(body), encoding))))
			req.Header.Set("Content-Encoding", encoding)
			rec := httptest.NewRecorder()
			New().Handler().ServeHTTP(rec, req)
			var response struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if rec.Code != 400 || response.Error.Type != "invalid_request_error" ||
				!strings.Contains(response.Error.Message, "OpenAI lead") ||
				!strings.Contains(response.Error.Message, "Magpie-served model for the lead") ||
				!strings.Contains(response.Error.Message, "OpenAI subagent") {
				t.Fatalf("guidance: status=%d response=%s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "gAAAAA") || f.calls != 0 || nativeCalls != 0 {
				t.Fatalf("ciphertext leaked or request forwarded: fake=%d native=%d", f.calls, nativeCalls)
			}
		})
	}
}

func TestCodexSealedAgentHandoffDoesNotAffectOtherMessages(t *testing.T) {
	for _, tc := range []struct {
		name, input string
	}{
		{"plain agent task", `[{"type":"agent_message","content":[{"type":"input_text","text":"Message Type: NEW_TASK\nPayload:\nreply PONG"}]}]`},
		{"non-native encrypted part", `[{"type":"agent_message","content":[{"type":"input_text","text":"Payload:\n"},{"type":"encrypted_content","encrypted_content":"plain task"}]}]`},
		{"other encrypted item", `[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"},{"type":"encrypted_content","encrypted_content":"gAAAAATest_ciphertext=="}]}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{t: t, reply: sse(`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`)}
			setup(t, provider.Responses, f)
			body := `{"model":"fake/m1","stream":true,"input":` + tc.input + `}`
			code, response := post(t, CodexPath+"/responses", body)
			if code != 200 || f.calls != 1 || strings.Contains(response, "sealed subagent task") {
				t.Fatalf("normal request: status=%d upstream=%d response=%s", code, f.calls, response)
			}
			if tc.name == "plain agent task" && !strings.Contains(string(f.got), "reply PONG") {
				t.Fatal("plain task was not delivered")
			}
		})
	}
}

func TestCodexSealedAgentHandoffNativePassthrough(t *testing.T) {
	var got []byte
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		io.WriteString(w, sse(`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`))
	})
	body := `{"model":"gpt-5.5","stream":true,"input":[` + sealedHandoff + `]}`
	code, response := codexPost(t, body)
	if code != 200 || !strings.Contains(string(got), "gAAAAATest_ciphertext==") || strings.Contains(response, "sealed subagent task") {
		t.Fatalf("native request: status=%d forwarded=%v response=%s", code, len(got) > 0, response)
	}
}
