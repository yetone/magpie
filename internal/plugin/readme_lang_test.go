package plugin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPickReadmeByLanguage(t *testing.T) {
	files := []string{"README.md", "README.zh-CN.md", "README_ja.md", "readme-de.md", "README.zh-TW.md", "index.js", "README.fr.md.bak"}
	for lang, want := range map[string]string{
		"zh-CN": "README.zh-CN.md", "zh": "README.zh-CN.md", "zh_CN": "README.zh-CN.md",
		"zh-TW": "README.zh-TW.md", "ja": "README_ja.md", "de": "readme-de.md",
		"en": "", "": "", "fr": "", "ko": "",
	} {
		if got := pickReadme(files, lang); got != want {
			t.Errorf("pickReadme(%q) = %q, want %q", lang, got, want)
		}
	}
	// the conventions a plugin may spell it in
	for _, n := range []string{"README.zh.md", "README_zh.md", "README-zh.md", "README_zh_CN.md", "README.zh-cn.md", "README.CN.md", "README.zh-Hans.md", "readme.zh.markdown"} {
		if got := pickReadme([]string{"README.md", n}, "zh-CN"); got != n {
			t.Errorf("%s not picked for zh-CN, got %q", n, got)
		}
	}
	// traditional readers take a simplified README over English
	if got := pickReadme([]string{"README.md", "README.zh.md"}, "zh-TW"); got != "README.zh.md" {
		t.Errorf("zh-TW got %q", got)
	}
}

// A plugin that ships a README in the UI's language shows it, installed or
// not; one without shows README.md.
func TestReadmeInUILanguage(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/npm/pk-zh"):
			_, _ = w.Write([]byte(`{"readme":"# English","dist-tags":{"latest":"1.2.3"}}`))
		case strings.HasPrefix(r.URL.Path, "/npm/pk-en"):
			_, _ = w.Write([]byte(`{"readme":"# English only","dist-tags":{"latest":"1.0.0"}}`))
		case r.URL.Path == "/data/pk-zh@1.2.3":
			_, _ = w.Write([]byte(`{"files":[{"name":"/README.md"},{"name":"/README.zh-CN.md"},{"name":"/docs/README.ja.md"}]}`))
		case r.URL.Path == "/data/pk-en@1.0.0":
			_, _ = w.Write([]byte(`{"files":[{"name":"/README.md"}]}`))
		case r.URL.Path == "/cdn/pk-zh@1.2.3/README.zh-CN.md":
			_, _ = w.Write([]byte("# 中文"))
		case r.URL.Path == "/api/repos/o/r/contents":
			_, _ = w.Write([]byte(`[{"name":"README.md","type":"file"},{"name":"README_zh.md","type":"file"}]`))
		case r.URL.Path == "/raw/o/r/HEAD/README_zh.md":
			_, _ = w.Write([]byte("# 仓库中文"))
		case r.URL.Path == "/raw/o/r/HEAD/README.md":
			_, _ = w.Write([]byte("# repo English"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a, b, c, d, e := npmRegistry, jsdelivrData, jsdelivrCDN, githubAPI, githubRaw
	npmRegistry, jsdelivrData, jsdelivrCDN, githubAPI, githubRaw = srv.URL+"/npm", srv.URL+"/data", srv.URL+"/cdn", srv.URL+"/api", srv.URL+"/raw"
	defer func() { npmRegistry, jsdelivrData, jsdelivrCDN, githubAPI, githubRaw = a, b, c, d, e }()
	ctx := context.Background()

	for _, c := range []struct{ name, lang, want string }{
		{"pk-zh", "zh-CN", "# 中文"},
		{"pk-zh", "en", "# English"},
		{"pk-zh", "", "# English"},
		{"pk-zh", "ja", "# English"}, // not in the package root
		{"pk-en", "zh-CN", "# English only"},
		{"github:o/r", "zh-CN", "# 仓库中文"},
		{"github:o/r", "en", "# repo English"},
	} {
		p, err := ReadmeIn(ctx, c.name, c.lang)
		if err != nil || p.Readme != c.want {
			t.Errorf("ReadmeIn(%q, %q) = %q, %v; want %q", c.name, c.lang, p.Readme, err, c.want)
		}
	}

	// installed: the folder's own file, no ask
	dir := Target("pk-zh")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README_zh.md"), []byte("# 本地"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	if p, err := ReadmeIn(ctx, "pk-zh", "zh"); err != nil || p.Readme != "# 本地" {
		t.Errorf("installed: %q, %v", p.Readme, err)
	}
}
