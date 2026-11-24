package plugin

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// A plugin link, magpie://plugin?name=<npm package>, opens the window's
// Plugins page on that package's sheet: what it is, who published it and
// its README, with Install for the reader to press. Nothing is installed
// by the link itself. A plugin's own page asks the same of the window by
// magpie.openPlugin(name) (pagebridge.js).

// IsLink is whether link is a magpie://plugin link, well formed or not.
func IsLink(link string) bool {
	u, err := url.Parse(strings.TrimSpace(link))
	return err == nil && strings.EqualFold(u.Scheme, "magpie") && linkAction(u) == "plugin"
}

func linkAction(u *url.URL) string {
	if u.Host != "" {
		return strings.ToLower(u.Host)
	}
	// magpie:plugin?… or magpie:///plugin?…
	return strings.ToLower(strings.Trim(u.Opaque+u.Path, "/"))
}

// ParseLink is the npm package a magpie://plugin link names. Only a
// package's name is taken: no version, path, git repository or URL, which
// a link from a web page has no business choosing for the reader.
func ParseLink(link string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || !strings.EqualFold(u.Scheme, "magpie") || linkAction(u) != "plugin" {
		return "", errors.New("not a magpie://plugin link")
	}
	name := u.Query().Get("name")
	if name == "" {
		return "", errors.New("magpie://plugin links name a package: magpie://plugin?name=<npm package>")
	}
	if !NPMName(name) {
		return "", fmt.Errorf("%q is not an npm package's name", name)
	}
	return name, nil
}

// NPMName is whether s is an npm package's name, scoped or not, as
// npm takes a new one: lower case, URL-safe, at most 214 characters.
func NPMName(s string) bool {
	return len(s) <= 214 && pkgName.MatchString(s)
}
