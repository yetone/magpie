package library

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
)

// Claude Code runs a plugin's hooks and MCP servers with CLAUDE_PLUGIN_ROOT
// and CLAUDE_PLUGIN_DATA in their environment, and resolves ${NAME} in their
// commands, arguments and env, and in skill and agent Markdown (its plugins
// reference, "Where each variable resolves"). A part the library carries
// runs as an ordinary hook, server, skill or subagent of each agent, where
// neither is set, so magpie resolves every reference to them it can find
// before writing the part out.
//
// A hook's command can also read the variable itself, in the language it
// runs: ponytail's SessionStart hook is
//
//	node -e "require(require('path').join(process.env.CLAUDE_PLUGIN_ROOT.split(...).join('/'), 'hooks/ponytail-activate.js'))"
//
// which, given as written, threw at every start of the agent
// (@AXmake110 on X). Such a read becomes a string literal of the folder.
// A hook has no env of its own in any agent's file, so the command is the
// only place to put it; an MCP server has one, and is given both
// variables there, as Claude Code gives them.

var pluginEnvNames = []string{"CLAUDE_PLUGIN_ROOT", "CLAUDE_PLUGIN_DATA"}

const plVar = `(CLAUDE_PLUGIN_(?:ROOT|DATA))`

// pluginEnvReads are a program's reads of the variable that a string
// literal stands in for: JavaScript (Node, Bun, Deno), Python and Ruby.
var pluginEnvReads = lazyre.New(strings.Join([]string{
	`(?:process|Bun)\.env\.` + plVar + `\b`,
	`(?:process|Bun)\.env\[\s*['"` + "`" + `]` + plVar + `['"` + "`" + `]\s*\]`,
	`Deno\.env\.get\(\s*['"]` + plVar + `['"]\s*\)`,
	`os\.environ\[\s*['"]` + plVar + `['"]\s*\]`,
	`os\.(?:environ\.get|getenv)\(\s*['"]` + plVar + `['"]\s*(?:,[^()]*)?\)`,
	`\bENV\[\s*['"]` + plVar + `['"]\s*\]`,
	`\bENV\.fetch\(\s*['"]` + plVar + `['"]\s*(?:,[^()]*)?\)`,
}, "|"))

// pluginEnvShell are the shells' own spellings other than sh's ${NAME} and
// $NAME (which pluginSub replaces): PowerShell's $env:NAME and
// ${env:NAME}, and cmd's %NAME%. The folder goes in as written, as it
// does for sh's.
var pluginEnvShell = lazyre.New(`(?i:\$env:)` + plVar + `\b|\$\{(?i:env:)` + plVar + `\}|%` + plVar + `%`)

// pluginSub replaces ${NAME} and $NAME with the plugin's folders.
func pluginSub(name string) func(string) string {
	root, data := pluginDir(name), pluginData(name)
	return strings.NewReplacer("${CLAUDE_PLUGIN_ROOT}", root, "$CLAUDE_PLUGIN_ROOT", root,
		"${CLAUDE_PLUGIN_DATA}", data, "$CLAUDE_PLUGIN_DATA", data).Replace
}

// pluginEnvRefs resolves a hook command's reads of the plugin's variables:
// a program's read becomes a string literal, quoted so it doesn't end the
// shell's quoting around it, and a shell's own spelling the folder.
// ${NAME} and $NAME are left to pluginSub.
func pluginEnvRefs(cmd, name string) string {
	if !strings.Contains(cmd, "CLAUDE_PLUGIN_") {
		return cmd
	}
	folder := func(v string) string {
		if v == "CLAUDE_PLUGIN_DATA" {
			return pluginData(name)
		}
		return pluginDir(name)
	}
	var b strings.Builder
	last := 0
	for _, m := range pluginEnvReads.FindAllStringSubmatchIndex(cmd, -1) {
		v := ""
		for g := 2; g+1 < len(m); g += 2 {
			if m[g] >= 0 {
				v = cmd[m[g]:m[g+1]]
				break
			}
		}
		b.WriteString(cmd[last:m[0]])
		// forward slashes: every runtime here takes them on Windows too,
		// and a backslash would be an escape in the literal
		p := filepath.ToSlash(folder(v))
		q := "'"
		if shellQuoteAt(cmd, m[0]) == '\'' {
			q = `"`
		}
		b.WriteString(q + strings.NewReplacer(`\`, `\\`, q, `\`+q).Replace(p) + q)
		last = m[1]
	}
	b.WriteString(cmd[last:])
	return pluginEnvShell.ReplaceAllStringFunc(b.String(), func(s string) string {
		if strings.Contains(s, "DATA") {
			return pluginData(name)
		}
		return pluginDir(name)
	})
}

// shellQuoteAt is the sh quote open at byte i of s: ', " or 0.
func shellQuoteAt(s string, i int) byte {
	var q byte
	for j := 0; j < i && j < len(s); j++ {
		c := s[j]
		switch {
		case q == '\'':
			if c == '\'' {
				q = 0
			}
		case c == '\\':
			j++
		case q == '"':
			if c == '"' {
				q = 0
			}
		case c == '\'' || c == '"':
			q = c
		}
	}
	return q
}

// pluginServerEnv gives a plugin's command server the variables Claude
// Code gives it, unless the plugin set them itself.
func pluginServerEnv(s *Server, name string) {
	if s.Transport != "stdio" && s.Transport != "" {
		return
	}
	for _, v := range pluginEnvNames {
		if _, ok := s.Env[v]; ok {
			continue
		}
		if s.Env == nil {
			s.Env = map[string]string{}
		}
		if v == "CLAUDE_PLUGIN_DATA" {
			s.Env[v] = pluginData(name)
		} else {
			s.Env[v] = pluginDir(name)
		}
	}
}

// pluginSkillText resolves ${NAME} in a skill's SKILL.md copied out of a
// plugin, as Claude Code does when it loads a plugin's skill.
func pluginSkillText(dir, name string) error {
	f := filepath.Join(dir, "SKILL.md")
	b, err := os.ReadFile(f)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	s := string(b)
	if !strings.Contains(s, "CLAUDE_PLUGIN_") {
		return nil
	}
	fi, err := os.Stat(f)
	if err != nil {
		return err
	}
	return os.WriteFile(f, []byte(pluginSub(name)(s)), fi.Mode().Perm())
}

// healPluginRefs resolves the plugin variables still in the parts of an
// installed plugin, written by a magpie that left them as they were: a
// hook's program read, a server that reads them, a subagent's prompt.
// Such a reference never works outside Claude Code's plugin runtime, so
// resolving it changes nothing that worked; the next sync writes the
// part out again.
func (l *Library) healPluginRefs() {
	if len(l.Plugins) == 0 {
		return
	}
	ours := func(b string) bool { return b != "" && l.plugin(b) != nil }
	for _, h := range l.Hooks {
		if h != nil && ours(h.Bundle) && strings.Contains(h.Command, "CLAUDE_PLUGIN_") {
			h.Command = pluginSub(h.Bundle)(pluginEnvRefs(h.Command, h.Bundle))
		}
	}
	for _, s := range l.MCP {
		if s == nil || !ours(s.Bundle) {
			continue
		}
		reads := strings.Contains(s.Command, "CLAUDE_PLUGIN_")
		for _, a := range s.Args {
			reads = reads || strings.Contains(a, "CLAUDE_PLUGIN_")
		}
		for _, v := range s.Env {
			reads = reads || strings.Contains(v, "CLAUDE_PLUGIN_")
		}
		if reads {
			pluginServerEnv(s, s.Bundle)
		}
	}
	for _, s := range l.Subagents {
		if s != nil && ours(s.Bundle) && strings.Contains(s.Prompt, "CLAUDE_PLUGIN_") {
			s.Prompt = pluginSub(s.Bundle)(s.Prompt)
		}
	}
}
