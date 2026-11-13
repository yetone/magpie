package provider

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// testdata/ccswitch_links.txt holds CC Switch's own example links, copied
// as written from its repository (deplink.html and the site integration
// guide at 3c76d3b), one per line.
func ccSwitchLinks(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("testdata/ccswitch_links.txt")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func TestCCSwitchOwnExamples(t *testing.T) {
	type want struct {
		Name, Chat, Responses, Anthropic, Gemini, Key string
		Models                                        []string
	}
	got := map[string]want{}
	n := 0
	for _, l := range ccSwitchLinks(t) {
		p, err := ParseImport(l)
		if !strings.Contains(l, "resource=provider") {
			// prompts, MCP servers and skills are not providers
			if err == nil || !strings.Contains(err.Error(), "providers") {
				t.Errorf("%.60s: %v", l, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%.90s: %v", l, err)
			continue
		}
		n++
		if p.ID != "example-ai" && p.ID != "example-gemini" {
			t.Errorf("%.90s: id %q", l, p.ID)
		}
		app := l[strings.Index(l, "app=")+4:]
		app = app[:strings.IndexByte(app, '&')]
		got[app+"/"+map[bool]string{true: "config", false: "params"}[strings.Contains(l, "config=")]+"/"+p.Name+p.Key+strings.Join(p.Models, ",")] = want{p.Name, p.Chat, p.Responses, p.Anthropic, p.Gemini, p.Key, p.Models}
	}
	if n != 11 {
		t.Fatalf("%d provider links in the examples", n)
	}
	for _, c := range []struct {
		key string
		w   want
	}{
		// endpoint, apiKey: claude's address is Anthropic's
		{"claude/params/Example AIsk-test-key123", want{Name: "Example AI", Anthropic: "https://api.example.com", Key: "sk-test-key123"}},
		// the config's env, with its three tiers
		{"claude/config/Example AIsk-test-key123example-chat,example-fast,example-pro", want{Name: "Example AI", Anthropic: "https://api.example.com", Key: "sk-test-key123", Models: []string{"example-chat", "example-fast", "example-pro"}}},
		// the key in the URL, the address in the config
		{"claude/config/Example AIsk-test-key123", want{Name: "Example AI", Anthropic: "https://api.example.com", Key: "sk-test-key123"}},
		// codex: config.toml's provider table, wire_api responses
		{"codex/config/Example AIsk-test-key123example-coder", want{Name: "Example AI", Responses: "https://api.example.com/v1", Key: "sk-test-key123", Models: []string{"example-coder"}}},
		{"codex/params/Example AIsk-test-key123", want{Name: "Example AI", Responses: "https://api.example.com/v1", Key: "sk-test-key123"}},
		{"gemini/config/Example GeminiAIzaSy-test-key123", want{Name: "Example Gemini", Gemini: "https://api.example.com", Key: "AIzaSy-test-key123"}},
		// apiFormat names the API an app of any kind speaks
		{"opencode/params/Example AIsk-test-key123example-chat", want{Name: "Example AI", Anthropic: "https://api.example.com/v1", Key: "sk-test-key123", Models: []string{"example-chat"}}},
		{"pi/params/Example AIsk-test-key123example-chat", want{Name: "Example AI", Chat: "https://api.example.com/v1", Key: "sk-test-key123", Models: []string{"example-chat"}}},
	} {
		if g, ok := got[c.key]; !ok {
			t.Errorf("no example gave %q; have %v", c.key, got)
		} else if !reflect.DeepEqual(g, c.w) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.key, g, c.w)
		}
	}
}

func TestCCSwitchIconAndUsageDropped(t *testing.T) {
	for _, l := range ccSwitchLinks(t) {
		if !strings.Contains(l, "iconUrl=") {
			continue
		}
		p, err := ParseImport(l)
		if err != nil || p.IconURL == "" {
			t.Fatalf("iconUrl: %q, %v", p.IconURL, err)
		}
	}
	// a usage script (CC Switch's balance query) is code: never taken
	p, err := ParseImport("ccswitch://v1/import?resource=provider&app=claude&name=x&endpoint=https%3A%2F%2Fa.example&usageScript=KHsKfSk%3D&usageEnabled=true&usageBaseUrl=https%3A%2F%2Fevil.example")
	if err != nil || p.BalanceURL != "" {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestCCSwitchRefused(t *testing.T) {
	const base = "ccswitch://v1/import?resource=provider&app=claude&name=x&endpoint=https%3A%2F%2Fa.example"
	for name, c := range map[string]struct{ link, why string }{
		"other version":     {strings.Replace(base, "//v1/", "//v2/", 1), "v1"},
		"other path":        {strings.Replace(base, "/import?", "/export?", 1), "/import"},
		"no resource":       {strings.Replace(base, "resource=provider&", "", 1), "resource"},
		"unknown app":       {strings.Replace(base, "app=claude", "app=nope", 1), "app"},
		"no name":           {strings.Replace(base, "name=x&", "", 1), "name"},
		"plain http":        {strings.Replace(base, "https", "http", 1), "https"},
		"javascript":        {strings.Replace(base, "https%3A%2F%2Fa.example", "javascript%3Aalert(1)", 1), "base URL"},
		"file":              {strings.Replace(base, "https%3A%2F%2Fa.example", "file%3A%2F%2F%2Fetc%2Fpasswd", 1), "base URL"},
		"bad format":        {base + "&apiFormat=grpc", "apiFormat"},
		"remote config":     {base + "&configUrl=https%3A%2F%2Fa.example%2Fc.json", "configUrl"},
		"config not base64": {base + "&config=%25%25%25", "base64"},
		"config not json":   {base + "&config=bm90IGpzb24", "json"},
		"config huge":       {base + "&config=" + strings.Repeat("A", 300<<10), "large"},
		"claude no env":     {base + "&config=e30", "env"},
		"key with newline":  {base + "&apiKey=sk%0Aevil", "key"},
		"no address":        {"ccswitch://v1/import?resource=provider&app=claude&name=x", "base URL"},
		"codex env_key": {"ccswitch://v1/import?resource=provider&app=codex&name=x&config=" +
			b64(`{"config":"model_provider=\"p\"\n[model_providers.p]\nbase_url=\"https://a.example/v1\"\nenv_key=\"HOME_SECRET\""}`), "HOME_SECRET"},
		"name only a symbol": {"ccswitch://v1/import?resource=provider&app=claude&name=%2A%2A&endpoint=https%3A%2F%2Fa.example", "id"},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := ParseImport(c.link)
			if err == nil {
				t.Fatalf("accepted: %+v", p)
			}
			if !strings.Contains(err.Error(), c.why) {
				t.Fatalf("%q does not say %q", err, c.why)
			}
		})
	}
	if _, err := ParseImport("ccswitch://v1/import?resource=provider&app=claude&name=x&endpoint=https%3A%2F%2Fa.example&homepage=javascript%3Aalert(1)"); err != nil {
		t.Fatalf("a bad homepage turned the link away: %v", err)
	}
}

// a ccswitch link is as safe as a magpie one: the balance host rule and
// the id rules are the shared ones
func TestCCSwitchNamedAsSubscription(t *testing.T) {
	p, err := ParseImport("CCSWITCH://V1/import?resource=provider&app=claude&name=claude&endpoint=https%3A%2F%2Fa.example")
	if err != nil || p.ID == "claude" {
		t.Fatalf("%+v %v", p, err)
	}
}

func b64(s string) string {
	const a = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	var out []byte
	for i := 0; i < len(s); i += 3 {
		var v uint
		n := 0
		for j := 0; j < 3; j++ {
			v <<= 8
			if i+j < len(s) {
				v |= uint(s[i+j])
				n++
			}
		}
		for j := 0; j <= n; j++ {
			out = append(out, a[(v>>(18-6*uint(j)))&63])
		}
	}
	return string(out)
}
