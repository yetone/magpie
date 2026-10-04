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
		"curl/8.4.0":                               "curl",
		"oh-my-pi/1.0":                             "omp",
		"command-code/2.0":                         "commandcode",
		"deepseek-harness/0.3.1":                   "dsh",
		"dsh":                                      "dsh", // a record kept by an id already
		"pi":                                       "pi",
		"grok-pager/0.2.1":                         "grok",
		"ZCode/3.10.1":                             "zcode",
		"Alma/1.2.0":                               "alma",
		"HanaAgent/1.0":                            "hanako",
		"hanako":                                   "hanako", // its key
		"atomcode/5.2.1":                           "atomcode",
		"cline ai-sdk/openai-compatible/3.0.37":    "cline",
		"":                                         "other",
	}
	for ua, want := range cases {
		if got := usage.AgentOf(ua); got != want {
			t.Errorf("%q: got %q want %q", ua, got, want)
		}
	}
}
