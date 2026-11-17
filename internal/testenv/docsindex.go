package testenv

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DocsIndexStale compares the docs pages under root's site/public with
// site/docs-index.sums, which site/docs-index.mjs writes beside
// public/llms-full.txt and public/search/*.json, and lists each page
// changed, added or gone since. Those two files are built from the pages
// and committed; a Go change that edited a docs page and ran only Go
// tests left them stale on main five times from 11-13 to 11-17
// (459d668b3, 256910f17, 9dcec6751, dbbb2f26b, 00b2c11a6), each found by
// CI's node tests after the push. A tree with no site/public/docs lists
// nothing.
func DocsIndexStale(root string) ([]string, error) {
	pub := filepath.Join(root, "site", "public")
	if _, err := os.Stat(filepath.Join(pub, "docs")); err != nil {
		return nil, nil
	}
	f, err := os.Open(filepath.Join(root, "site", "docs-index.sums"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	want := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		sum, name, ok := strings.Cut(sc.Text(), "  ")
		if ok {
			want[name] = sum
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	names := []string{"llms.txt"}
	for _, dir := range []string{"docs", "docs/zh", "docs/ja"} {
		es, err := os.ReadDir(filepath.Join(pub, filepath.FromSlash(dir)))
		if err != nil {
			return nil, err
		}
		for _, e := range es {
			if n := e.Name(); !e.IsDir() && strings.HasSuffix(n, ".html") && n != "contributors.html" {
				names = append(names, dir+"/"+n)
			}
		}
	}
	var stale []string
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(pub, filepath.FromSlash(n)))
		if err != nil {
			return nil, err
		}
		h := sha256.Sum256(b)
		switch sum, ok := want[n]; {
		case !ok:
			stale = append(stale, n+" (new)")
		case sum != hex.EncodeToString(h[:]):
			stale = append(stale, n)
		}
		delete(want, n)
	}
	for n := range want {
		stale = append(stale, n+" (gone)")
	}
	sort.Strings(stale)
	return stale, nil
}

// docsIndexHere is DocsIndexStale over the module the tested package is
// in, found from its folder (go test runs a package's tests there), so it
// fails the run of the package whose change edited a page.
func docsIndexHere() error {
	dir, err := os.Getwd()
	if err != nil {
		return nil
	}
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
			if !strings.HasPrefix(string(b), "module github.com/yetone/magpie\n") {
				return nil
			}
			break
		}
		up := filepath.Dir(dir)
		if up == dir {
			return nil
		}
		dir = up
	}
	stale, err := DocsIndexStale(dir)
	if err != nil {
		return fmt.Errorf("site/docs-index.sums: %v", err)
	}
	if len(stale) == 0 {
		return nil
	}
	if len(stale) > 10 {
		stale = append(stale[:10], fmt.Sprintf("and %d more", len(stale)-10))
	}
	return fmt.Errorf("docs pages changed since site/public/llms-full.txt and search/*.json were built; run `node search-index.mjs` in site/ and commit what it writes:\n\t%s", strings.Join(stale, "\n\t"))
}
