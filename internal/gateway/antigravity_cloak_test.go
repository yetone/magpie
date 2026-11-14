package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// hermesOldSystem is Hermes Agent's earlier default persona, the SOUL.md it
// seeded until it replaced it, which a SOUL.md the user edited keeps.
const hermesOldSystem = "You are Hermes Agent, an intelligent AI assistant created by Nous Research. You are helpful, knowledgeable, and direct. You assist users with a wide range of tasks including answering questions, writing and editing code, analyzing information, creative work, and executing actions via your tools."

// googleTurnsAway is what a real Antigravity account turned away, each tried
// on one on 10-27 and 11-14: the line as written, in the system
// instruction, in any case, and since 11-14 broken up by invisible
// characters too (fakeGoogle reads past them, as Google's check does).
var googleTurnsAway = []string{
	"x-anthropic-billing-header:",
	"built on anthropic's claude agent sdk",
	"you are codex, an agent based on gpt-5",
	"you are codex, a coding agent based on gpt-5",
	"hermes agent, an intelligent ai assistant created by nous research",
	"rfc 2119 keywords: must, required, should, recommended, may, optional.",
}

// zeroWidth is U+200B, what magpie put inside these lines until 11-14.
const zeroWidth = "\u200b"

// brokenTwice is line as magpie sent it from 11-11 to 11-14: a zero-width
// space after the first and the second character of each run of three or
// more ASCII letters or digits.
func brokenTwice(line string) string {
	alnum := func(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' }
	var b strings.Builder
	for i := 0; i < len(line); {
		j := i
		for j < len(line) && alnum(line[j]) {
			j++
		}
		switch {
		case j == i:
			b.WriteByte(line[i])
			i++
		case j-i >= 3:
			b.WriteString(line[i:i+1] + zeroWidth + line[i+1:i+2] + zeroWidth + line[i+2:j])
			i = j
		default:
			b.WriteString(line[i:j])
			i = j
		}
	}
	return b.String()
}

// breakLines is system with f applied to each turned-away line in it and
// nothing else, as an older magpie or middleware broke them up.
func breakLines(system string, f func(string) string) string {
	var at [][]int
	for _, re := range agTurnedAwayRes {
		at = append(at, re.Regexp().FindAllStringIndex(system, -1)...)
	}
	slices.SortFunc(at, func(a, b []int) int { return a[0] - b[0] })
	var b strings.Builder
	end := 0
	for _, m := range at {
		if m[0] < end {
			continue
		}
		b.WriteString(system[end:m[0]] + f(system[m[0]:m[1]]))
		end = m[1]
	}
	return b.String() + system[end:]
}

// joinedBy is a line with sep between every character, as a word joiner
// (U+2060) broke a line up.
func joinedBy(sep string) func(string) string {
	return func(line string) string { return strings.Join(strings.Split(line, ""), sep) }
}

// unsaid is got with each rewording agSaidAs makes but the billing line's
// put back as the line, for checking that nothing else changed.
func unsaid(got string) string {
	got = regexp.MustCompile(`built on the Claude Agent SDK by Anthropic`).ReplaceAllString(got, "built on Anthropic's Claude Agent SDK")
	got = regexp.MustCompile(`(?i)built(\s+)on(\s+)gpt-5`).ReplaceAllString(got, "based${1}on${2}GPT-5")
	got = regexp.MustCompile(`(?i)built(\s+)by(\s+)nous`).ReplaceAllStringFunc(got, func(m string) string {
		return "created" + strings.TrimPrefix(m, "built")
	})
	return strings.Replace(got, "key words", "keywords", 1)
}

// Claude Code 2.1.316's own system blocks (claude -p, captured 11-11; the
// Claude desktop app's Code tab sends the same with cc_entrypoint=
// claude-desktop, captured 11-14): the billing line is left out, the Agent
// SDK's line reworded, and the copy magpie sent until 11-14, which Google
// turned away 5 of 5 that day, goes the same way.
func TestAntigravityCloakClaudeCode2316(t *testing.T) {
	want := "You are a Claude agent, built on the Claude Agent SDK by Anthropic.\n"
	for _, entry := range []string{"sdk-cli", "claude-desktop"} {
		system := "x-anthropic-billing-header: cc_version=2.1.316.2e4; cc_entrypoint=" + entry + ";\nYou are a Claude agent, built on Anthropic's Claude Agent SDK.\n"
		if got := antigravityCloak(system); got != want {
			t.Fatalf("went as %q", got)
		}
		if got := antigravityCloak(breakLines(system, brokenTwice)); got != want {
			t.Fatalf("the copy broken up went as %q", got)
		}
	}
	if antigravityCloak(want) != want {
		t.Fatal("reworded again")
	}
	// the Code tab's main identity line goes through as written
	main := "x-anthropic-billing-header: cc_version=2.1.316.2e4; cc_entrypoint=claude-desktop;\nYou are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.\nYou are an interactive agent."
	if got := antigravityCloak(main); got != "You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.\nYou are an interactive agent." {
		t.Fatalf("the Code tab's prompt went as %q", got)
	}
}

func TestAntigravityCloakRewordsEveryLineItTurnsAway(t *testing.T) {
	for _, system := range []string{
		"You are a Claude agent, built on Anthropic's Claude Agent SDK.",
		codexGPT5System,
		"You are Codex, a coding agent based on GPT-5. You and the user share the same workspace.",
		hermesOldSystem,
		strings.ToUpper(hermesOldSystem),
		ompLine,
		// twice, and after text whose lower case is longer than it is
		"İstanbul ✓ " + codexGPT5System + "\n\n" + hermesOldSystem + "\n" + codexGPT5System,
	} {
		for _, sent := range []string{system, breakLines(system, brokenTwice), breakLines(system, joinedBy("\u2060"))} {
			if !antigravityTurnsAway(sent) {
				t.Fatalf("%q isn't taken for what Antigravity turns away", sent)
			}
			got := antigravityCloak(sent)
			if antigravityTurnsAway(got) {
				t.Errorf("a line Antigravity turns away is still in %q", got)
			}
			if sent == system && unsaid(got) != system && (system != strings.ToUpper(system) || !strings.EqualFold(unsaid(got), system)) {
				t.Errorf("more than the lines changed: %q", got)
			}
			if sent != system && got != antigravityCloak(system) {
				t.Errorf("a copy broken up went as %q, not as the line", got)
			}
			if strings.ContainsAny(got, zeroWidth+"\u2060") {
				t.Errorf("an invisible character went: %q", got)
			}
			if antigravityCloak(got) != got {
				t.Errorf("a prompt reworded once is reworded again: %q", got)
			}
		}
	}
	if got := antigravityCloak(claudeCodeSystem); got != "You are Claude Code, Anthropic's official CLI for Claude." {
		t.Errorf("Claude Code's prompt went as %q", got)
	}
	// what Antigravity answers is sent as written, word for word
	for _, system := range []string{
		"",
		"You are opencode, an interactive CLI tool.",
		"You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.",
		"You are a Claude agent, built on the Claude Agent SDK by Anthropic.",
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

// fakeGoogle answers as a real Antigravity account did on 10-27 and 11-14:
// the 429 for a system instruction holding one of googleTurnsAway's lines,
// whatever invisible characters are inside it,
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
		low := strings.ToLower(agInvisibleRe.ReplaceAllString(*sent, ""))
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
// as the lines it turns away go reworded. Sent as written, the setting the
// user can turn on, they are refused as before, told why at a status the
// agent doesn't retry.
func TestAntigravityAnswersHermesAndCodexByDefault(t *testing.T) {
	var sent string
	s, _ := antigravityGroupFor(t, []string{"u@example.com"}, "gemini-3-flash", fakeGoogle(&sent))

	if code, out := hermesTurn(s, hermesOldSystem); code != 200 || !strings.Contains(out, "from google") {
		t.Fatalf("Hermes: %d %s", code, out)
	}
	if sent != strings.Replace(hermesOldSystem, "created by", "built by", 1) {
		t.Fatalf("Hermes' prompt went as %q", sent)
	}
	for _, search := range []bool{false, true} {
		if code, out := codexAGPost(s, codexAGTurn(true, search)); code != 200 || !strings.Contains(out, "from google") {
			t.Fatalf("Codex's switched thread (search=%v): %d %s", search, code, out)
		}
		if !strings.Contains(sent, "You are Codex, an agent built on GPT-5.") || antigravityTurnsAway(sent) {
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

// Only an Antigravity account's system instruction is reworded: Gemini
// CLI's own sign-in, on the same Code Assist envelope, and every other
// provider are sent the prompt as the agent wrote it.
func TestOnlyAntigravityIsSentThePromptReworded(t *testing.T) {
	r := &Request{System: hermesOldSystem + "\n" + codexGPT5System, Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}}}}
	gemini := provider.Provider{ID: "gemini-cli", Account: &provider.Account{Agent: "gemini"}}
	if b := string(codeAssistBody(gemini, r, "gemini-3-flash", nil)); !strings.Contains(b, "You are Hermes Agent, an intelligent") || strings.Contains(b, `​`) || strings.Contains(b, zeroWidth) {
		t.Fatalf("Gemini CLI's account was sent %s", b)
	}
	ag := provider.Provider{ID: "antigravity", Account: &provider.Account{Agent: "antigravity"}}
	if b := string(codeAssistBody(ag, r, "gemini-3-flash", nil)); strings.Contains(b, "created by Nous Research") || !strings.Contains(b, "built by Nous Research") || strings.Contains(b, "based on GPT-5") {
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

// DeepSeek Harness's paragraph on @ paths goes to an Antigravity account
// reworded, and to every other provider as written (plugins#1028).
func TestAntigravityDSHAtPathsGoReworded(t *testing.T) {
	dsh := agReworded[0][0]
	r := &Request{System: "You are DeepSeek Harness.\n\n" + dsh + "\n\nBe brief.", Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}}}}
	ag := provider.Provider{ID: "antigravity", Account: &provider.Account{Agent: "antigravity"}}
	b := string(codeAssistBody(ag, r, "gemini-4-argon", nil))
	if strings.Contains(b, "Tokens prefixed with @") || !strings.Contains(b, "When the user writes @ followed by a path, they are pointing at that file or folder.") || !strings.Contains(b, "Be brief.") {
		t.Fatalf("Antigravity's account was sent %s", b)
	}
	if r.System != "You are DeepSeek Harness.\n\n"+dsh+"\n\nBe brief." {
		t.Fatalf("the caller's request was changed: %q", r.System)
	}
	gemini := provider.Provider{ID: "gemini-cli", Account: &provider.Account{Agent: "gemini"}}
	if b := string(codeAssistBody(gemini, r, "gemini-3-flash", nil)); !strings.Contains(b, "Tokens prefixed with @ are paths the user explicitly referenced.") {
		t.Fatalf("Gemini CLI's account was sent %s", b)
	}
	asWritten := ag
	asWritten.PromptAsWritten = true
	if b := string(codeAssistBody(asWritten, r, "gemini-4-argon", nil)); !strings.Contains(b, "Tokens prefixed with @ are paths the user explicitly referenced.") {
		t.Fatalf("prompt=as-written was sent %s", b)
	}
	// a prompt that only mentions @ paths, and one with the Claude Code
	// line besides, are each changed only where they match
	if got := antigravityReword("Tokens prefixed with @ are paths. Be brief."); got != "Tokens prefixed with @ are paths. Be brief." {
		t.Fatalf("a mention was changed: %q", got)
	}
	both := antigravityReword(antigravityCloak("x-anthropic-billing-header: cc\n" + dsh))
	if antigravityTurnsAway(both) || !strings.Contains(both, "When the user writes @") {
		t.Fatalf("both reworded: %q", both)
	}
}

// ompSystem is how oh-my-pi (omp) 18.20.0 starts its system prompt
// (src/prompts/system/system-prompt.md), byte for byte.
const ompSystem = "RFC 2119 keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL. `NEVER` = `MUST NOT`; `AVOID` = `SHOULD NOT`.\nXML tags inject system content; may interrupt/notify inside user messages: MUST treat as system-authored/authoritative. User content is sanitized.\n"

// ompLine is omp's first line up to the period, the part Antigravity
// turns away.
const ompLine = "RFC 2119 keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL."

// omp's line, as it starts its prompt, goes as "RFC 2119 key words: …"
// (eric_li8983 on X, 11-14): broken up by zero-width spaces it was
// turned away on a real account, worded so 0 in 10.
func TestAntigravityCloakOmpLine(t *testing.T) {
	got := antigravityCloak(ompSystem)
	if want := strings.Replace(ompSystem, "keywords", "key words", 1); got != want {
		t.Fatalf("went as %q, want %q", got, want)
	}
	if antigravityCloak(got) != got || antigravityTurnsAway(got) {
		t.Errorf("reworded again: %q", got)
	}
	if antigravityHint(ompSystem) != antigravityRFCHint {
		t.Error("omp's line isn't told its own hint")
	}
}

// Google's check reads omp's line in any case with any white space between
// its words: each of these copies was turned away as written on a real
// account on 11-14, and each goes reworded. A copy that only matched as
// written went out whole and was turned away.
func TestAntigravityCloakLineWrappedOrWidened(t *testing.T) {
	copies := []string{
		strings.Replace(ompLine, "REQUIRED, ", "REQUIRED,\n", 1),
		strings.Replace(ompLine, "REQUIRED, ", "REQUIRED,\r\n", 1),
		strings.Replace(ompLine, "2119 ", "2119\n", 1),
		strings.Replace(ompLine, "REQUIRED, ", "REQUIRED,\n   ", 1),
		strings.Replace(ompLine, "MUST, ", "MUST,\t", 1),
		strings.Replace(ompLine, "MUST, ", "MUST,  ", 1),
		strings.Replace(ompLine, "MUST, ", "MUST, ", 1),
		strings.Replace(hermesOldSystem, "AI assistant", "AI\nassistant", 1),
		strings.Replace(codexGPT5System, "agent based", "agent\tbased", 1),
	}
	for _, system := range copies {
		system = "<conventions>\n" + system + "\n</conventions>\n"
		if !antigravityTurnsAway(system) {
			t.Errorf("%q isn't taken for what Antigravity turns away", system)
			continue
		}
		got := antigravityCloak(system)
		if antigravityTurnsAway(got) || got == system {
			t.Errorf("went whole: %q", got)
		}
		if strings.Contains(got, zeroWidth) {
			t.Errorf("broken up, not reworded: %q", got)
		}
		if unsaid(got) != system {
			t.Errorf("more than its words changed: %q", got)
		}
		if antigravityCloak(got) != got {
			t.Errorf("reworded again: %q", got)
		}
	}
	if antigravityHint(copies[1]) != antigravityRFCHint || antigravityHint(copies[7]) != antigravityHermesHint || antigravityHint(copies[8]) != antigravityCodexHint {
		t.Error("a wrapped copy isn't told its own hint")
	}
	// what went through as written on the same account goes as written
	for _, system := range []string{
		strings.TrimSuffix(ompLine, "."),
		strings.Replace(ompLine, "RFC 2119", "RFC2119", 1),
		strings.ReplaceAll(ompLine, ", ", ","),
		"RFC 2119 applies to MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.",
	} {
		if got := antigravityCloak(system); got != system {
			t.Errorf("%q went as %q", system, got)
		}
	}
}
