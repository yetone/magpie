package provider

import "testing"

// A Gemini model at a relay of the user's with Anthropic's and Gemini's URLs
// (TJHHHH's Tokenflow on Discord: omp asked gemini-4 on Messages, and each
// turn went out translated to Gemini) is wired to agents on Gemini's API, as
// is one at a provider whose Base URL was given as the Gemini one. Another
// model there, a Gemini model a Chat URL serves, one set to an API of its
// own, and a provider with no Gemini URL are as before.
func TestAgentGeminiForAGeminiModelOffMessages(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	tokenflow := Provider{ID: "tokenflow", Key: "k", Anthropic: "https://relay.example", Gemini: "https://relay.example/v1beta"}
	asGemini := Provider{ID: "asgem", Key: "k", Chat: "https://relay.example/v1", Anthropic: "https://relay.example", Gemini: "https://relay.example/v1beta", BaseAPI: "gemini"}
	hybrid := Provider{ID: "hybrid", Key: "k", Chat: "https://relay.example/v1", Anthropic: "https://relay.example", Gemini: "https://relay.example/v1beta"}
	noGemini := Provider{ID: "anth", Key: "k", Anthropic: "https://relay.example"}
	for _, c := range []struct {
		p     Provider
		model string
		want  bool
	}{
		{tokenflow, "gemini-4-carbon", true},
		{tokenflow, "google/Gemini-3-Pro", true},
		{tokenflow, "claude-sonnet-4-5", false},
		{tokenflow, "glm-5", false},
		{asGemini, "gemini-3-flash", true},
		{asGemini, "gpt-5.5", false},
		{hybrid, "gemini-3-flash", false},
		{noGemini, "gemini-3-flash", false},
	} {
		if got := c.p.AgentGemini(c.model); got != c.want {
			t.Errorf("%s AgentGemini(%s) = %v, want %v", c.p.ID, c.model, got, c.want)
		}
	}
}
