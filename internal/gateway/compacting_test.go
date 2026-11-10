package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Each agent's compaction request is told from its prompt; an ordinary
// request, or one only quoting a prompt earlier on, isn't.
func TestCompacting(t *testing.T) {
	user := func(text string) Message { return Message{Role: "user", Parts: []Part{{Kind: Text, Text: text}}} }
	said := Message{Role: "assistant", Parts: []Part{{Kind: Text, Text: "done"}}}
	for name, req := range map[string]*Request{
		"claude code": {System: "You are Claude Code, Anthropic's official CLI for Claude.\nYou are a helpful AI assistant tasked with summarizing conversations.",
			Messages: []Message{user("fix it"), said, user("Your task is to create a detailed summary of the conversation so far, paying close attention to the user's explicit requests")}},
		"codex":      {Messages: []Message{user("fix it"), said, user(codexCompactPrompt)}},
		"opencode":   {System: "You are a context summarization agent. You are given a conversation between a user and an agent.", Messages: []Message{user("fix it")}},
		"pi":         {System: "You are a context summarization assistant. Your task is to read a conversation", Messages: []Message{user("<conversation>…</conversation>")}},
		"gemini cli": {System: "You are a specialized system component responsible for distilling chat history into a structured XML <state_snapshot>.", Messages: []Message{user("fix it")}},
		"qwen code":  {System: "You are the component that summarizes a conversation when its context window is about to overflow. The summary", Messages: []Message{user("fix it")}},
		"dsh":        {Messages: []Message{user("fix it"), said, user(dshCompactAsk)}},
		"kimi":       {Messages: []Message{user("fix it"), said, user("---\n\nThe above is a list of messages in an agent conversation. You are now given a task to compact this conversation context according to specific priorities and rules.")}},
	} {
		if !compacting(req) {
			t.Errorf("%s: not taken for compacting", name)
		}
	}
	for name, req := range map[string]*Request{
		"plain":  {System: "You are a coding agent.", Messages: []Message{user("fix it")}},
		"quoted": {Messages: []Message{user(codexCompactPrompt), said, user("thanks, go on")}},
		"nil":    nil,
	} {
		if compacting(req) {
			t.Errorf("%s: taken for compacting", name)
		}
	}
}

// A rule for compacting sends an agent's compaction to its model on its
// own: the turn it happens in stays where it was, before and after; a
// compaction longer than the rule's model takes goes by the next rule that
// matches; a group with no such rule routes one as before.
func TestRuleCompact(t *testing.T) {
	s, a, b := ruled(t, provider.Rule{Use: "a/small", Compact: true}, provider.Rule{Use: "b/big", Tokens: 2000})
	type sent struct {
		body string
		want string
		n    int
		held bool
	}
	ccSystem := `{"role":"system","content":"You are a helpful AI assistant tasked with summarizing conversations."},`
	compaction := func(first string) string {
		return `{"model":"group/r","messages":[` + ccSystem + `{"role":"user","content":` + quote(first) + `},{"role":"assistant","content":"done"},` +
			`{"role":"user","content":"Your task is to create a detailed summary of the conversation so far, paying close attention to the user's explicit requests."}]}`
	}
	first := long(5000)
	for i, tc := range []sent{
		{chat(first, nil, 0, ""), "kb", 2, false}, // the turn begins long: to b
		{chat(first, nil, 1, ""), "kb", 2, true},  // its tool round stays
		{compaction(first), "ka", 1, false},       // Claude Code compacts it: to a
		{chat(first, nil, 2, ""), "kb", 2, true},  // and the turn goes on on b
		{strings.Replace(chat(first, nil, 0, ""), `]}`, `,{"role":"assistant","content":"ok"},{"role":"user","content":`+quote(codexCompactPrompt)+`}]}`, 1), "ka", 1, false}, // Codex's
		{chat(first, nil, 3, ""), "kb", 2, true},
	} {
		code, out := postAs(t, s, "sess", tc.body)
		r := lastRoute(s)
		if code != 200 || !strings.Contains(out, "from "+tc.want) || r.Rule == nil || r.Rule.N != tc.n || r.Rule.Held != tc.held || r.Rule.Compact != (tc.want == "ka") {
			t.Fatalf("request %d: %d %s %+v", i+1, code, out, r.Rule)
		}
	}
	if a.n() != 2 || b.n() != 4 {
		t.Fatalf("a %d b %d", a.n(), b.n())
	}

	// longer than a's 64k: passed over for the next rule
	code, out := postAs(t, s, "big", compaction(long(70000)))
	r := lastRoute(s)
	if code != 200 || !strings.Contains(out, "from kb") || !r.Rule.Compact || r.Rule.N != 2 || len(r.Rule.Small) != 1 || r.Rule.Small[0] != "a/small" {
		t.Fatalf("too long: %d %s %+v", code, out, r.Rule)
	}

	// no rule for compacting: as before
	s, _, _ = ruled(t, provider.Rule{Use: "b/big", Tokens: 2000})
	postAs(t, s, "sess", chat(first, nil, 0, ""))
	code, out = postAs(t, s, "sess", compaction(first))
	if r := lastRoute(s); code != 200 || !strings.Contains(out, "from kb") || r.Rule.Compact || r.Rule.N != 1 {
		t.Fatalf("no compact rule: %d %s %+v", code, out, r.Rule)
	}
}

// compact is a condition of its own, typed and read back.
func TestCompactRuleWords(t *testing.T) {
	g := provider.Group{ID: "g", Members: []string{"a/small", "b/big"}}
	r, _, _, err := provider.ParseRule(g, []string{"use=small", "compact"})
	if err != nil || !r.Compact || r.Use != "a/small" {
		t.Fatalf("%+v %v", r, err)
	}
	if r.Line() != "use=a/small compact" || strings.Join(r.Conditions(), ",") != "compacting" {
		t.Fatalf("%q %v", r.Line(), r.Conditions())
	}
	if _, _, _, err := provider.ParseRule(g, []string{"use=small", "compact=maybe"}); err == nil {
		t.Fatal("compact=maybe was taken")
	}
	if !r.Matches(provider.RuleRequest{Compact: true}) || r.Matches(provider.RuleRequest{Tokens: 1 << 20}) {
		t.Fatal("Matches")
	}
}

// A compaction a rule sent to a model of its own isn't a turn of the
// conversation: when it answers, the conversation stays where it was
// (gateway.go guards answered with !hit.Compact), so when it breaks off
// mid-reply the stick must be left alone too — unanswered forgot it
// whatever the request was, and the agent's next turn, after a compaction
// that failed, was told to go by routing again instead of the account that
// had been answering it (#733's follow-up, noted on #776's merge).
func TestCompactionBrokeOffKeepsTheStick(t *testing.T) {
	const broke = "Unable to reach the model provider: We're having trouble connecting to the model provider."
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		who := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.Header().Set("Content-Type", "text/event-stream")
		if who == "broken" && strings.Contains(string(body), "detailed summary") { // the compaction alone breaks off
			io.WriteString(w, sse(`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"Let me look"}}]}`,
				`data: {"error":{"message":"`+broke+`"}}`,
				`data: [DONE]`))
			return
		}
		io.WriteString(w, sse(`data: {"id":"c2","choices":[{"index":0,"delta":{"role":"assistant","content":"from fine"}}]}`,
			`data: {"id":"c2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: {"id":"c2","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":3000,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":2500}}}`,
			`data: [DONE]`))
	}))
	defer up.Close()
	for _, p := range []provider.Provider{
		{ID: "flaky", Name: "Flaky", Key: "broken", Chat: up.URL + "/v1", Models: []string{"m"}},
		{ID: "steady", Name: "Steady", Key: "fine", Chat: up.URL + "/v1", Models: []string{"m"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"flaky/m", "steady/m"}, Routing: provider.Ordered,
		Rules: []provider.Rule{{Use: "flaky/m", Compact: true}}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	post := func(body string) string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("x-session-id", "s1")
		s.Handler().ServeHTTP(rec, req)
		return rec.Body.String()
	}
	ccSystem := `{"role":"system","content":"You are a helpful AI assistant tasked with summarizing conversations."},`
	compaction := func(first string) string {
		return `{"model":"group/g","stream":true,"messages":[` + ccSystem + `{"role":"user","content":` + quote(first) + `},{"role":"assistant","content":"done"},` +
			`{"role":"user","content":"Your task is to create a detailed summary of the conversation so far, paying close attention to the user's explicit requests."}]}`
	}
	// an ordinary turn: flaky answers it, and the conversation stays there
	post(`{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	t.Logf("first turn: %+v", s.trace.routes[len(s.trace.routes)-1])
	sticks.Lock()
	var key string
	var st stick
	for k, v := range sticks.m { // the one conversation of this test
		key, st = k, v
	}
	sticks.Unlock()
	if st.who != "flaky#"+provider.KeyID("broken") {
		t.Fatalf("the first turn's answerer: %+v", st)
	}
	// a compaction, which the rule sends to flaky's own model, breaks off:
	// the agent replays the same conversation, so the request is in the one
	// the first turn answered in
	post(compaction("hi"))
	r := s.trace.routes[len(s.trace.routes)-1]
	if n := len(r.Tries); n != 1 || r.Tries[0].Status != 200 || r.Tries[0].Fail == "" {
		t.Fatalf("the broken compaction isn't told as failed: %+v", r.Tries)
	}
	sticks.Lock()
	_, kept := sticks.m[key]
	sticks.Unlock()
	if !kept {
		t.Fatal("a compaction that broke off took the conversation off the account that answers it")
	}
}

// Minis's prompts as Minis sends them (OpenMinis b4c0661: Android's
// ChatViewModel.compactSummarySystemPrompt and its user message, iOS's
// AIChatViewModel+Compaction.swift, whose line continuations render the
// first paragraph to the same bytes) (#1635, jueda12).
const minisCompactSystem = `You are a context compaction engine. Your summary will REPLACE the original messages in the conversation context window. The agent will read your summary as past context, then proceed based on the user's NEXT message — your summary is background, not a standing work order. Write the summary in the same language the user used in the conversation.

MUST PRESERVE (never omit or shorten):
- All file paths, directory names, URLs, UUIDs, and identifiers — copy verbatim
- Commands executed and their outcomes (success/failure/output)
- What was requested and what was done (record as past events, not as ongoing goals)
- Key decisions made and their rationale
- Errors encountered and how they were resolved
- Important constraints, rules, or user preferences mentioned
- Any tool calls and their results that affect current state

STRUCTURE:
1. Start with a one-line description of what the conversation was about (use past tense — "User asked X, agent did Y", NOT "Goal: X").
2. Then a concise narrative of what happened, preserving technical details.
3. End with a "What had been done so far" section listing completed work — NOT a "todo" or "pending" list. Do not invent ongoing objectives or carry-over tasks from old turns; if the user wants to continue, they will say so in their next message.

PRIORITIZE recent context over older history — recent decisions and recent file/path references are most useful for continuity.

Do NOT translate or alter code snippets, file paths, identifiers, or error messages. Be concise but never lose information the agent needs.`

func minisCompactAsk(conversation string) string {
	return "Compact this conversation into a context summary:\n\n" + conversation +
		"\n\n---\nEND OF CONVERSATION TO COMPACT.\n\n" +
		"Now generate a structured context summary following the system prompt instructions. Do NOT continue the conversation above — summarize it. " +
		"Write everything in past tense, framed as \"what was discussed / what was done\", NOT as an ongoing goal or todo list."
}

// Minis compacts with one request, no tools: its own system prompt and the
// conversation wrapped in one user message. Either one tells it; an
// ordinary Minis turn, asking to compact something else or after a
// compaction, isn't one.
func TestCompactingMinis(t *testing.T) {
	user := func(text string) Message { return Message{Role: "user", Parts: []Part{{Kind: Text, Text: text}}} }
	said := Message{Role: "assistant", Parts: []Part{{Kind: Text, Text: "done"}}}
	conversation := "[user]: 幫我整理桌面的檔案\n[assistant]: 已整理完成"
	for name, req := range map[string]*Request{
		"minis":          {System: minisCompactSystem, Messages: []Message{user(minisCompactAsk(conversation))}},
		"system only":    {System: minisCompactSystem, Messages: []Message{user(conversation)}},
		"user only":      {Messages: []Message{user(minisCompactAsk(conversation))}},
		"system message": {System: "You are an on-device AI assistant.\n\n" + minisCompactSystem, Messages: []Message{user(minisCompactAsk(conversation))}},
	} {
		if !compacting(req) {
			t.Errorf("%s: not taken for compacting", name)
		}
	}
	for name, req := range map[string]*Request{
		"turn":  {System: "You are an on-device AI assistant running on iOS.", Messages: []Message{user("Compact this conversation into a short note for my boss")}},
		"after": {System: "You are an on-device AI assistant running on iOS.", Messages: []Message{user(minisCompactAsk(conversation)), said, user("繼續")}},
	} {
		if compacting(req) {
			t.Errorf("%s: taken for compacting", name)
		}
	}
}

// A system prompt that only quotes a compaction prompt, behind the agent's
// own, isn't a compaction: Minis puts its memory (GLOBAL.md, the daily log)
// after its own prompt in every request, so one memory holding Minis's
// compaction line took every turn of that conversation for one (#1635,
// jueda12: 28 requests in a row, one with 4 tokens of chat, went to the
// compaction rule's model). The prompts that lead the system prompt, alone
// or behind a short lead, still are.
func TestCompactingIgnoresQuotedPromptInSystem(t *testing.T) {
	user := func(text string) Message { return Message{Role: "user", Parts: []Part{{Kind: Text, Text: text}}} }
	// Minis's own prompt runs to thousands of tokens (19k-23k with its
	// tools, in the report) before the memory it adds
	own := "You are an on-device AI assistant running on iOS.\n\n" +
		strings.Repeat("- Use the tools to read and change the user's files, notes and reminders; ask before deleting anything.\n", 60)
	notes := "以下是先前的筆記：\n\n" +
		"「You are a context compaction engine. Your summary will REPLACE the original messages in the conversation context window.」\n\n" +
		"其餘是一般對話內容……"
	for name, req := range map[string]*Request{
		"memory":      {System: own + "\n\n" + notes, Messages: []Message{user("hello there")}},
		"claude code": {System: own + "\n\nYou are a helpful AI assistant tasked with summarizing conversations.", Messages: []Message{user("hello there")}},
	} {
		if compacting(req) {
			t.Errorf("%s: a system prompt that merely quotes a compaction prompt was taken for a compaction", name)
		}
	}
	conversation := "[user]: 幫我整理桌面的檔案\n[assistant]: 已整理完成"
	for name, req := range map[string]*Request{
		"alone":       {System: minisCompactSystem, Messages: []Message{user(conversation)}},
		"short lead":  {System: "You are an on-device AI assistant.\n\n" + minisCompactSystem, Messages: []Message{user(conversation)}},
		"claude code": {System: "x-anthropic-billing-header: cc_version=2.1.296; cc_entrypoint=cli;You are Claude Code, Anthropic's official CLI for Claude.You are a helpful AI assistant tasked with summarizing conversations.", Messages: []Message{user("fix it")}},
		"kimi (1.52)": {System: "You are a helpful assistant that compacts conversation context.", Messages: []Message{user("## Message 1\nRole: user\nContent:\nfix it")}},
		"gemini cli":  {System: "You are a specialized system component responsible for distilling chat history into a structured XML <state_snapshot>.", Messages: []Message{user("fix it")}},
	} {
		if !compacting(req) {
			t.Errorf("%s: not taken for compacting", name)
		}
	}
}

// The last user message counts as a compaction's ask only where the agent
// puts its ask: at the start of a text part (Claude Code 2.1.296's behind
// its TEXT ONLY preamble; Kimi's in a part of its own after the
// conversation), or, for Minis, anywhere in a part that opens with Minis's
// own first line. A user writing about one of them, as on #1635, isn't
// compacting.
func TestCompactingAsksWhereTheAgentPutsThem(t *testing.T) {
	user := func(parts ...string) Message {
		m := Message{Role: "user"}
		for _, p := range parts {
			m.Parts = append(m.Parts, Part{Kind: Text, Text: p})
		}
		return m
	}
	said := Message{Role: "assistant", Parts: []Part{{Kind: Text, Text: "done"}}}
	// Claude Code 2.1.296's compaction ask: its preamble (hnn), then the
	// instructions
	ccAsk := "CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.\n\n" +
		"- Do NOT use Read, Bash, Grep, Glob, Edit, Write, or ANY other tool.\n" +
		"- You already have all the context you need in the conversation above.\n" +
		"- Tool calls will be REJECTED and will waste your only turn — you will fail the task.\n" +
		"- Your entire response must be plain text: an <analysis> block followed by a <summary> block.\n\n" +
		"Your task is to create a detailed summary of the conversation so far, paying close attention to the user's explicit requests and your previous actions."
	history := strings.Repeat("[user]: 幫我整理桌面的檔案\n[assistant]: 已整理完成\n", 200) // well past compactLead
	for name, req := range map[string]*Request{
		"claude code": {Messages: []Message{user("fix it"), said, user(ccAsk)}},
		"kimi": {Messages: []Message{user("## Message 1\nRole: user\nContent:\n", history,
			"\n---\n\nThe above is a list of messages in an agent conversation. You are now given a task to compact this conversation context according to specific priorities and rules.")}},
		"minis": {Messages: []Message{user(minisCompactAsk(history))}},
	} {
		if !compacting(req) {
			t.Errorf("%s: not taken for compacting", name)
		}
	}
	for name, req := range map[string]*Request{
		"minis marker quoted": {Messages: []Message{user("Minis 壓縮時，會把整段對話包成一則以 \"END OF CONVERSATION TO COMPACT.\" 結尾的 user 訊息，這樣對嗎？")}},
		"ask deep in a paste": {Messages: []Message{user(history + "\nYour task is to create a detailed summary of the conversation so far — what does this line in my log mean?")}},
	} {
		if compacting(req) {
			t.Errorf("%s: taken for compacting", name)
		}
	}
}

// Alma's compaction ask, as the report on #2833 quotes it: the first bytes
// of the last user message, the conversation behind it.
const almaCompactAsk = "Merge the previous summary with the new conversation. Produce the <analysis>, then the merged structured summary exactly as instructed. The conversation continues after your summary — do not conclude it."

// Alma compacting is recognised by its ask, at the start of the last user
// message (#2833): a group's compact rule sends it to its model and says so
// in X-Magpie-Model, while a note that only quotes the ask further into a
// long message goes by the rule that matches it, as compactLead bounds it.
func TestRuleCompactAlma(t *testing.T) {
	s, _, _ := ruled(t, provider.Rule{Use: "a/small", Compact: true}, provider.Rule{Use: "b/big", Tokens: 1})
	post := func(body string) (*httptest.ResponseRecorder, Route) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("x-session-id", "alma")
		s.Handler().ServeHTTP(rec, req)
		return rec, lastRoute(s)
	}
	alma := func(text string) string {
		return `{"model":"group/r","messages":[{"role":"user","content":` + quote(text) + `}]}`
	}

	rec, r := post(alma(almaCompactAsk + "\n\n[user]: tidy up my desktop\n[assistant]: done"))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from ka") ||
		rec.Header().Get(modelHeader) != "a/small" || r.Rule == nil || !r.Rule.Compact || r.Rule.N != 1 {
		t.Fatalf("Alma compacting: %d %s %q %+v", rec.Code, rec.Body.String(), rec.Header().Get(modelHeader), r.Rule)
	}

	// a long message that quotes the ask deep in is no compaction: only
	// compactLead keeps it from being one, as it does the agents' own asks
	history := strings.Repeat("[user]: tidy up my desktop\n[assistant]: done\n", 200)
	rec, r = post(alma(history + "\n" + almaCompactAsk + "\nWhat does this line in my log mean?"))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from kb") ||
		rec.Header().Get(modelHeader) != "b/big" || r.Rule == nil || r.Rule.Compact || r.Rule.N != 2 {
		t.Fatalf("a quoted Alma ask: %d %s %q %+v", rec.Code, rec.Body.String(), rec.Header().Get(modelHeader), r.Rule)
	}

	// Alma's first compaction, which has no summary to merge (0.4.335's
	// own prompts), by its ask and by its system prompt alone
	first := "Here is the full conversation to compact. Produce the <analysis>, then the structured summary exactly as instructed. The conversation continues after your summary — do not conclude it.\n\n<conversation>\n[user]: tidy up my desktop\n</conversation>"
	for name, body := range map[string]string{
		"first ask":    alma(first),
		"first system": `{"model":"group/r","messages":[{"role":"system","content":"You are compacting a long conversation so it can continue WITHOUT losing context. Your summary will REPLACE the earlier messages."},{"role":"user","content":"tidy up"}]}`,
		"merge system": `{"model":"group/r","messages":[{"role":"system","content":"You are UPDATING an existing conversation summary with new activity so the conversation can continue WITHOUT losing context."},{"role":"user","content":"tidy up"}]}`,
	} {
		rec, r = post(body)
		if rec.Code != 200 || rec.Header().Get(modelHeader) != "a/small" || r.Rule == nil || !r.Rule.Compact {
			t.Errorf("Alma %s: %d %s %q %+v", name, rec.Code, rec.Body.String(), rec.Header().Get(modelHeader), r.Rule)
		}
	}
}
