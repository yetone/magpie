package agent

// Claude Code's status line, magpie's (steven_4422 on Discord: "show 5h
// and 7d account usage in claude code"). `magpie statusline` (#1997)
// prints the session's model and each account's windows as the gateway
// last read them, but only a user who found it in the reference and wrote
// it into settings.json had it. The Agents page offers it as a switch on
// Claude Code's row: on writes settings.json's statusLine to run this
// magpie's binary by its full path (a terminal's `magpie` may be another
// copy, or none: the app from the site puts none on PATH), off takes out
// only a status line that runs `magpie statusline`. A status line of the
// user's own (claude-hud, a script) is never written over: the switch
// isn't offered while one is there, and on is refused.
//
// Claude Code (2.1.316) watches settings.json and runs a changed
// statusLine command in its open sessions too, with the session's JSON on
// stdin, after each change of its messages; so nothing needs a restart.
// Only on this computer: a WSL distro's Claude Code would run a Windows
// binary through interop, which isn't tried.

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/update"
)

// claudeStatusExe is the magpie a status line written now runs; a var for
// tests.
var claudeStatusExe = func() string {
	if exe, err := update.Program(); err == nil && exe != "" {
		return exe
	}
	return "magpie"
}

// claudeStatusCommand is the statusLine command that runs exe's
// statusline: by a path Claude Code's shell takes as one word (it runs a
// status line through bash, Git Bash on Windows, where a path is written
// with forward slashes).
func claudeStatusCommand(exe, goos string) string {
	if goos == "windows" {
		exe = strings.ReplaceAll(exe, `\`, "/")
	}
	if strings.ContainsAny(exe, " \t'\"\\$`!&;|<>()*?[]#~{}") {
		exe = "'" + strings.ReplaceAll(exe, "'", `'\''`) + "'"
	}
	return exe + " statusline"
}

// claudeStatusOurs says whether a statusLine command runs magpie's
// statusline and nothing else: a magpie binary by any path or quoting, its
// first argument statusline (with or without the accounts to keep).
func claudeStatusOurs(cmd string) bool {
	prog, rest, ok := shellWord(strings.TrimLeft(cmd, " \t"))
	if !ok || prog == "" {
		return false
	}
	base := strings.ToLower(prog[strings.LastIndexAny(prog, `/\`)+1:])
	base = strings.TrimSuffix(base, ".exe")
	if base != "magpie" && !strings.HasPrefix(base, "magpie-") {
		return false
	}
	// every word plain: a status line that runs more (&& a script, | a
	// filter) is the user's, and off never takes it out
	for i := 0; ; i++ {
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			return i > 0
		}
		var w string
		if w, rest, ok = shellWord(rest); !ok || i == 0 && w != "statusline" {
			return false
		}
	}
}

// shellWord reads s's first word as a POSIX shell does ('…', "…" with its
// \ escapes, a bare \), up to unquoted space; ok is false for a quote
// left open or an operator in it.
func shellWord(s string) (word, rest string, ok bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case ' ', '\t', '\n':
			return b.String(), s[i:], true
		case ';', '&', '|', '<', '>', '(', ')', '`', '$':
			return "", "", false
		case '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return "", "", false
			}
			b.WriteString(s[i+1 : i+1+end])
			i += 1 + end
		case '"':
			for i++; ; i++ {
				if i >= len(s) {
					return "", "", false
				}
				if s[i] == '"' {
					break
				}
				if s[i] == '$' || s[i] == '`' {
					return "", "", false
				}
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`", s[i+1]) >= 0 {
					i++
				}
				b.WriteByte(s[i])
			}
		case '\\':
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), "", true
}

// claudeStatusField is the statusline switch of Claude Code's settings.json
// at path; settled is called after each write (no open session needs a
// restart for it).
func claudeStatusField(at place, path string, settled func()) Field {
	// mine: settings.json has a status line; ours: it runs magpie's
	cur := func() (has, ours bool) {
		if _, ok := edit.GetJSONRaw(path, "statusLine"); !ok {
			return false, false
		}
		cmd, _ := edit.GetJSON(path, "statusLine.command")
		return true, claudeStatusOurs(cmd)
	}
	here := at.base == nil
	return Field{
		Key: "statusline", Label: "status line", Quiet: true,
		Get: func() string {
			if _, ours := cur(); ours {
				return "on"
			}
			return ""
		},
		Set: func(v string) error {
			has, ours := cur()
			switch v {
			case "", "off":
				if !ours {
					return nil
				}
				settled()
				return edit.DelJSON(path, "statusLine")
			case "on":
				if ours {
					return nil
				}
				if has {
					return fmt.Errorf("%s has a status line of its own; magpie leaves it as it is (take it out there first, or add `magpie statusline` to the command it runs)", at.native(path))
				}
				if !here {
					return fmt.Errorf("magpie's status line is for Claude Code on this computer, not in WSL")
				}
				settled()
				return edit.SetJSON(path, edit.KV{Path: "statusLine", Value: map[string]any{
					"type": "command", "command": claudeStatusCommand(claudeStatusExe(), runtime.GOOS),
				}})
			}
			return fmt.Errorf("status line is on or off, not %q", v)
		},
		Options: func(map[string]string) []Option {
			// a distro's file isn't read: a stopped one would start (#3896)
			if !here {
				return nil
			}
			if has, ours := cur(); has && !ours {
				return nil
			}
			return []Option{{Value: "on", Note: "the 5h and 7d use of the accounts magpie serves, under Claude Code's prompt"}}
		},
	}
}
