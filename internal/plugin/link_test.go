package plugin

import (
	"strings"
	"testing"
)

func TestParseLink(t *testing.T) {
	good := map[string]string{
		"magpie://plugin?name=%40magpie-community%2Fopencode-zed-auth": "@magpie-community/opencode-zed-auth",
		"magpie://plugin?name=@magpie-community/opencode-zed-auth":     "@magpie-community/opencode-zed-auth",
		"MAGPIE://Plugin?name=opencode-foo":                            "opencode-foo",
		"magpie:plugin?name=opencode-foo":                              "opencode-foo",
		"magpie:///plugin?name=opencode-foo":                           "opencode-foo",
		"  magpie://plugin/?name=a.b_c~d-e  ":                          "a.b_c~d-e",
	}
	for link, want := range good {
		got, err := ParseLink(link)
		if err != nil || got != want {
			t.Errorf("ParseLink(%q) = %q, %v; want %q", link, got, err, want)
		}
		if !IsLink(link) {
			t.Errorf("IsLink(%q) = false", link)
		}
	}
	// a link names a package and nothing else: no version, path, git
	// repository, URL, upper case or a name npm wouldn't take
	bad := []string{
		"magpie://plugin",
		"magpie://plugin?name=",
		"magpie://plugin?name=opencode-foo@1.2.3",
		"magpie://plugin?name=opencode-foo%40latest",
		"magpie://plugin?name=..%2F..%2Fetc",
		"magpie://plugin?name=%2Ftmp%2Fevil",
		"magpie://plugin?name=.%2Fevil",
		"magpie://plugin?name=C%3A%5Cevil",
		"magpie://plugin?name=github%3Aowner%2Frepo",
		"magpie://plugin?name=owner%2Frepo",
		"magpie://plugin?name=https%3A%2F%2Fevil.example%2Fx.tgz",
		"magpie://plugin?name=file%3A%2F%2F%2Ftmp%2Fx",
		"magpie://plugin?name=OpenCode-Foo",
		"magpie://plugin?name=.hidden",
		"magpie://plugin?name=_under",
		"magpie://plugin?name=%40scope%2F",
		"magpie://plugin?name=%40%2Fx",
		"magpie://plugin?name=a%20b",
		"magpie://plugin?name=a%0Ab",
		"magpie://plugin?name=" + strings.Repeat("a", 215),
	}
	for _, link := range bad {
		if got, err := ParseLink(link); err == nil {
			t.Errorf("ParseLink(%q) = %q, want an error", link, got)
		}
		if !IsLink(link) {
			t.Errorf("IsLink(%q) = false: a bad plugin link is still one, for the dialog to say why", link)
		}
	}
	for _, link := range []string{"magpie://import?preset=deepseek", "https://plugin?name=x", "magpie://plugins?name=x", "ccswitch://plugin?name=x"} {
		if IsLink(link) {
			t.Errorf("IsLink(%q) = true", link)
		}
		if _, err := ParseLink(link); err == nil {
			t.Errorf("ParseLink(%q) took it", link)
		}
	}
	if !NPMName(strings.Repeat("a", 214)) || NPMName(strings.Repeat("a", 215)) {
		t.Error("npm's 214-character limit")
	}
}
