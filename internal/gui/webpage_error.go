package gui

import (
	"html"
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
)

var (
	webPageStart = lazyre.New(`(?i)<(!doctype|html|head|body)\b`)
	webPageTitle = lazyre.New(`(?is)<title[^>]*>(.*?)</title>`)
)

// webPageSaid is a proxy's own web page inside an error text of magpie's:
// a request magpie made to a remote magpie or a vendor came back as
// Cloudflare's 524 page, and the Gateway page showed "<!DOCTYPE html>
// <!--[if lt IE 7]>…" (aiandcloud on X). It returns the text before the
// page and the page's title, as one short line, and false for a text with
// no page in it.
func webPageSaid(msg string) (string, bool) {
	at := webPageStart.FindStringIndex(msg)
	if at == nil {
		return "", false
	}
	before := strings.TrimRight(strings.TrimSpace(msg[:at[0]]), ":：—- ")
	title := ""
	if m := webPageTitle.FindStringSubmatch(msg[at[0]:]); m != nil {
		title = strings.Join(strings.Fields(html.UnescapeString(m[1])), " ")
	}
	if len([]rune(before)) > 160 {
		before = string([]rune(before)[:160])
	}
	switch {
	case before != "" && title != "":
		return before + ": " + title, true
	case before != "":
		return before, true
	case title != "":
		return title, true
	}
	return "a web page", true
}
