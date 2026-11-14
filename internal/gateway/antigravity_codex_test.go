package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codexGPT5System is the start of Codex's own prompt for gpt-5.6-sol, as
// Codex 0.162.1 sends it: the first developer message of the thread.
const codexGPT5System = "You are Codex, an agent based on GPT-5. You and the user share one workspace, and your job is to collaborate with them until their goal is genuinely handled."

// Antigravity answers Codex's GPT-5 identity line in the system instruction
// with the same 429 it gives Claude Code's prompt, in any case; the lines
// it answers were each tried on a real Antigravity account (10-27).
func TestAntigravityTurnsAwayCodexsGPT5Prompt(t *testing.T) {
	for _, system := range []string{
		codexGPT5System,
		"You are Codex, a coding agent based on GPT-5. You and the user share the same workspace.",
		"you are codex, an agent based on gpt-5.",
		"Some instructions first.\n" + codexGPT5System,
	} {
		if !antigravityTurnsAway(system) {
			t.Errorf("%q isn't taken for what Antigravity turns away", system)
		}
		if antigravityHint(system) != antigravityCodexHint {
			t.Errorf("%q isn't told Codex's hint", system)
		}
	}
	// what it answers: magpie's own Codex prompt for other models, GPT-6's,
	// the line reworded, and the identity alone
	for _, system := range []string{
		"You are Codex, based on GPT-5. You are running as a coding agent in the Codex CLI on a user's computer.",
		"You are Codex, an agent based on GPT-6.",
		"You are Codex, an AI agent based on GPT-5.",
		"You are Codex, an agent built on GPT-5.",
		"You are Codex, a coding agent.",
	} {
		if antigravityTurnsAway(system) {
			t.Errorf("%q is taken for what Antigravity turns away; it answers it", system)
		}
	}
	if antigravityHint(claudeCodeSystem) != antigravityTurnedAwayHint {
		t.Error("Claude Code's prompt isn't told its own hint")
	}
}

// codexAGTurn is a Codex turn on Antigravity's model itself, on the Responses
// API, with Codex's GPT-5 prompt as its first developer message. switched
// is a thread begun on a GPT model and moved to this one (/model, resume
// -m, a fork as an agent's thread): it keeps that prompt first and says
// the switch in a later developer message, after a reply. search is
// Codex's web_search tool, which it sends every turn and an Antigravity
// model is given magpie's search for.
func codexAGTurn(switched, search bool) string {
	input := `{"type":"message","role":"developer","content":[{"type":"input_text","text":` + quote(codexGPT5System) + `}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}`
	if switched {
		input += `,{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]},` +
			`{"type":"message","role":"developer","content":[{"type":"input_text","text":"<model_switch>\nThe user was previously using a different model.\n</model_switch>"}]},` +
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}`
	}
	tools := ""
	if search {
		tools = `,"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{}}},{"type":"web_search"}]`
	}
	return `{"model":"antigravity/gemini-3-flash","stream":true,"input":[` + input + `]` + tools + `}`
}

// Jodiezzzzzz on X (10-27): every Codex call to antigravity/claude-opus-5-5
// ended in "exceeded retry limit, last status: 429 Too Many Requests", the
// account's only one, while dsh was served on the same account and model.
// The thread carried Codex's GPT-5 prompt, which Antigravity turns away:
// Codex is told why at a status it doesn't retry, Antigravity is asked
// once, and the account doesn't rest.
func TestAntigravityTurnsCodexsGPT5ThreadAwayOnce(t *testing.T) {
	for _, c := range []struct{ switched, search bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		switched, search := c.switched, c.search
		t.Run(fmt.Sprintf("switched=%v,search=%v", switched, search), func(t *testing.T) {
			asked := 0
			var system string
			// a model magpie's search can be served by, as a Google
			// sign-in's own Gemini searches for it
			s, askedUsage := antigravityGroupFor(t, []string{"u@example.com"}, "gemini-3-flash", func(w http.ResponseWriter, r *http.Request) {
				asked++
				b, _ := io.ReadAll(r.Body)
				system = string(b)
				w.WriteHeader(http.StatusTooManyRequests)
				io.WriteString(w, turnedAway)
			})
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(codexAGTurn(switched, search))))
			code, out := rec.Code, rec.Body.String()
			if !strings.Contains(system, "systemInstruction") || !strings.Contains(system, "an agent built on GPT-5") || search != strings.Contains(system, `"web_search"`) {
				t.Fatalf("the prompt didn't go as the system instruction, so this isn't the case: %s", system)
			}
			if code != http.StatusBadRequest {
				t.Fatalf("Codex got %d, which it retries: %s", code, out)
			}
			if !strings.Contains(out, "Resource has been exhausted") || !strings.Contains(out, antigravityCodexHint) {
				t.Fatalf("Codex isn't told Antigravity's words and why: %s", out)
			}
			if asked != 1 {
				t.Fatalf("Antigravity was asked %d times", asked)
			}
			r := lastRoute(s)
			if len(r.Tries) != 1 || r.Tries[0].Fail != failPrompt || r.Tries[0].Rest != nil {
				t.Fatalf("the try: %+v", r.Tries)
			}
			if *askedUsage != 0 {
				t.Fatalf("the account's usage was asked again %d times", *askedUsage)
			}
			if _, ok := restOf("antigravity@u@example.com"); ok {
				t.Fatalf("the account rests for a prompt it turned away: %v", rests())
			}
		})
	}
}

// In a group, the next Codex turn carrying that prompt skips Antigravity
// as Claude Code's does, and the skip says Codex's hint.
func TestAntigravityForeseesCodexsGPT5Prompt(t *testing.T) {
	asked := 0
	s, _ := antigravityTurnedAwayGroup(t, []string{"u@example.com"}, func(w http.ResponseWriter, r *http.Request) {
		asked++
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, turnedAway)
	})
	for range 2 {
		if code, out := turnOn(t, s, claudeTurn(codexGPT5System)); code != 200 || !strings.Contains(out, "from the other") {
			t.Fatalf("the turn: %d %s", code, out)
		}
	}
	if asked != 1 {
		t.Fatalf("Antigravity was asked %d times", asked)
	}
	if c := s.Recent()[0]; !strings.Contains(c.Fallback, antigravityCodexHint) {
		t.Fatalf("the skip doesn't say why: %q", c.Fallback)
	}
}

func TestAntigravityCodexHintIsTheRoutingPages(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "gui", "assets", "plugins", "routing", "client.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), quote(antigravityCodexHint)) {
		t.Fatal("plugins/routing/client.js doesn't know the note the gateway adds to Antigravity's refusal of Codex's prompt")
	}
	if !strings.Contains(string(b), quote(antigravityHermesHint)) {
		t.Fatal("plugins/routing/client.js doesn't know the note the gateway adds to Antigravity's refusal of Hermes' old persona")
	}
}
