package artifact

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"html"
)

// The viewer shell (docs/subsystems/artifacts.md, "Comments"): what a tab
// opening an artifact's page gets. It runs at magpie's own address, out
// of the page's sandbox, and frames the page, which is served as before;
// it draws the comments over it, keeps the shell's token (ShellToken) to
// the comments' API, and speaks to the page's runtime by postMessage.
// The page never reaches it but through those messages, which it reads
// only from its own frame.

//go:embed shell.js
var shellJS []byte

//go:embed shell.css
var shellCSS []byte

// ShellConfig is what the shell is given.
type ShellConfig struct {
	// API is the page's <folder>_api; Token, the shell's token
	API   string `json:"api"`
	Token string `json:"token"`
	ID    string `json:"id"`
	Title string `json:"title"`
	// Src is the page the shell frames: the address it was opened at;
	// the shell adds the fragment, which the server never sees
	Src string `json:"src"`
	// Version is the version shown; Pinned, that the address names it
	Version int  `json:"version"`
	Pinned  bool `json:"pinned,omitempty"`
	// Origin is magpie's origin, as the browser names it: the page's
	// runtime posts to it alone
	Origin string `json:"origin"`
	// Lang is magpie's language setting (system, en, zh, zh-TW, ja, de)
	Lang string `json:"lang,omitempty"`
	// App is the newest page's _api, where its app manifest, icons and
	// service worker are (app.go): a pinned version's shell takes its
	// icon, and only the newest's installs
	App string `json:"app,omitempty"`
}

// ShellPage is the shell's HTML, and the nonce its CSP must allow.
func ShellPage(c ShellConfig) (page []byte, nonce string) {
	b := make([]byte, 18)
	rand.Read(b)
	nonce = base64.RawURLEncoding.EncodeToString(b)
	cfg, _ := json.Marshal(c) // escapes < > &, so no </script> ends it early
	var p bytes.Buffer
	p.WriteString(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover"><meta name="referrer" content="no-referrer"><title>`)
	p.WriteString(html.EscapeString(c.Title))
	p.WriteString(`</title>`)
	if c.App != "" {
		p.WriteString(`<link rel="icon" href="` + html.EscapeString(c.App+"/"+AppIcon+"192.png") + `">`)
		if !c.Pinned {
			p.WriteString(`<link rel="manifest" href="` + html.EscapeString(c.App+"/"+AppManifest) + `">`)
		}
	}
	p.WriteString(`<style nonce="` + nonce + `">`)
	p.Write(shellCSS)
	p.WriteString(`</style></head><body><iframe id="page" title="` + html.EscapeString(c.Title) + `" allow="autoplay; clipboard-write; fullscreen"></iframe><div id="ui"></div><script nonce="` + nonce + `">`)
	p.Write(bytes.TrimRight(shellJS, "\n"))
	p.WriteString("(")
	p.Write(cfg)
	p.WriteString(");</script></body></html>")
	return p.Bytes(), nonce
}

// ShellCSP is the shell's Content-Security-Policy: its own script and
// style alone, the page in a frame from magpie's address, and nothing
// framing it.
func ShellCSP(nonce string) string {
	return "default-src 'none'; script-src 'nonce-" + nonce + "'; style-src 'nonce-" + nonce + "'; frame-src 'self'; connect-src 'self'; img-src 'self' data:; manifest-src 'self'; worker-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
}
