package agent

import (
	"strings"
	"testing"
)

// wztlink1013 on Discord (Windows, Codex 0.162.0): switching Codex's model
// on the Agents page failed with "config.toml: edited TOML is invalid: toml:
// key http_headers should be a table, not a value". The config held magpie's
// provider headers as their own table, which a rewrite of
// [model_providers.magpie] leaves in place, and the inline http_headers
// written beside it defined the key twice. The header is now set in that
// table, the user's other headers and comments kept.
func TestCodexMagpieHeadersAsOwnTable(t *testing.T) {
	for _, crlf := range []bool{false, true} {
		config := "model = \"gpt-5.5\"\n\n" +
			"[model_providers.magpie]\nname = \"magpie\"\nbase_url = \"http://127.0.0.1:1/v1\"\nwire_api = \"responses\"\n\n" +
			"[model_providers.magpie.http_headers]\n# my proxy\n\"x-org\" = \"acme\"\n\"x-openai-actor-authorization\" = \"magpie\"\n"
		if crlf {
			config = strings.ReplaceAll(config, "\n", "\r\n")
		}
		home, read := codexHome(t, `{"OPENAI_API_KEY":"sk-relay"}`, config)
		cx := codex(home)
		if err := cx.Fields[0].Set("fake/m1"); err != nil {
			t.Fatalf("crlf %v: %v", crlf, err)
		}
		cfg := strings.ReplaceAll(read(), "\r\n", "\n")
		if strings.Contains(cfg, "http_headers = {") {
			t.Fatalf("crlf %v: inline http_headers beside the table:\n%s", crlf, cfg)
		}
		for _, w := range []string{"[model_providers.magpie.http_headers]", "# my proxy", `"x-org" = "acme"`, `"x-openai-actor-authorization" = "magpie"`} {
			if !strings.Contains(cfg, w) {
				t.Fatalf("crlf %v: no %q:\n%s", crlf, w, cfg)
			}
		}
		if strings.Count(cfg, "x-openai-actor-authorization") != 1 {
			t.Fatalf("crlf %v: header written twice:\n%s", crlf, cfg)
		}
		if c := cx.Check(); c != "" {
			t.Fatalf("crlf %v: %s", crlf, c)
		}
	}
}
