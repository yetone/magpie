package edit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// An Inline value given to SetTOMLTable goes into the spelling the file
// already has for that key. Codex's config.toml held magpie's provider
// headers as their own table (wztlink1013 on Discord, Windows), and the
// inline http_headers written beside it defined the key twice: "edited TOML
// is invalid: toml: key http_headers should be a table, not a value".
func TestSetTOMLTableInlineMergesUsersSpelling(t *testing.T) {
	actor := Inline{{Path: "x-openai-actor-authorization", Value: "magpie"}}
	cases := []struct {
		name, in string
		want     []string // lines the result has
		gone     []string // text it no longer has
	}{{
		name: "own table",
		in: "model = \"m\"\n\n[model_providers.magpie]\nname = \"old\"\n\n" +
			"[model_providers.magpie.http_headers]\n# mine\n\"x-org\" = \"acme\" # keep\n\"x-openai-actor-authorization\" = \"old\"\n",
		want: []string{"# mine", `"x-org" = "acme" # keep`, `"x-openai-actor-authorization" = "magpie"`, `name = "magpie"`},
		gone: []string{"http_headers = {", `"old"`},
	}, {
		name: "own table, header not there yet",
		in:   "[model_providers.magpie]\nname = \"magpie\"\n\n[model_providers.magpie.http_headers]\nx-org = \"acme\"\n",
		want: []string{`x-org = "acme"`, `"x-openai-actor-authorization" = "magpie"`},
		gone: []string{"http_headers = {"},
	}, {
		name: "own table only",
		in:   "model = \"m\"\n\n[model_providers.magpie.http_headers]\nx-org = \"acme\"\n",
		want: []string{"[model_providers.magpie]", `x-org = "acme"`, `"x-openai-actor-authorization" = "magpie"`},
		gone: []string{"http_headers = {"},
	}, {
		name: "dotted keys",
		in:   "[model_providers.magpie]\nname = \"old\"\nhttp_headers.\"x-org\" = \"acme\" # keep\nhttp_headers.\"x-openai-actor-authorization\" = \"old\"\n",
		want: []string{`http_headers."x-org" = "acme" # keep`, `http_headers."x-openai-actor-authorization" = "magpie"`},
		gone: []string{"http_headers = {", `"old"`},
	}, {
		name: "dotted keys, header not there yet",
		in:   "[model_providers.magpie]\nhttp_headers.x-org = \"acme\"\n",
		want: []string{`http_headers.x-org = "acme"`, `http_headers."x-openai-actor-authorization" = "magpie"`},
		gone: []string{"http_headers = {"},
	}, {
		name: "inline",
		in:   "[model_providers.magpie]\nname = \"old\"\nhttp_headers = { \"x-openai-actor-authorization\" = \"old\" }\n",
		want: []string{`http_headers = { "x-openai-actor-authorization" = "magpie" }`},
		gone: []string{`"old"`},
	}, {
		name: "absent",
		in:   "model = \"m\"\n",
		want: []string{`http_headers = { "x-openai-actor-authorization" = "magpie" }`},
	}}
	for _, c := range cases {
		for _, crlf := range []bool{false, true} {
			in := c.in
			if crlf {
				in = strings.ReplaceAll(in, "\n", "\r\n")
			}
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(in), 0o644); err != nil {
				t.Fatal(err)
			}
			err := SetTOMLTable(path, "model_providers.magpie",
				KV{Path: "name", Value: "magpie"},
				KV{Path: "http_headers", Value: actor})
			if err != nil {
				t.Fatalf("%s (crlf %v): %v", c.name, crlf, err)
			}
			b, _ := os.ReadFile(path)
			got := string(b)
			if crlf && strings.Count(got, "\n") != strings.Count(got, "\r\n") {
				t.Errorf("%s: mixed line endings:\n%q", c.name, got)
			}
			got = strings.ReplaceAll(got, "\r\n", "\n")
			lines := map[string]bool{}
			for _, l := range strings.Split(got, "\n") {
				lines[l] = true
			}
			for _, w := range c.want {
				if !lines[w] {
					t.Errorf("%s (crlf %v): no line %q:\n%s", c.name, crlf, w, got)
				}
			}
			for _, g := range c.gone {
				if strings.Contains(got, g) {
					t.Errorf("%s (crlf %v): still has %q:\n%s", c.name, crlf, g, got)
				}
			}
			var cfg struct {
				Providers map[string]struct {
					Headers map[string]string `toml:"http_headers"`
				} `toml:"model_providers"`
			}
			if err := toml.Unmarshal(b, &cfg); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if h := cfg.Providers["magpie"].Headers["x-openai-actor-authorization"]; h != "magpie" {
				t.Errorf("%s (crlf %v): header reads %q:\n%s", c.name, crlf, h, got)
			}
			if strings.Contains(c.in, "acme") && cfg.Providers["magpie"].Headers["x-org"] != "acme" {
				t.Errorf("%s (crlf %v): the user's x-org header is lost:\n%s", c.name, crlf, got)
			}
		}
	}
}
