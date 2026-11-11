package plugin

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Where the files of a published npm package are listed and read, moved
// by tests. A package's registry entry carries only its README.md; a
// README in the reader's language travels in the package's files.
var (
	jsdelivrData = "https://data.jsdelivr.com/v1/packages/npm"
	jsdelivrCDN  = "https://cdn.jsdelivr.net/npm"
)

// readmeTags are the language tags a README named for the reader's
// language may carry, best first: "zh-CN" reads README.zh-CN.md,
// README_zh_cn.md, README.zh.md, README-CN.md and the like. The plain
// README.md is the caller's fallback, not listed. Traditional Chinese
// takes a simplified README over English.
func readmeTags(lang string) []string {
	l := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(lang), "_", "-"))
	base, _, _ := strings.Cut(l, "-")
	var tags []string
	switch base {
	case "", "en":
		return nil
	case "zh":
		if strings.Contains(l, "hant") || strings.Contains(l, "tw") || strings.Contains(l, "hk") || strings.Contains(l, "mo") {
			tags = []string{"zh-tw", "zh-hant", "zh-hk", "tw", "zh", "zh-cn", "zh-hans", "cn"}
		} else {
			tags = []string{"zh-cn", "zh-hans", "zh", "cn", "zh-sg"}
		}
	case "ja":
		tags = []string{l, "ja", "jp"}
	default:
		tags = []string{l, base}
	}
	seen := map[string]bool{}
	out := tags[:0]
	for _, t := range tags {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// readmeTag is the language tag a file name carries, lower-case with "-"
// for "_" ("README_zh_CN.md" is "zh-cn"), and whether it is a README at
// all; the plain README.md has the tag "".
func readmeTag(name string) (string, bool) {
	n := strings.ToLower(name)
	if !strings.HasPrefix(n, "readme") {
		return "", false
	}
	n = n[len("readme"):]
	for _, ext := range []string{".markdown", ".md", ".txt"} {
		if strings.HasSuffix(n, ext) {
			n = strings.TrimSuffix(n, ext)
			break
		}
	}
	n = strings.TrimLeft(n, "._-")
	n = strings.ReplaceAll(n, "_", "-")
	if strings.ContainsAny(n, "./ ") {
		return "", false
	}
	return n, true
}

// pickReadme is the file of names that is the README in lang, "" when
// there is none (the caller reads the plain one).
func pickReadme(names []string, lang string) string {
	for _, tag := range readmeTags(lang) {
		for _, n := range names {
			if t, ok := readmeTag(n); ok && t == tag {
				return n
			}
		}
	}
	return ""
}

// folderLangReadme is the README in lang a plugin's folder carries, if it
// has one.
func folderLangReadme(dir, lang string) (Page, bool) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return Page{}, false
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	n := pickReadme(names, lang)
	if n == "" {
		return Page{}, false
	}
	b, err := os.ReadFile(filepath.Join(dir, n))
	if err != nil {
		return Page{}, false
	}
	const most = 200 << 10
	if len(b) > most {
		b = b[:most]
	}
	p := Page{Readme: string(b)}
	if fi, err := os.Stat(filepath.Join(dir, n)); err == nil {
		p.Updated = fi.ModTime()
	}
	return p, true
}

// npmLangReadme is the README in lang of a package version npm has, read
// from its files through jsDelivr; false when it has none or jsDelivr
// doesn't answer, and the caller keeps npm's own README.
func npmLangReadme(ctx context.Context, name, version, lang string) (string, bool) {
	if len(readmeTags(lang)) == 0 || version == "" {
		return "", false
	}
	b, err := fetchJSON(ctx, jsdelivrData+"/"+npmPath(name)+"@"+url.PathEscape(version)+"?structure=flat", 4<<20)
	if err != nil {
		return "", false
	}
	var l struct {
		Files []struct {
			Name string `json:"name"`
		} `json:"files"`
	}
	if json.Unmarshal(b, &l) != nil {
		return "", false
	}
	var names []string
	for _, f := range l.Files {
		if strings.Count(f.Name, "/") == 1 { // the package's root only
			names = append(names, strings.TrimPrefix(f.Name, "/"))
		}
	}
	n := pickReadme(names, lang)
	if n == "" {
		return "", false
	}
	b, err = fetchJSON(ctx, jsdelivrCDN+"/"+npmPath(name)+"@"+url.PathEscape(version)+"/"+url.PathEscape(n), 200<<10)
	if err != nil || len(b) == 0 {
		return "", false
	}
	return string(b), true
}

// githubLangReadme is the README in lang at the root of a GitHub
// repository's default branch, as its contents are listed.
func githubLangReadme(ctx context.Context, repo, lang string) (string, bool) {
	if len(readmeTags(lang)) == 0 {
		return "", false
	}
	b, err := fetchJSON(ctx, githubAPI+"/repos/"+repo+"/contents", 2<<20)
	if err != nil {
		return "", false
	}
	var l []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if json.Unmarshal(b, &l) != nil {
		return "", false
	}
	var names []string
	for _, f := range l {
		if f.Type == "file" {
			names = append(names, f.Name)
		}
	}
	n := pickReadme(names, lang)
	if n == "" {
		return "", false
	}
	b, err = fetchJSON(ctx, githubRaw+"/"+repo+"/HEAD/"+url.PathEscape(n), 200<<10)
	if err != nil || len(b) == 0 {
		return "", false
	}
	return string(b), true
}
