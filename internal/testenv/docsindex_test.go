package testenv

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestDocsIndexStaleNamesEachChangedPage holds DocsIndexStale to its
// cases: a page edited, added or gone since the sums were written is
// named, the sums' own pages pass, and a tree with no docs has nothing to
// check.
func TestDocsIndexStaleNamesEachChangedPage(t *testing.T) {
	root := t.TempDir()
	if got, err := DocsIndexStale(root); err != nil || got != nil {
		t.Fatalf("no docs: %v, %v", got, err)
	}
	pub := filepath.Join(root, "site", "public")
	files := map[string]string{
		"llms.txt": "# magpie\n", "docs/a.html": "<article>a</article>", "docs/zh/a.html": "<article>甲</article>",
		"docs/ja/a.html": "<article>あ</article>", "docs/contributors.html": "not built from",
	}
	sums := ""
	for _, n := range []string{"docs/a.html", "docs/ja/a.html", "docs/zh/a.html", "llms.txt"} {
		h := sha256.Sum256([]byte(files[n]))
		sums += hex.EncodeToString(h[:]) + "  " + n + "\n"
	}
	for n, b := range files {
		p := filepath.Join(pub, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "site", "docs-index.sums"), []byte(sums), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := DocsIndexStale(root); err != nil || len(got) != 0 {
		t.Fatalf("current: %v, %v", got, err)
	}
	write := func(n, b string) {
		if err := os.WriteFile(filepath.Join(pub, filepath.FromSlash(n)), []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("docs/zh/a.html", "<article>乙</article>")
	write("docs/b.html", "<article>b</article>")
	write("docs/contributors.html", "changed, still not built from")
	if err := os.Remove(filepath.Join(pub, "docs", "ja", "a.html")); err != nil {
		t.Fatal(err)
	}
	got, err := DocsIndexStale(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"docs/b.html (new)", "docs/ja/a.html (gone)", "docs/zh/a.html"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestDocsIndexCurrent checks this tree's own docs against its sums, the
// check every package using testenv makes after its tests.
func TestDocsIndexCurrent(t *testing.T) {
	if err := docsIndexHere(); err != nil {
		t.Fatal(err)
	}
}
