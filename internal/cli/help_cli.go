package cli

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/agent"
)

// helpAsked is whether args ask for help rather than for the command to
// run: -h or --help anywhere, or help as the word after the command. It
// is looked at before run does anything, since a command that took
// --help for a value acted on it: `magpie snow --help` set Snow's model
// to "--help", which took magpie out of it (#1457), and `magpie serve
// --help` started a gateway.
func helpAsked(args []string) bool {
	// what magpie is started with by others (Claude Code's MCP helper, the
	// Agents page's dry run, a link, Windows' -Embedding) never carries
	// an exact -h, --help or help
	for _, a := range args {
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return len(args) > 1 && args[1] == "help"
}

// helpFor is what `magpie <command> --help` prints: the command's own
// usage where it has one, its lines of magpie's usage otherwise, an
// agent's forms and fields for an agent, and the whole usage for
// anything else.
func helpFor(args []string) string {
	cmd := args[0]
	switch cmd {
	case "-h", "--help", "help":
		return usage
	case "model":
		if len(args) > 1 && (args[1] == "spec" || args[1] == "specs") {
			return specUsage
		}
		return modelUsage
	case "group", "groups":
		if len(args) > 1 && (args[1] == "rule" || args[1] == "rules") {
			return ruleUsage
		}
		return groupUsage
	case "library", "lib":
		if len(args) > 1 && (args[1] == "project" || args[1] == "projects") {
			return "  " + libraryProjectUsage
		}
		return "  " + libraryUsage
	case "plugin", "plugins":
		return pluginUsage
	case "quota", "quotas":
		return quotaUsage
	case "statusline":
		return statuslineUsage
	case "mtls":
		return mtlsUsage
	case "https":
		return httpsUsage
	case "heap":
		return heapUsage
	case "cpu":
		return cpuUsage
	case "goroutines":
		return goroutinesUsage
	case "log", "logs":
		return logUsage
	case "sessions":
		return sessionsUsage
	case "search":
		return searchUsage
	case "tool", "tools":
		return toolUsage
	case "visible":
		return visibleUsage
	case "webdav", "dav":
		return webdavUsage
	case "s3":
		return s3Usage
	case "github":
		return githubUsage
	case "claude-code":
		return claudeCodeUsage
	case "settings":
		return settingsUsage
	case "list":
		cmd = "ls"
	case "account":
		cmd = "accounts"
	case "app", "gui":
		cmd = ""
	}
	if cmd != "" && !strings.HasPrefix(cmd, "-") {
		if lines := usageLines(cmd); lines != "" {
			return lines
		}
	}
	if a, err := agent.Find(cmd); err == nil && cmd != "" {
		return agentHelp(a)
	}
	return usage
}

// usageLines are the lines of magpie's usage for `magpie <cmd>`, with the
// lines that carry on from them.
func usageLines(cmd string) string {
	var b strings.Builder
	in := false
	for _, l := range strings.Split(usage, "\n") {
		if rest, ok := strings.CutPrefix(l, "  magpie "); ok {
			in = rest == cmd || strings.HasPrefix(rest, cmd+" ")
		} else if !strings.HasPrefix(l, "          ") {
			in = false
		}
		if in {
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}

// agentHelp is `magpie <agent> --help`: the forms that set it, and its
// fields.
func agentHelp(a *agent.Agent) string {
	var b strings.Builder
	id := a.ID
	fmt.Fprintf(&b, "%s\n\n", a.Name)
	fmt.Fprintf(&b, "  magpie %s                  show it\n", id)
	if len(a.Fields) == 0 {
		return b.String()
	}
	fmt.Fprintf(&b, "  magpie %s <model>          set its model   (magpie models %s lists them)\n", id, id)
	fmt.Fprintf(&b, "  magpie %s <field>          show one field\n", id)
	fmt.Fprintf(&b, "  magpie %s <field> <value>  set another field\n", id)
	fmt.Fprintf(&b, "  magpie %s default          take magpie out: back on what it had before\n", id)
	fmt.Fprintf(&b, "  magpie %s <field> default  that field back to its own default\n", id)
	if a.ID == "agy" {
		fmt.Fprintf(&b, "  magpie %s run [args…]      start agy, on magpie while its model is magpie's (what your shell's agy runs)\n", id)
		fmt.Fprintf(&b, "  magpie %s env              the variables and model it is started with\n", id)
	}
	if a.ID == "claude" {
		fmt.Fprintf(&b, "  magpie %s run [args…]      start Claude Code through magpie, a plain claude left as it is (what claude-magpie runs)\n", id)
		fmt.Fprintf(&b, "  magpie %s launcher [add|remove]   the claude-magpie command\n", id)
	}
	if a.ID == "claude-desktop" {
		fmt.Fprintf(&b, "  magpie %s launcher [add|remove]   Claude Magpie.app (macOS): Desktop on magpie in a folder of its own, your own Claude Desktop left signed in\n", id)
	}
	var keys []string
	for _, f := range a.Fields {
		keys = append(keys, f.Key)
	}
	fmt.Fprintf(&b, "\nfields: %s\n", strings.Join(keys, ", "))
	// Claude Code's tiers are named as its model's aliases are
	var both []string
	for _, f := range a.Fields {
		if slices.Contains(a.ModelAliases, f.Key) {
			both = append(both, f.Key)
		}
	}
	if n := len(both); n > 0 {
		names := both[0]
		if n > 1 {
			names = strings.Join(both[:n-1], ", ") + " and " + both[n-1]
		}
		fmt.Fprintf(&b, "  %s alone name the %s: magpie %s %s sets its %s to %s\n", names, a.Fields[0].Label, id, both[0], a.Fields[0].Label, both[0])
		// they are the agent's own models, not magpie's: setting one takes
		// magpie out (#2000, read as a query of the tier)
		fmt.Fprintf(&b, "  they are %s's own models, not magpie's: setting one takes magpie out; magpie %s alone shows each\n", a.Name, id)
	}
	return b.String()
}

// flagArg is an argument to `magpie <agent> …` that is a flag, which no
// agent form takes: it is said to be one, rather than set as a model or a
// field's value. A negative number is a value.
func flagArg(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			continue
		}
		if _, err := strconv.ParseFloat(a, 64); err == nil {
			continue
		}
		return a
	}
	return ""
}
