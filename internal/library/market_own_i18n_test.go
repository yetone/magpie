package library

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The market's text for magpie's own servers is the GUI's in every
// language (the page says a featured one's through t()). Magpie
// Artifacts' says what it writes into Claude Code's settings.json, and a
// reader of another language read it in English only (rosu_h on X);
// Memory's and Sessions' were English too.
func TestOwnServersSaidInEveryLanguage(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "gui", "assets", "i18n.js"))
	if err != nil {
		t.Fatal(err)
	}
	lang := regexp.MustCompile(`^  (\w+|"[\w-]+"): \{$`)
	entry := regexp.MustCompile(`^    ("(?:[^"\\]|\\.)*"): "`)
	tables := map[string]map[string]bool{}
	var cur map[string]bool
	for _, line := range strings.Split(string(b), "\n") {
		if m := lang.FindStringSubmatch(line); m != nil {
			cur = map[string]bool{}
			tables[strings.Trim(m[1], `"`)] = cur
		} else if m := entry.FindStringSubmatch(line); m != nil && cur != nil {
			k, err := strconv.Unquote(m[1])
			if err != nil {
				t.Fatalf("%s: %v", m[1], err)
			}
			cur[k] = true
		}
	}
	langs := []string{"zh", "zh-TW", "ja", "de"}
	for _, l := range langs {
		if len(tables[l]) == 0 {
			t.Fatalf("i18n.js has no %s table", l)
		}
	}
	n := 0
	for _, m := range featured {
		if m.Publisher != "Magpie" {
			continue
		}
		n++
		for _, l := range langs {
			if !tables[l][m.Description] {
				t.Errorf("%s's text isn't in %s:\n%s", m.Name, l, m.Description)
			}
		}
	}
	if n == 0 {
		t.Fatal("no server of magpie's own in the market")
	}
}
