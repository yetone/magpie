package gateway

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestLangOfEachLanguage(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"为什么 parse 会 panic？帮我看看 src/parser.rs", langZH},
		{"為什麼 parse 會 panic？幫我看看這個問題", langZHTW},
		{"parse がパニックするのはなぜですか？見てください", langJA},
		{"parse 解析器が落ちる原因を調べて", langJA}, // Han beside kana is Japanese
		{"parse가 왜 패닉하나요? 확인해 주세요", langKO},
		{"Warum stürzt parse ab? Kannst du das bitte prüfen und es nicht löschen", langDE},
		{"why does parse panic? please check it and fix the bug", langEN},
		// mixed: a Chinese ask with English code and errors around it
		{"fn parse(s: &str) -> Ast { todo!() } thread 'main' panicked at src/parser.rs:3 not yet implemented\n修一下", langZH},
		// an English ask quoting one Chinese name or a German word isn't that language
		{"rename the 张 field to name, and the Straße column too", langEN},
		{"", langEN},
		{"   \n ", langEN},
		{"🙂 123 {}", langEN},
	} {
		if got := langOf(c.text); got != c.want {
			t.Errorf("langOf(%q) = %s, want %s", c.text, got, c.want)
		}
	}
}

// The language is the user's words', not a tool's output (a Chinese file
// read in an English conversation), the agent's prompt, or a block the
// agent adds to a user turn; and the newest of them, found however far
// back the user last spoke.
func TestConversationLangReadsTheUsersWords(t *testing.T) {
	zhFile := func(i int) (string, string) {
		return `{"cmd":"cat README.zh.md"}`, fmt.Sprintf("这是一个解析器。它会读取源代码并生成语法树 %d", i)
	}
	for _, from := range []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic, provider.Gemini} {
		body := toolTurns(from, 70, zhFile, false)
		if got := conversationLang(from, []byte(body)); got != langEN {
			t.Errorf("%s: a Chinese tool output in an English conversation read as %s", from, got)
		}
		zh := strings.Replace(toolTurns(from, 70, codexRead, false), "why does parse panic?", "parse 为什么会 panic？", 1)
		if got := conversationLang(from, []byte(zh)); got != langZH {
			t.Errorf("%s: a Chinese ask 70 turns back read as %s", from, got)
		}
	}
	// Claude Code's reminder beside a Japanese ask, in one user turn
	ja := `{"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>\nThe user's CLAUDE.md says: always answer briefly and run the tests.\n</system-reminder>"},{"type":"text","text":"テストが落ちる理由を調べてください"}]}]}`
	if got := conversationLang(provider.Anthropic, []byte(ja)); got != langJA {
		t.Errorf("a Japanese ask beside a reminder read as %s", got)
	}
	// the agent's English prompt and a Chinese system prompt aren't the user's
	sys := `{"system":"你是一个编程助手","messages":[{"role":"user","content":"why does parse panic?"}]}`
	if got := conversationLang(provider.Anthropic, []byte(sys)); got != langEN {
		t.Errorf("a Chinese system prompt made the conversation %s", got)
	}
	if got := conversationLang(provider.Responses, []byte(`{"input":"parse가 왜 패닉하나요?"}`)); got != langKO {
		t.Errorf("Responses' one-string input read as %s", got)
	}
	if got := conversationLang(provider.Chat, []byte(`{"messages":[]}`)); got != langEN {
		t.Errorf("an empty conversation read as %s", got)
	}
}

// tenacious_eagle_96548 on Discord: a Chinese conversation's loop is told
// in Chinese, and every other language in its own, on every protocol;
// the notice names neither magpie nor a quota, since it goes upstream with
// the next turn.
func TestToolLoopNoticeInTheConversationsLanguage(t *testing.T) {
	asks := map[string]struct{ ask, says string }{
		langEN:   {"why does parse panic?", "This turn was stopped"},
		langZH:   {"parse 为什么会 panic？帮我看看", "这一轮在发给模型之前已被停止"},
		langZHTW: {"parse 為什麼會 panic？幫我看看這個", "這一輪在送給模型之前已被停止"},
		langJA:   {"parse がパニックするのはなぜ？", "このターンはモデルに送る前に止めました"},
		langKO:   {"parse가 왜 패닉하나요?", "이 턴은 모델에 보내기 전에 중단되었습니다"},
		langDE:   {"Warum stürzt parse ab? Kannst du das prüfen?", "Dieser Zug wurde angehalten"},
	}
	for lang, a := range asks {
		f := toolLoopNotices[lang]
		if strings.Contains(strings.ToLower(f), "magpie") || strings.Contains(strings.ToLower(f), "quota") || strings.Count(f, "%d") != 1 || strings.Count(f, "%s") != 1 {
			t.Fatalf("%s notice: %q", lang, f)
		}
		for _, x := range toolLoopAsks {
			t.Run(lang+"/"+x.name, func(t *testing.T) {
				v := &countingVendor{}
				gw := toolLoopOn(t, v)
				// the ask far back, past the turns the guard reads
				turn := func(i int) (string, string) {
					if i < 60 {
						return pollJob(i)
					}
					return codexRead(i)
				}
				body := strings.Replace(toolTurns(x.from, 60+toolLoopRepeats, turn, x.stream), "why does parse panic?", a.ask, 1)
				got := askAgent(t, gw+x.path, body)
				if !strings.Contains(got, a.says) || !strings.Contains(got, "sed -n 1,200p src/parser.rs") || v.asked.Load() != 0 {
					t.Fatalf("not told in %s (vendor asked %d):\n%s", lang, v.asked.Load(), got)
				}
			})
		}
	}
	if len(asks) != len(toolLoopNotices) {
		t.Fatalf("%d notices, %d tested", len(toolLoopNotices), len(asks))
	}
}
