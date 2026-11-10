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

// hermesOldSystem is Hermes Agent's earlier default persona, the SOUL.md it
// seeded until it replaced it, which a SOUL.md the user edited keeps.
const hermesOldSystem = "You are Hermes Agent, an intelligent AI assistant created by Nous Research. You are helpful, knowledgeable, and direct. You assist users with a wide range of tasks including answering questions, writing and editing code, analyzing information, creative work, and executing actions via your tools."

// googleTurnsAway is what a real Antigravity account turned away, each tried
// on one on 10-27: the line as written, in the system instruction, in any
// case. Broken up by a zero-width space after the first letter, each went
// through.
var googleTurnsAway = []string{
	"x-anthropic-billing-header:",
	"built on anthropic's claude agent sdk",
	"you are codex, an agent based on gpt-5",
	"you are codex, a coding agent based on gpt-5",
	"hermes agent, an intelligent ai assistant created by nous research",
}

// brokenTwice is line with "|" after the first and the second character of
// each run of three or more ASCII letters or digits.
func brokenTwice(line string) string {
	var b strings.Builder
	for i := 0; i < len(line); {
		j := i
		for j < len(line) && agAlnum(line[j]) {
			j++
		}
		switch {
		case j == i:
			b.WriteByte(line[i])
			i++
		case j-i >= 3:
			b.WriteString(line[i:i+1] + "|" + line[i+1:i+2] + "|" + line[i+2:j])
			i = j
		default:
			b.WriteString(line[i:j])
			i = j
		}
	}
	return b.String()
}

// Claude Code 2.1.316's own system blocks (claude -p, captured 11-11): the
// first is turned away, once broken after only its first letter.
func TestAntigravityCloakClaudeCode2316(t *testing.T) {
	system := "x-anthropic-billing-header: cc_version=2.1.316.2e4; cc_entrypoint=sdk-cli;\nYou are a Claude agent, built on Anthropic's Claude Agent SDK.\n"
	got := antigravityCloak(system)
	want := "x-a|n|thropic-b|i|lling-h|e|ader: cc_version=2.1.316.2e4; cc_entrypoint=sdk-cli;\nYou are a Claude agent, b|u|ilt on A|n|thropic's C|l|aude A|g|ent S|D|K.\n"
	if got != strings.ReplaceAll(want, "|", zeroWidth) {
		t.Fatalf("went as %q", got)
	}
	if antigravityCloak(got) != got {
		t.Fatal("broken again")
	}
}

func TestAntigravityCloakBreaksEveryLineItTurnsAway(t *testing.T) {
	for _, system := range []string{
		claudeCodeSystem,
		"You are a Claude agent, built on Anthropic's Claude Agent SDK.",
		codexGPT5System,
		"You are Codex, a coding agent based on GPT-5. You and the user share the same workspace.",
		hermesOldSystem,
		strings.ToUpper(hermesOldSystem),
		// twice, and after text whose lower case is longer than it is
		"İstanbul ✓ " + codexGPT5System + "\n\n" + hermesOldSystem + "\n" + codexGPT5System,
	} {
		if !antigravityTurnsAway(system) {
			t.Fatalf("%q isn't taken for what Antigravity turns away", system)
		}
		got := antigravityCloak(system)
		if antigravityTurnsAway(got) {
			t.Errorf("a line Antigravity turns away is still whole in %q", got)
		}
		if strings.ReplaceAll(got, zeroWidth, "") != system {
			t.Errorf("more than zero-width spaces changed: %q", got)
		}
		if !strings.Contains(got, zeroWidth) {
			t.Errorf("nothing broken up: %q", got)
		}
		// nothing else: every run of three or more letters or digits in a
		// line has a break after its first and its second character, the
		// way Google's check lets through (11-11, a real account)
		low := strings.ToLower(strings.ReplaceAll(got, zeroWidth, "|"))
		for _, line := range googleTurnsAway {
			if strings.Contains(strings.ToLower(system), line) && !strings.Contains(low, brokenTwice(line)) {
				t.Errorf("%q isn't broken after its first two characters in %q", line, got)
			}
		}
		if antigravityCloak(got) != got {
			t.Errorf("a prompt broken up once is broken again")
		}
	}
	// what Antigravity answers is sent as written, word for word
	for _, system := range []string{
		"",
		"You are opencode, an interactive CLI tool.",
		"You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.",
		"You are Codex, based on GPT-5. You are running as a coding agent in the Codex CLI on a user's computer.",
		"You are Hermes Agent, built by Nous Research. Be direct.",
		"You are Hermes Agent, an intelligent AI assistant created by Nous.",
	} {
		if got := antigravityCloak(system); got != system {
			t.Errorf("%q went as %q", system, got)
		}
	}
}

func TestAntigravityHermesHint(t *testing.T) {
	if antigravityHint(hermesOldSystem) != antigravityHermesHint {
		t.Error("Hermes' old persona isn't told its own hint")
	}
	if antigravityHint(codexGPT5System) != antigravityCodexHint || antigravityHint(claudeCodeSystem) != antigravityTurnedAwayHint {
		t.Error("the other prompts lost their hints")
	}
}

// fakeGoogle answers as a real Antigravity account did on 10-27: the 429
// for a system instruction holding one of googleTurnsAway's lines whole,
// and a reply otherwise. sent is the system instruction it was last sent.
func fakeGoogle(sent *string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var env struct {
			Request struct {
				SystemInstruction struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"systemInstruction"`
			} `json:"request"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &env)
		*sent = ""
		for _, p := range env.Request.SystemInstruction.Parts {
			*sent += p.Text
		}
		low := strings.ToLower(*sent)
		for _, line := range googleTurnsAway {
			if strings.Contains(low, line) {
				w.WriteHeader(http.StatusTooManyRequests)
				io.WriteString(w, turnedAway)
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"from google"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3}}}`))
	}
}

// hermesTurn is a Hermes Agent turn on Antigravity's model, on the Chat
// Completions API it reaches magpie by, with system as its system prompt.
func hermesTurn(s *Server, system string) (int, string) {
	body := `{"model":"antigravity/gemini-3-flash","stream":false,"messages":[{"role":"system","content":` + quote(system) + `},{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("User-Agent", "hermes-agent/0.12")
	s.Handler().ServeHTTP(rec, r)
	return rec.Code, rec.Body.String()
}

func codexAGPost(s *Server, body string) (int, string) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

// zgoodz on X (10-27): "antigravity在Hermes里一样429", and Jodiezzzzzz's Codex
// thread begun on a GPT model: both are answered by Antigravity with no setup,
// as the lines it turns away go broken up. Sent as written, the setting the
// user can turn on, they are refused as before, told why at a status the
// agent doesn't retry.
func TestAntigravityAnswersHermesAndCodexByDefault(t *testing.T) {
	var sent string
	s, _ := antigravityGroupFor(t, []string{"u@example.com"}, "gemini-3-flash", fakeGoogle(&sent))

	if code, out := hermesTurn(s, hermesOldSystem); code != 200 || !strings.Contains(out, "from google") {
		t.Fatalf("Hermes: %d %s", code, out)
	}
	if strings.ReplaceAll(sent, zeroWidth, "") != hermesOldSystem || sent == hermesOldSystem {
		t.Fatalf("Hermes' prompt went as %q", sent)
	}
	for _, search := range []bool{false, true} {
		if code, out := codexAGPost(s, codexAGTurn(true, search)); code != 200 || !strings.Contains(out, "from google") {
			t.Fatalf("Codex's switched thread (search=%v): %d %s", search, code, out)
		}
		if !strings.Contains(strings.ReplaceAll(sent, zeroWidth, "|"), brokenTwice("You are Codex, an agent based on GPT-5")) || antigravityTurnsAway(sent) {
			t.Fatalf("Codex's prompt went as %q", sent)
		}
	}

	p, err := provider.Find("antigravity")
	if err != nil {
		t.Fatal(err)
	}
	p.PromptAsWritten = true
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if p, _ := provider.Find("antigravity"); !p.PromptAsWritten {
		t.Fatal("prompt=as-written isn't kept")
	}
	code, out := hermesTurn(s, hermesOldSystem)
	if sent != hermesOldSystem {
		t.Fatalf("sent as written, Hermes' prompt went as %q", sent)
	}
	if code != http.StatusBadRequest || !strings.Contains(out, antigravityHermesHint) {
		t.Fatalf("Hermes, as written: %d %s", code, out)
	}
	code, out = codexAGPost(s, codexAGTurn(true, false))
	if !strings.Contains(sent, codexGPT5System) {
		t.Fatalf("sent as written, Codex's prompt went as %q", sent)
	}
	if code != http.StatusBadRequest || !strings.Contains(out, antigravityCodexHint) {
		t.Fatalf("Codex, as written: %d %s", code, out)
	}
}

// Only an Antigravity account's system instruction is broken up: Gemini
// CLI's own sign-in, on the same Code Assist envelope, and every other
// provider are sent the prompt as the agent wrote it.
func TestOnlyAntigravityIsSentThePromptBrokenUp(t *testing.T) {
	r := &Request{System: hermesOldSystem + "\n" + codexGPT5System, Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}}}}
	gemini := provider.Provider{ID: "gemini-cli", Account: &provider.Account{Agent: "gemini"}}
	if b := string(codeAssistBody(gemini, r, "gemini-3-flash", nil)); !strings.Contains(b, "You are Hermes Agent, an intelligent") || strings.Contains(b, `​`) || strings.Contains(b, zeroWidth) {
		t.Fatalf("Gemini CLI's account was sent %s", b)
	}
	ag := provider.Provider{ID: "antigravity", Account: &provider.Account{Agent: "antigravity"}}
	if b := string(codeAssistBody(ag, r, "gemini-3-flash", nil)); strings.Contains(b, "You are Hermes Agent, an intelligent") {
		t.Fatalf("Antigravity's account was sent %s", b)
	}
	if r.System != hermesOldSystem+"\n"+codexGPT5System {
		t.Fatalf("the caller's request was changed: %q", r.System)
	}
	gem, err := buildGemini(r, "m")
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{
		"chat":      buildChat(r, "m", "", false),
		"anthropic": buildAnthropic(r, "m"),
		"responses": buildResponses(r, "m", "", false),
		"gemini":    gem,
	} {
		if !strings.Contains(string(b), "You are Hermes Agent, an intelligent") || strings.Contains(string(b), zeroWidth) || strings.Contains(string(b), `\u200b`) {
			t.Errorf("%s was sent the prompt broken up: %s", name, b)
		}
	}
}
