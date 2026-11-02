package gateway

// The folder an agent works in, as it tells its model in the request — for
// a group's rule by folder (provider.Rule.Folders). Each agent says it its
// own way, every format here taken from the agent's own request:
//
//   - Claude Code (2.1.296): its environment, " - Primary working
//     directory: /x", in the system prompt or a system message; when the
//     folder changes, a <system-reminder> in a turn, "Primary working
//     directory: /y (was /x)". < and > in the path come as &lt; and &gt;.
//   - Codex (0.162): a user message's <environment_context> with
//     "<cwd>/x</cwd>", the path's &, < and > as entities.
//   - Pi (1.1): the system prompt's "<cwd>\n/x\n</cwd>" or its "Current
//     working directory: /x", backslashes as /.
//   - OpenCode (2.0): "<env>\n  Working directory: /x\n…</env>", in the
//     system prompt or, when it changes, a later message.
//
// The caller's own layer — its instructions and its system messages — is
// read whole, a --bare run's "CWD: /x" among the forms (#4940). A turn of
// the conversation is read only for the forms a caller states in one:
// Codex's and pi's <cwd>, OpenCode's <env>, Claude Code's
// <system-reminder>. A line a user message quotes — "Primary working
// directory: /x" alone on its line (WardLu's P3) — is no caller's word.
// Of those it says, the last one goes: an agent that moved says so after.
// An agent that says none of them has no folder: a rule by folder doesn't
// match it.

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

var (
	ccWorkDir = regexp.MustCompile(`(?m)^(?: - )?Primary working directory: (.+)$`)
	// the folder a <cwd> holds: Codex's on its line, Pi's on its own
	cwdTag = regexp.MustCompile(`<cwd>\s*([^<\n]+?)\s*</cwd>`)
	envDir = regexp.MustCompile(`(?m)^\s*Working directory: (.+)$`)
	// a --bare run's whole system prompt is "CWD: /x"
	bareCWD = regexp.MustCompile(`(?m)^\s*(?: - )?CWD: (.+)$`)
	// pi 1.1 states its folder as a line, where its <cwd> wraps it
	piDir  = regexp.MustCompile(`(?m)^\s*(?: - )?Current working directory: (.+)$`)
	wasDir = regexp.MustCompile(` \(was .*\)$`)
)

// dirForm is one form a caller states its folder in. wrap, when set, is
// the caller's own block the form is read inside: in a turn, a form is a
// caller's word only inside the block it states it in (#4940).
type dirForm struct {
	re   *regexp.Regexp
	wrap string
	was  bool // a moved-folder line, its " (was …)" dropped
}

// callerForms are the forms of the caller's own layer — its instructions
// and its system messages — where every form it states counts, wherever
// it sits there.
var callerForms = []dirForm{
	{re: ccWorkDir, was: true},
	{re: bareCWD},
	{re: piDir},
	{re: cwdTag},
	{re: envDir, wrap: "<env>"},
}

// turnForms are the forms a caller states in a turn: Codex's and pi's
// <cwd> (the <environment_context> is one of the user's), OpenCode's
// <env> a later message, and Claude Code's <system-reminder>, which says
// a folder it moved to.
var turnForms = []dirForm{
	{re: cwdTag},
	{re: envDir, wrap: "<env>"},
	{re: ccWorkDir, wrap: "<system-reminder>", was: true},
}

// workDir is the folder the request says its agent works in; "" when it
// doesn't say.
func workDir(req *Request) string {
	dir, _ := workPlace(req)
	return dir
}

// workPlace is the folder the request says its agent works in, and
// whether that folder is in a git repository as the text that says the
// folder says it: "yes", "no", or "" when it doesn't say.
func workPlace(req *Request) (dir, git string) {
	if req == nil {
		return "", ""
	}
	look := func(text string, forms []dirForm) {
		if d := workDirIn(text, "", forms); d != "" {
			dir, git = d, provider.GitSaid(text)
		}
	}
	look(req.System, callerForms)
	for _, m := range req.Messages {
		if m.Role == "assistant" {
			continue // the model's words, not the agent's
		}
		forms := turnForms
		if m.Role == "system" {
			forms = callerForms // the caller's own, said after its instructions
		}
		for _, p := range m.Parts {
			if p.Kind == Text {
				look(p.Text, forms)
			}
		}
	}
	return dir, git
}

// tellWorkPlace puts on a request to a plugin's provider the folder its
// agent works in (provider.WorkDirHeader, percent-encoded) and whether
// it is in a git repository (provider.WorkGitHeader), as far as body, in
// protocol to, says: a plugin that tells its vendor the user's
// environment, as ZCode's Start Plan does, says the agent's own and not
// magpie's (magpie-community/plugins#86). A folder not said leaves both
// off: unknown. Whether it is a git repository is the agent's word, else
// looked at on disk.
func tellWorkPlace(h http.Header, to provider.Protocol, body []byte) {
	h.Del(provider.WorkDirHeader)
	h.Del(provider.WorkGitHeader)
	f := factsOf(to, body)
	dir, git := f.dir, f.git
	if dir == "" {
		return
	}
	if git == "" {
		git = provider.GitOnDisk(dir)
	}
	h.Set(provider.WorkDirHeader, url.PathEscape(dir))
	if git != "" {
		h.Set(provider.WorkGitHeader, git)
	}
}

// workDirIn is the last folder text says in these forms, else dir.
func workDirIn(text, dir string, forms []dirForm) string {
	if !strings.Contains(text, "irectory: ") && !strings.Contains(text, "<cwd>") && !strings.Contains(text, "CWD:") {
		return dir
	}
	at := -1
	take := func(i int, v string, was bool) {
		if v = strings.TrimSpace(html.UnescapeString(v)); v != "" && i > at {
			if was {
				v = wasDir.ReplaceAllString(v, "")
			}
			at, dir = i, v
		}
	}
	for _, f := range forms {
		if f.wrap == "" {
			for _, m := range f.re.FindAllStringSubmatchIndex(text, -1) {
				take(m[0], text[m[2]:m[3]], f.was)
			}
			continue
		}
		// OpenCode's <env>, Claude Code's <system-reminder>: the caller's
		// own block the form is read inside
		end := "</" + f.wrap[1:]
		for rest, off := text, 0; ; {
			i := strings.Index(rest, f.wrap)
			if i < 0 {
				break
			}
			j := strings.Index(rest[i:], end)
			if j < 0 {
				break
			}
			block := rest[i : i+j]
			if m := f.re.FindStringSubmatchIndex(block); m != nil {
				take(off+i+m[0], block[m[2]:m[3]], f.was)
			}
			rest, off = rest[i+j:], off+i+j
		}
	}
	return dir
}
