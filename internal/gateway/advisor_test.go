package gateway

import (
	"os"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// advisorTurn is a Claude Code turn with its advisor on, as Claude Code
// 2.1.300 sends it (read from its request to a fake ANTHROPIC_BASE_URL in
// a sandbox HOME, --advisor; the environment text cut short, the agent's
// tools but Read left out): the server tool, and a {role: "system"} message
// holding the advisor's instructions and a tool_addition naming it.
func advisorTurn(model, advisor string) string {
	b, err := os.ReadFile("testdata/claude-code-advisor-turn.json")
	if err != nil {
		panic(err)
	}
	return strings.NewReplacer(`"MODEL"`, `"`+model+`"`, `"ADVISOR"`, `"`+advisor+`"`).Replace(string(b))
}

// Claude Code's advisor names its model as Claude Code names its own: on
// magpie, by magpie's id, which the vendor doesn't know, so the request was
// turned away whole and the advisor never ran (JackChou on Discord). The
// advisor's model now goes as the upstream's own name for it when it is
// the same provider's Claude model; one the upstream can't run (another
// provider's, a vendor's model asked) is run by magpie (advisor_serve.go),
// and one magpie doesn't serve either is left out and the turn still
// answered.
func TestAdvisorModelAsTheUpstreamNamesIt(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	u, url := newBillingUpstream(t)
	for _, p := range []provider.Provider{
		{ID: "relay", Name: "Relay", Key: "k", Anthropic: url, Models: []string{"claude-opus-5-5", "claude-sonnet-5-5"}},
		{ID: "other", Name: "Other", Key: "k", Anthropic: url, Models: []string{"claude-opus-5-5"}},
		{ID: "ds", Name: "DeepSeek", Key: "k", Anthropic: url, Models: []string{"deepseek-v4.1-flash"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	advisors := func(b string) []string {
		var out []string
		for _, t := range gjson.Get(b, "tools").Array() {
			if strings.HasPrefix(t.Get("type").String(), "advisor_") {
				out = append(out, t.Get("model").String())
			}
		}
		return out
	}
	for _, c := range []struct {
		name, model, advisor, want string // want "" = not sent upstream
		served                     bool   // run by magpie
	}{
		{"same provider", "relay/claude-sonnet-5-5", "relay/claude-opus-5-5", "claude-opus-5-5", false},
		{"1M mark", "relay/claude-sonnet-5-5", "relay/claude-opus-5-5[1m]", "claude-opus-5-5", false},
		{"Claude's own id", "relay/claude-sonnet-5-5", "claude-opus-5-5", "claude-opus-5-5", false},
		{"another provider", "relay/claude-sonnet-5-5", "other/claude-opus-5-5", "", true},
		{"a vendor's model", "ds/deepseek-v4.1-flash", "relay/claude-opus-5-5", "", true},
		{"a model magpie doesn't serve", "ds/deepseek-v4.1-flash", "nobody/claude-opus-5-5", "", false},
	} {
		claudeCodeSends(t, advisorTurn(c.model, c.advisor))
		b := u.last(t)
		got := advisors(b)
		switch {
		case c.want == "" && len(got) != 0, c.want != "" && (len(got) != 1 || got[0] != c.want):
			t.Errorf("%s: advisor sent as %q, want %q: %s", c.name, got, c.want, b)
		}
		if !strings.Contains(b, `"name":"Read"`) {
			t.Errorf("%s: the agent's own tool lost: %s", c.name, b)
		}
		if c.served {
			// offered as the model's own tool, Claude Code's instructions
			// for it as written
			if !strings.Contains(b, `"name":"advisor"`) || !strings.Contains(b, "You have access to an `advisor` tool") || strings.Contains(b, advisorGone) {
				t.Errorf("%s: advisor not offered as magpie's: %s", c.name, b)
			}
			continue
		}
		advisorSaid(t, c.name, b, c.want != "")
	}
}

// advisorSaid checks what an upstream body b tells the model of the
// advisor: with it on (on), Claude Code's instructions and the
// tool_addition naming it as Claude Code sent them; with it left out,
// neither, and Claude Code's own word that it is gone. The rest of the
// message holding them stays.
func advisorSaid(t *testing.T, name, b string, on bool) {
	t.Helper()
	instructions := strings.Contains(b, "You have access to an `advisor` tool")
	ref := false
	gjson.Get(b, "messages").ForEach(func(_, m gjson.Result) bool {
		m.Get("content").ForEach(func(_, blk gjson.Result) bool {
			if blk.Get("type").String() == "tool_addition" && blk.Get("tool.name").String() == "advisor" {
				ref = true
			}
			return true
		})
		return true
	})
	switch {
	case on && (!instructions || !ref):
		t.Errorf("%s: advisor on, but its instructions (%v) or tool_addition (%v) lost: %s", name, instructions, ref, b)
	case !on && (instructions || ref):
		// the model called advisor() as the agent's tool: "No such tool
		// available: advisor" (JackChou on Discord)
		t.Errorf("%s: advisor left out, but its instructions (%v) or tool_addition (%v) still sent: %s", name, instructions, ref, b)
	case !on && !strings.Contains(b, advisorGone):
		t.Errorf("%s: advisor left out without saying so: %s", name, b)
	}
	if !strings.Contains(b, "Today's date is 2026-10-15.") {
		t.Errorf("%s: the system message's own text lost: %s", name, b)
	}
}

// A translation has no advisor to offer: the tool goes with every other
// server tool, and Claude Code's instructions to call it are said to be
// void, so the model doesn't call advisor() as one of the agent's tools
// (JackChou on Discord: "找不到对应工具").
func TestAdvisorLeftOutOnTranslation(t *testing.T) {
	for _, c := range []struct {
		name  string
		api   func(p *provider.Provider, url string)
		model string
	}{
		{"chat", func(p *provider.Provider, url string) { p.Chat = url + "/v1" }, "glm-5.1"},
		{"responses", func(p *provider.Provider, url string) { p.Responses = url + "/v1" }, "gpt-5.5"},
		{"gemini", func(p *provider.Provider, url string) { p.Gemini = url + "/v1beta" }, "gemini-3.5-pro"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			u, url := newBillingUpstream(t)
			p := provider.Provider{ID: "v", Name: "V", Key: "k", Models: []string{c.model}}
			c.api(&p, url)
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			claudeCodeSends(t, advisorTurn("v/"+c.model, "claude-opus-5-5"))
			b := u.last(t)
			if strings.Contains(b, "advisor_") {
				t.Errorf("the advisor server tool sent: %s", b)
			}
			advisorSaid(t, c.name, b, false)
		})
	}
}
