package provider

import "testing"

func TestWithScheme(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.1.20:3425":               "http://192.168.1.20:3425",
		"http://192.168.1.20:3425":        "http://192.168.1.20:3425",
		"HTTP://192.168.1.20:3425/":       "http://192.168.1.20:3425/",
		"Http://h:1/v1/":                  "http://h:1/v1/",
		"https://h":                       "https://h",
		"HTTPS://h/":                      "https://h/",
		"http://http://192.168.1.20:3425": "http://192.168.1.20:3425",
		"http://http://h:1/":              "http://h:1/",
		"https://https://h":               "https://h",
		"HTTP://HTTP://h":                 "http://h",
		"  http://h:1  ":                  "http://h:1",
		"localhost:3425/x?u=http://y":     "http://localhost:3425/x?u=http://y",
		"":                                "",
	} {
		if got := WithScheme(in, "http"); got != want {
			t.Errorf("WithScheme(%q) = %q, want %q", in, got, want)
		}
	}
}

// A Remote magpie's address typed with its scheme, in any case, with or
// without the slash, is one scheme in every URL; one saved twice by an older
// editor reads as one (eachg0 on X).
func TestRemoteMagpieSchemeOnce(t *testing.T) {
	for _, in := range []string{
		"http://192.168.1.20:3425", "HTTP://192.168.1.20:3425/", "Http://192.168.1.20:3425/v1",
		"http://http://192.168.1.20:3425", "http://http://192.168.1.20:3425/v1/", "192.168.1.20:3425",
	} {
		for _, field := range []string{"chat", "decide", "anthropic"} {
			p := Provider{ID: "r", Preset: RemoteMagpiePreset}
			switch field {
			case "chat":
				p.Chat, p.Responses = in, in
			case "decide":
				p.Decide = in
			case "anthropic":
				p.Anthropic = in
			}
			p = normalize(p)
			for name, got := range map[string]string{"chat": p.Chat, "responses": p.Responses, "decide": p.Decide, "anthropic": p.Anthropic} {
				want := "http://192.168.1.20:3425"
				if name != "anthropic" {
					want += "/v1"
				}
				if got != want {
					t.Errorf("%s=%q: %s = %q, want %q", field, in, name, got, want)
				}
			}
		}
	}
}

// A custom provider's System One address with its scheme already there.
func TestDecideSchemeOnce(t *testing.T) {
	p := normalize(Provider{ID: "c", Chat: "https://api.example.com/v1", Decide: "HTTPS://https://api.example.com/v1/"})
	if p.Decide != "https://api.example.com/v1" {
		t.Errorf("Decide = %q", p.Decide)
	}
}

func TestAddressSchemeOnce(t *testing.T) {
	for _, in := range []string{"HTTP://h:1/", "http://http://h:1", "http://h:1"} {
		u, err := parseAddress(in)
		if err != nil || u.String() != "http://h:1" {
			t.Errorf("parseAddress(%q) = %v, %v", in, u, err)
		}
	}
}
