package plugin

import (
	"context"
	"net/url"
	"os"
	"strings"
	"time"
)

// A Discover page's README, from the copy the community repo's `tagged`
// workflow keeps (readmes/<key>.md on its dist branch, passed on by
// usemagpie.ai/api/plugins/readme) before npm or GitHub: npm puts a human
// check in front of some networks and answers a page that isn't the
// package's, so the README never came (River_WLan on X). Where the copy
// isn't there, or doesn't answer in readmeCacheWait, npm and GitHub are
// asked as they were.

// readmeBases is where a README's copy is asked, in order; moved by tests.
// A %s is the package's key (readmeKey), url-escaped as the site wants it.
var readmeBases = []string{
	"https://usemagpie.ai/api/plugins/readme?name=%s",
	"https://raw.githubusercontent.com/magpie-community/plugins/dist/readmes/%s.md",
}

// readmeCacheWait is how long each address may take: the copy is a small
// file, and a slow one is as good as none beside npm's own 12 seconds.
var readmeCacheWait = 5 * time.Second

// readmeKey is what a package is filed under in the copy: its npm name
// ("@scope/name" is the folder @scope's name.md) or "github/owner/repo" for
// one installed from a repository. "" when the spec is neither.
func readmeKey(spec string) string {
	if repo := githubRepo(spec); repo != "" && strings.HasPrefix(spec, "github:") {
		return "github/" + repo
	}
	if pkgName.MatchString(spec) {
		return spec
	}
	return ""
}

// cachedReadme is the README the workflow kept for spec, false when it has
// none (or isn't asked: MAGPIE_PLUGIN_CATALOG=off keeps to the vendors).
func cachedReadme(ctx context.Context, spec string) (string, bool) {
	key := readmeKey(spec)
	if key == "" || os.Getenv("MAGPIE_PLUGIN_CATALOG") == "off" {
		return "", false
	}
	for _, base := range readmeBases {
		arg := key
		if strings.Contains(base, "?") {
			arg = url.QueryEscape(key)
		}
		c, cancel := context.WithTimeout(ctx, readmeCacheWait)
		b, err := fetchJSON(c, strings.Replace(base, "%s", arg, 1), 256<<10)
		cancel()
		if ctx.Err() != nil {
			return "", false
		}
		s := strings.TrimSpace(string(b))
		if err != nil || s == "" || looksLikeHTML(s) {
			continue
		}
		return string(b), true
	}
	return "", false
}

// looksLikeHTML is a page standing in for the file: a captive portal's or a
// human check's, never a README the workflow wrote.
func looksLikeHTML(s string) bool {
	l := strings.ToLower(s[:min(len(s), 256)])
	return strings.HasPrefix(l, "<!doctype") || strings.HasPrefix(l, "<html")
}
