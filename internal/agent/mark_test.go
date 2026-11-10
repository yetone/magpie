package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// commentable says whether an agent reads path as TOML, YAML or dotenv,
// where a line can carry a comment. JSON can't, and gets none.
func commentable(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml", ".yaml", ".yml", ".env":
		return true
	}
	return filepath.Base(path) == ".env"
}

// rewrittenEachSync are the agents whose config every sync writes again
// unchanged, notes or none (#3870)
var rewrittenEachSync = map[string]bool{"kimi": true, "grok": true, "atomcode": true}

func modTimes(home string) map[string]time.Time {
	m := map[string]time.Time{}
	filepath.WalkDir(home, func(p string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			if fi, err := e.Info(); err == nil {
				rel, _ := filepath.Rel(home, p)
				m[rel] = fi.ModTime()
			}
		}
		return nil
	})
	return m
}

// Every agent magpie writes into a TOML, YAML or dotenv config gets a
// comment line over each line that points at the gateway, saying magpie
// wrote it and how to undo it: Jevik_AI on X deleted magpie's folders by hand and
// was left with a Codex config naming a gateway that no longer ran, and
// nothing in it naming magpie. Disconnect takes each note with its line.
func TestMarkedLinesEveryAgent(t *testing.T) {
	home, _ := codexHome(t, "", "")
	edit.Mark = lineNote
	t.Cleanup(func() { edit.Mark = nil })
	managed := claudeManaged
	claudeManaged = func() string { return filepath.Join(home, "managed-settings.json") }
	t.Cleanup(func() { claudeManaged = managed })
	startAlma(t)
	startShelley(t)
	host := strings.TrimPrefix(gateway.URL(), "http://")
	var marked []string
	for _, a := range All() {
		if a.Launch != nil || a.Native != nil || a.WSL != "" || len(a.Fields) == 0 {
			continue
		}
		t.Run(a.ID, func(t *testing.T) {
			if _, err := a.ConnectHow(); err != nil {
				t.Skip("can't be connected here:", err)
			}
			if !a.Wired() {
				t.Skip("not wired by Connect here")
			}
			// a note is read back as written: a sync doesn't write the
			// file again and again, as one that compared a line with the
			// one it would write did (dsh's routes)
			SyncCatalog()
			dshWiredOnce()
			synced, when := agentFiles(t, home), modTimes(home)
			SyncCatalog()
			dshWiredOnce()
			later := modTimes(home)
			for p, b := range agentFiles(t, home) {
				if synced[p] != b {
					t.Errorf("a second sync changed %s:\n--- first\n%s\n--- second\n%s", p, synced[p], b)
				} else if commentable(p) && !when[p].Equal(later[p]) && !rewrittenEachSync[a.ID] {
					t.Errorf("a second sync wrote %s again", p)
				}
			}
			for p, b := range agentFiles(t, home) {
				if !commentable(p) {
					continue
				}
				lines := strings.Split(b, "\n")
				for i, l := range lines {
					if strings.Contains(l, "# "+edit.MarkTag) {
						marked = append(marked, a.ID)
						// a line of its own: Grok App reads the rest of
						// a line as its value
						if !edit.IsNote(l) {
							t.Errorf("%s: a note beside a value: %q", p, l)
						}
					}
					above := i > 0 && edit.IsNote(lines[i-1])
					// a scalar on its own line; an inline table or a list
					// can't carry a note of its own
					if !strings.Contains(l, host) || edit.IsNote(l) || above || strings.HasPrefix(strings.TrimSpace(l), "- ") || strings.Contains(l, "= {") || strings.Contains(l, "= [") {
						continue
					}
					t.Errorf("%s: a line on the gateway without a note: %q", p, l)
				}
			}
			if err := a.Disconnect(); err != nil {
				t.Fatal(err)
			}
			for p, b := range agentFiles(t, home) {
				for _, l := range strings.Split(b, "\n") {
					if strings.Contains(l, "# "+edit.MarkTag) && !strings.Contains(l, "Codex threads started on magpie") {
						t.Errorf("%s: a note outlived Disconnect: %q", p, l)
					}
				}
			}
		})
	}
	if !slices.Contains(marked, "codex") {
		t.Fatal("Codex's config.toml got no note")
	}
}

// Codex's own keys that name magpie get a note on how to undo them by
// hand, not only the base URL.
func TestCodexMarkedLines(t *testing.T) {
	codexHome(t, "", "model = \"gpt-5.5\"\n")
	edit.Mark = lineNote
	t.Cleanup(func() { edit.Mark = nil })
	a, _ := Find("codex")
	if _, err := a.ConnectHow(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(a.Path)
	got := string(b)
	for _, want := range []string{
		"# magpie: Codex on magpie's local gateway; Disconnect in magpie or `magpie uninstall` undoes it, or delete this line and the model line\nmodel_provider = \"magpie\"\n",
		"# magpie: magpie's model list for Codex; " + undo + ", or delete this line\nmodel_catalog_json = ",
		"# magpie: for Codex threads started on magpie; unused while model_provider doesn't name magpie\nbase_url = \"" + gateway.URL() + "/v1\"\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(a.Path)
	if !strings.HasPrefix(string(b), "model = \"gpt-5.5\"\n") {
		t.Fatalf("after Disconnect:\n%s", b)
	}
}
