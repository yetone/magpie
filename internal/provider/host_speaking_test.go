package provider

import "testing"

// Host is the host of the first protocol the provider speaks, as it was
// when it read Speaks' list; it builds no list now: one for each look was
// 8 MB of what twenty polls of the Providers page allocated with real
// accounts (IsBedrock, IsKilo, IsCline and IsOpenCode each look).
func TestHostBuildsNoList(t *testing.T) {
	for _, p := range []Provider{
		{ID: "a", Chat: "https://api.example.com/v1"},
		{ID: "b", Anthropic: "https://api.anthropic.example/v1"},
		{ID: "c", Responses: "https://r.example.com/v1", Anthropic: "https://a.example.com"},
		{ID: "d", Gemini: "https://generativelanguage.example.com/v1beta"},
		{ID: "d2", Gemini: "https://api.wandapi.ai"}, // asked at its /v1beta (#6479)
		{ID: "d3", Gemini: " https://relay.example.com/gemini/models/ "},
		{ID: "e", Decide: "https://decide.example.com/v1"},
		{ID: "f"},
	} {
		want := ""
		for _, pr := range p.Speaks() {
			if u := p.Base(pr); u != "" {
				want = HostOf(u)
				break
			}
		}
		if want == "" && p.Decide != "" {
			want = HostOf(p.Decide)
		}
		if got := p.Host(); got != want {
			t.Errorf("%s: Host = %q, want %q", p.ID, got, want)
		}
		if n := testing.AllocsPerRun(20, func() { p.Host() }); n != 0 {
			t.Errorf("%s: Host allocated %.0f times a look", p.ID, n)
		}
	}
}

// Host takes a Gemini base's host from the saved URL rather than the one
// GeminiAsked builds (#6479); it is the same host for whatever was saved.
func TestHostOfGeminiIsAskedHost(t *testing.T) {
	for _, u := range []string{
		"https://generativelanguage.googleapis.com", "https://api.wandapi.ai/",
		"https://x.example/v1beta", " https://X.example/models/gemini:generateContent?alt=sse ",
		"api.wandapi.ai", "api.wandapi.ai/v1beta", "/models", "https:///models", "https://?x",
		"https://", "//", " ", "/x.example/v1", "https://x.example#f", "http://x.example:8080/",
	} {
		p := Provider{ID: "g", Gemini: u}
		want := ""
		if b := p.Base(Gemini); b != "" {
			want = HostOf(b)
		}
		if got := p.Host(); got != want {
			t.Errorf("Gemini %q: Host = %q, want %q (Base %q)", u, got, want, p.Base(Gemini))
		}
	}
}
