package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A project turns on a plugin Claude Code has installed for its folder
// alone (#7254, jinpwng on X): enabledPlugins in its
// .claude/settings.local.json. The user's own keys, comments and entries
// stay; only the key magpie wrote comes out, and a file it made goes.
func TestProjectClaudePluginEnabledPlugins(t *testing.T) {
	h, a, b := pluginProjects(t)
	write(t, filepath.Join(h, ".claude", "plugins", "installed_plugins.json"),
		`{"version":2,"plugins":{"fmt@acme":[{"scope":"user"}],"lint@acme":[{"scope":"user"}]}}`)
	write(t, filepath.Join(h, ".claude", "settings.json"), `{"enabledPlugins":{"lint@acme":true}}`)
	userWas := read(t, filepath.Join(h, ".claude", "settings.json"))
	f := filepath.Join(a, ".claude", "settings.local.json")

	if _, err := ProjectClaudePlugin(a, "nope", true); err == nil {
		t.Error("an id with no marketplace was taken")
	}
	v, _ := Read(nil)
	if len(v.ClaudeInstalled) != 2 || v.ClaudeInstalled[0].ID != "fmt@acme" || v.ClaudeInstalled[0].On || !v.ClaudeInstalled[1].On {
		t.Errorf("installed: %+v", v.ClaudeInstalled)
	}

	// a file magpie made, with nothing else in it, goes with the key
	res := ok(t)(ProjectClaudePlugin(a, "fmt@acme", true))
	if len(res.Problems) > 0 {
		t.Fatalf("problems: %+v", res.Problems)
	}
	if got := read(t, f); !strings.Contains(got, `"fmt@acme": true`) {
		t.Errorf("settings.local.json:\n%s", got)
	}
	if g := read(t, filepath.Join(a, ".git", "info", "exclude")); !strings.Contains(g, "/.claude/settings.local.json\n") {
		t.Errorf("info/exclude:\n%s", g)
	}
	if _, err := os.Stat(filepath.Join(b, ".claude")); err == nil {
		t.Error("another project got a file")
	}
	ok(t)(ProjectClaudePlugin(a, "fmt@acme", true)) // again: one key
	if n := strings.Count(read(t, f), "fmt@acme"); n != 1 {
		t.Errorf("%d keys:\n%s", n, read(t, f))
	}
	v, _ = Read(nil)
	if got := v.Projects[0].ClaudePlugins; len(got) != 1 || got[0] != "fmt@acme" {
		t.Errorf("view: %v", got)
	}
	ok(t)(ProjectClaudePlugin(a, "fmt@acme", false))
	gone(t, f)

	// the user's file, with a comment, a key of theirs and a plugin they
	// turned on themselves: only magpie's key is added and taken out
	own := "{\n  // mine\n  \"permissions\": {\"allow\": [\"Bash(ls)\"]},\n  \"enabledPlugins\": {\"lint@acme\": true}\n}\n"
	write(t, f, own)
	ok(t)(ProjectClaudePlugin(a, "fmt@acme", true))
	ok(t)(ProjectClaudePlugin(a, "lint@acme", true)) // theirs already: not written, not taken out
	got := read(t, f)
	if !strings.Contains(got, "// mine") || !strings.Contains(got, "Bash(ls)") || !strings.Contains(got, "fmt@acme") {
		t.Errorf("settings.local.json:\n%s", got)
	}
	ok(t)(ProjectClaudePlugin(a, "fmt@acme", false))
	ok(t)(ProjectClaudePlugin(a, "lint@acme", false))
	got = read(t, f)
	if strings.Contains(got, "fmt@acme") || !strings.Contains(got, `"lint@acme": true`) || !strings.Contains(got, "// mine") {
		t.Errorf("after taking out:\n%s", got)
	}

	// a key the user changed since is theirs: it stays when the project lets go
	ok(t)(ProjectClaudePlugin(a, "fmt@acme", true))
	write(t, f, strings.Replace(read(t, f), `"fmt@acme": true`, `"fmt@acme": false`, 1))
	ok(t)(ProjectClaudePlugin(a, "fmt@acme", false))
	if got := read(t, f); !strings.Contains(got, `"fmt@acme": false`) {
		t.Errorf("the user's value was taken:\n%s", got)
	}
	if read(t, filepath.Join(h, ".claude", "settings.json")) != userWas {
		t.Error("Claude Code's own settings.json was written")
	}

	// taking the project away takes magpie's key with it
	ok(t)(ProjectClaudePlugin(a, "lint@acme", true))
	ok(t)(RemoveProject(a, false))
	if got := read(t, f); strings.Count(got, "lint@acme") != 1 || strings.Contains(got, "// mine") == false {
		t.Errorf("after the project went:\n%s", got)
	}
}

// The file the project's skill offs and hooks share goes only once none of
// them is left in it.
func TestProjectClaudePluginSharesTheFile(t *testing.T) {
	_, a, _ := pluginProjects(t)
	f := filepath.Join(a, ".claude", "settings.local.json")
	ok(t)(ProjectPlugin(a, "acme-kit", []string{"claude"})) // hooks into the file
	ok(t)(ProjectClaudePlugin(a, "fmt@acme", true))
	ok(t)(ProjectPlugin(a, "acme-kit", nil))
	if !strings.Contains(read(t, f), "fmt@acme") {
		t.Errorf("the key went with the hooks:\n%s", read(t, f))
	}
	ok(t)(ProjectClaudePlugin(a, "fmt@acme", false))
	gone(t, f)
}
