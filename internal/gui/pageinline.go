package gui

import (
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/yetone/magpie/internal/plugin"
)

// A plugin's page is framed with sandbox="allow-scripts", so its origin is
// opaque and every file it loads is a cross-site request that carries no
// cookie of SameSite=Lax (Cloudflare Access's CF_Authorization is one).
// Behind a proxy that signs readers in by cookie, the page's HTML came
// (the frame's own navigation is same-site) but its stylesheet and
// scripts were redirected to the sign-in page, and the page said it
// "didn't answer" (xuanwo on X, Day card).
//
// A page served to a browser therefore comes as one document: its own
// classic scripts and stylesheets are written into the HTML, the scripts
// allowed by their hashes in the page's policy. Loads the page makes
// later (a script it adds) ask the window for the file (pagebridge.js's
// asset call), which carries the cookie.

var (
	inlineScript = regexp.MustCompile(`(?is)<script\b([^>]*?)\bsrc\s*=\s*(?:"([^"]*)"|'([^']*)')([^>]*)>\s*</script\s*>`)
	inlineLink   = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	attrRel      = regexp.MustCompile(`(?is)\brel\s*=\s*["']?\s*stylesheet\b`)
	attrHref     = regexp.MustCompile(`(?is)\bhref\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	attrType     = regexp.MustCompile(`(?is)\btype\s*=\s*["']?\s*([^"'\s>]*)`)
	attrDefer    = regexp.MustCompile(`(?is)\bdefer\b`)
	bodyClose    = regexp.MustCompile(`(?i)</body\s*>`)
)

// maxInlinePage bounds a page written as one document.
const maxInlinePage = 8 << 20

// localRef is a reference to a file in the page's folder: relative, no
// scheme, query or fragment.
func localRef(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "#") || strings.ContainsAny(ref, "?#\\") {
		return "", false
	}
	if u, err := url.Parse(ref); err != nil || u.Scheme != "" || u.Host != "" {
		return "", false
	}
	return ref, true
}

// inlinePage writes a page's own classic scripts and stylesheets into its
// HTML. It answers the document and the hashes of the scripts it wrote;
// what it can't write in (a module, a stylesheet naming url()s, a file
// not found) stays a reference. The bridge is marked inlined so it knows
// to fetch later loads through the window.
func inlinePage(html []byte, dir string) (out []byte, hashes []string) {
	var deferred []string
	size := len(html)
	hash := func(body string) string {
		sum := sha256.Sum256([]byte(body))
		h := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		hashes = append(hashes, h)
		return h
	}
	read := func(ref string) (string, bool) {
		if ref == pluginBridgePath {
			return "globalThis.__magpieInlined=1;\n" + string(pageBridgeJS), true
		}
		file, ok := plugin.FileIn(dir, ref)
		if !ok {
			return "", false
		}
		b, err := os.ReadFile(file)
		if err != nil || size+len(b) > maxInlinePage {
			return "", false
		}
		size += len(b)
		return string(b), true
	}
	res := inlineScript.ReplaceAllStringFunc(string(html), func(tag string) string {
		m := inlineScript.FindStringSubmatch(tag)
		attrs := m[1] + " " + m[4]
		if t := attrType.FindStringSubmatch(attrs); t != nil && t[1] != "" && !strings.EqualFold(t[1], "text/javascript") && !strings.EqualFold(t[1], "application/javascript") {
			return tag
		}
		ref, ok := localRef(m[2] + m[3])
		if !ok {
			return tag
		}
		body, ok := read(ref)
		if !ok {
			return tag
		}
		body = strings.ReplaceAll(body, "</script", `<\/script`)
		hash(body)
		s := "<script>" + body + "\n</script>"
		if attrDefer.MatchString(attrs) {
			deferred = append(deferred, s)
			return ""
		}
		return s
	})
	res = inlineLink.ReplaceAllStringFunc(res, func(tag string) string {
		if !attrRel.MatchString(tag) {
			return tag
		}
		m := attrHref.FindStringSubmatch(tag)
		if m == nil {
			return tag
		}
		ref, ok := localRef(m[1] + m[2])
		if !ok {
			return tag
		}
		body, ok := read(ref)
		if !ok || strings.Contains(strings.ToLower(body), "url(") || strings.Contains(strings.ToLower(body), "@import") {
			return tag
		}
		return "<style>" + strings.ReplaceAll(body, "</style", `<\/style`) + "</style>"
	})
	if len(deferred) > 0 {
		tail := strings.Join(deferred, "\n")
		if loc := bodyClose.FindAllStringIndex(res, -1); len(loc) > 0 {
			i := loc[len(loc)-1][0]
			res = res[:i] + tail + "\n" + res[i:]
		} else {
			res += "\n" + tail
		}
	}
	return []byte(res), hashes
}
