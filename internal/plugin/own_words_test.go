package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// A repository's own package.json says what the package is called and
// what it is (magpie.name, magpie.summary), read with the rest of it: its
// card under Discover → Unofficial shows the package's own words rather
// than the repository's name and GitHub's description (MYCF711, #3608).
// npm's copy, the one the card installs, says them when it says any.
func TestReadTaggedOwnWords(t *testing.T) {
	read := func(pkg, npm string) Tagged {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/package.json"):
				w.Write([]byte(pkg))
			case strings.HasPrefix(r.URL.Path, "/npm/"):
				if npm == "" {
					http.Error(w, `"Not Found"`, 404)
					return
				}
				w.Write([]byte(npm))
			default:
				w.Write([]byte("module.exports = {}"))
			}
		}))
		defer srv.Close()
		raw, reg := githubRaw, npmRegistry
		githubRaw, npmRegistry = srv.URL+"/raw", srv.URL+"/npm"
		defer func() { githubRaw, npmRegistry = raw, reg }()
		got, keep, known, err := readTagged(context.Background(), Tagged{Spec: "github:someone/magpie-officeace", Repo: "someone/magpie-officeace"}, "main")
		if err != nil || !keep || !known {
			t.Fatalf("read %s: %v kept %v known %v", pkg, err, keep, known)
		}
		return got
	}
	both := map[string]string{"en": "Unofficial: OfficeAce", "zh": "非官方：OfficeAce"}

	got := read(`{"name":"magpie-officeace","version":"0.3.0","main":"index.js","magpie":{"name":"OfficeAce","summary":{"en":"Unofficial: OfficeAce","zh":"非官方：OfficeAce"}}}`, "")
	if got.Name != "OfficeAce" || !reflect.DeepEqual(got.Summary, both) {
		t.Errorf("repository's words: name %q summary %v", got.Name, got.Summary)
	}

	// published from this repository: npm's copy is installed, and its
	// words are the card's
	got = read(`{"name":"magpie-officeace","version":"0.3.0","main":"index.js","magpie":{"name":"OfficeAce dev","summary":{"en":"dev"}}}`,
		`{"version":"0.2.0","main":"index.js","repository":"github:someone/magpie-officeace","magpie":{"name":"OfficeAce","summary":{"en":"Unofficial: OfficeAce","zh":"非官方：OfficeAce"}}}`)
	if got.Spec != "magpie-officeace" || got.Name != "OfficeAce" || !reflect.DeepEqual(got.Summary, both) {
		t.Errorf("npm's words: spec %q name %q summary %v", got.Spec, got.Name, got.Summary)
	}
	// npm's copy saying nothing of itself leaves the repository's
	got = read(`{"name":"magpie-officeace","version":"0.3.0","main":"index.js","magpie":{"name":"OfficeAce","summary":{"en":"Unofficial: OfficeAce","zh":"非官方：OfficeAce"}}}`,
		`{"version":"0.2.0","main":"index.js","repository":"github:someone/magpie-officeace"}`)
	if got.Spec != "magpie-officeace" || got.Name != "OfficeAce" || !reflect.DeepEqual(got.Summary, both) {
		t.Errorf("npm silent: spec %q name %q summary %v", got.Spec, got.Name, got.Summary)
	}

	// a package.json that says nothing of itself keeps the card it had
	got = read(`{"name":"magpie-officeace","version":"1.0.0","main":"index.js"}`, "")
	if got.Name != "" || got.Summary != nil {
		t.Errorf("saying nothing: name %q summary %v", got.Name, got.Summary)
	}
	// a summary that isn't a map of strings is none, and the package is
	// still listed: never a failed read of the whole package.json
	got = read(`{"name":"magpie-officeace","version":"1.0.0","main":"index.js","magpie":{"name":"OfficeAce","summary":"one sentence"}}`, "")
	if got.Name != "OfficeAce" || got.Summary != nil {
		t.Errorf("summary a string: name %q summary %v", got.Name, got.Summary)
	}
}

// magpie.summary is a sentence a language on one line, as written, and
// nothing that could make it read as other text.
func TestOwnWordsBounds(t *testing.T) {
	sum := func(v any) map[string]string {
		b, _ := json.Marshal(v)
		return ownSummary(b)
	}
	if got := sum(map[string]string{"en": "  OfficeAce for Word \n", "zh": "非官方"}); !reflect.DeepEqual(got, map[string]string{"en": "OfficeAce for Word", "zh": "非官方"}) {
		t.Errorf("trimmed: %v", got)
	}
	for name, said := range map[string]any{
		"two lines":     map[string]string{"en": "one\ntwo"},
		"tab":           map[string]string{"en": "a\tb"},
		"bidi override": map[string]string{"en": "Qoder‮"},
		"zero width":    map[string]string{"en": "zero​width"},
		"too long":      map[string]string{"en": strings.Repeat("a", 301)},
		"empty":         map[string]string{"en": "  "},
		"long language": map[string]string{"thirteen-char": "x"},
		"no language":   map[string]string{" ": "x"},
		"not a string":  map[string]any{"en": 3},
		"not an object": "x",
		"a list":        []string{"x"},
	} {
		if got := sum(said); got != nil {
			t.Errorf("%s: %v, want none", name, got)
		}
	}
	if got := ownSummary(json.RawMessage(`{"en":"bad \xff utf-8"}`)); got != nil {
		t.Errorf("bad utf-8: %v", got)
	}
	if got := sum(map[string]string{"en": strings.Repeat("a", 300), "zh": "x\ny"}); len(got) != 1 || len(got["en"]) != 300 {
		t.Errorf("one good language of two: %v", got)
	}
	if got := ownSummary(nil); got != nil {
		t.Errorf("none: %v", got)
	}
}
