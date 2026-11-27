package agent

import (
	"testing"

	"github.com/yetone/magpie/internal/usage"
)

// Every agent's requests are told by what its own description says.
func TestAgentOf(t *testing.T) {
	cases := map[string]string{
		"claude-cli/2.1.0 (external, cli)":         "claude",
		"codex_cli_rs/0.40.0 (Mac OS 26.0; arm64)": "codex",
		"GeminiCLI/0.9.0 (darwin; arm64)":          "gemini",
		"opencode/1.2.3":                           "opencode",
		"mimocode/1.2.3":                           "mimocode",
		"deveco/0.2.3 ai-sdk/provider-utils/4.0":   "deveco", // 0.2.3 sends ai-sdk/provider-utils/4.0.23
		"curl/8.4.0":                               "curl",
		"oh-my-pi/1.0":                             "omp",
		"command-code/2.0":                         "commandcode",
		"deepseek-harness/0.3.1":                   "dsh",
		"dsh":                                      "dsh", // a record kept by an id already
		"pi":                                       "pi",
		"grok-pager/0.2.1":                         "grok",
		"kigi/0.1.22 (linux; x86_64)":              "kigi", // Kigi 0.1.22's own
		"ZCode/3.10.1":                             "zcode",
		"Alma/1.2.0":                               "alma",
		"HanaAgent/1.0":                            "hanako",
		"hanako":                                   "hanako", // its key
		"atomcode/5.2.1":                           "atomcode",
		"open-code-review/1.13.10":                 "open-code-review",
		"open-code-review/1.13.10 | claude":        "open-code-review",
		"cline ai-sdk/openai-compatible/3.0.37":    "cline",
		"Vibecoding Client/1.3.104 (darwin; arm64; go1.26.2)": "mothx",
		"ZooCode/3.90.0": "zoo-code",
		"minimax-code":   "minimax-code", // what magpie's entry sends
		"MiniMaxCode":    "minimax-code", // 0.6.8's own (#1605)
		"":               "other",
		// Kimi Code 2.x and its desktop app, as jiabubu's requests named
		// them; the old Python kimi-cli's KimiCLI/
		"kimi-code-cli/2.4.1":     "kimi",
		"kimi-code-desktop/1.2.1": "kimi-code-desktop",
		"KimiCLI/1.4.0":           "kimi",
		// Bub's settings.USER_AGENT; a name it begins isn't its
		"bub/0.6.2":    "bub",
		"bubble/1.0.0": "bubble",
	}
	for ua, want := range cases {
		if got := usage.AgentOf(ua); got != want {
			t.Errorf("%q: got %q want %q", ua, got, want)
		}
	}
}

// Every agent's own gateway key (gateway.TokenFor) names it in usage: the
// ones that send no User-Agent of their own (StepCode, jcode, Prime Agent,
// Snow…) are told by that key alone (#1605's check of the class).
func TestEveryAgentNamedByItsKey(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range Clients() {
		seen[a.ID] = true
		if got := usage.AgentOf(a.ID); got != a.ID {
			t.Errorf("%s's key names %q", a.ID, got)
		}
	}
	for _, id := range []string{"minimax-code", "stepcode", "jcode", "prime-agent", "snow", "deveco"} {
		if !seen[id] {
			t.Errorf("%s isn't among the agents usage knows", id)
		}
	}
}
