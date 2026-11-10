package agent

// An agent a plugin adds that takes the gateway only from how it is
// started (agentplug.Launch: agy reads GEMINI_API_KEY and
// GOOGLE_GEMINI_BASE_URL from its environment and resolves a model only
// given with --model) is started on magpie as the built-in agy was: its
// row gives the command, and magpie puts a function named its command in
// each shell the user has while it is on one of magpie's models. On this
// machine the function runs `magpie <id> run` (cli/agy_cli.go), which
// starts the real one with the variables and the model while its file's
// model is magpie's, and as it is otherwise; PowerShell's asks `magpie
// <id> env` for them. A WSL distro has no magpie to ask, so its function
// reads the model from the file itself (or holds it, where the file can't
// be read so), with the variables written in.

import (
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/agentplug"
	"github.com/yetone/magpie/internal/update"
)

// launchSpec is how a plugin's agent is started: its id, which `magpie
// <id> run` names, its command, the flag that names the model ("" none),
// its variables, by name, the flags put before the user's arguments with
// their values (flag then value, sorted by flag: agy's --settings and
// magpie's settings file), and the arguments that start it as it is.
type launchSpec struct {
	id, bin, flag string
	env           map[string]string // templates (agentplug.Fill)
	args, plain   []string
}

// newLaunch is d's launch, its config's path, as the agent names it,
// in place of {config}.
func newLaunch(d agentplug.Desc, config string) *launchSpec {
	if d.Launch == nil {
		return nil
	}
	l := &launchSpec{id: d.ID, bin: d.Bin, flag: d.Launch.Model, env: d.Launch.Env, plain: slices.Clone(d.Launch.Plain)}
	for _, f := range sortedKeys(d.Launch.Args) {
		l.args = append(l.args, f, strings.ReplaceAll(d.Launch.Args[f], "{config}", config))
	}
	return l
}

// plainCases are the plain arguments as a shell's case matches them:
// each alone, and a flag with "=value".
func (l *launchSpec) plainCases() []string {
	var out []string
	for _, a := range l.plain {
		out = append(out, a)
		if strings.HasPrefix(a, "-") {
			out = append(out, a+"=*")
		}
	}
	return out
}

// vars are the variables filled for the gateway at gw and key, sorted by
// name, NAME=value.
func (l *launchSpec) vars(gw, key string) []string {
	out := make([]string, 0, len(l.env))
	for _, k := range sortedKeys(l.env) {
		out = append(out, k+"="+agentplug.Fill(l.env[k], gw, gw+"/v1", key))
	}
	return out
}

// names are the variables' names, sorted.
func (l *launchSpec) names() []string { return sortedKeys(l.env) }

// flags are the forms of the model's flag the agent takes: --model and
// -model for "--model", as agy does.
func (l *launchSpec) flags() []string {
	if l.flag == "" {
		return nil
	}
	out := []string{l.flag}
	if strings.HasPrefix(l.flag, "--") {
		out = append(out, l.flag[1:])
	}
	return out
}

// Given says a is the model's flag, in one of its forms, alone or with
// "=value".
func (l *launchSpec) given(a string) bool {
	a, _, _ = strings.Cut(a, "=")
	return slices.Contains(l.flags(), a)
}

func posixQuote(v string) string { return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'" }
func fishQuote(v string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, "'", `\'`).Replace(v) + "'"
}
func psQuote(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }

// command is what starts the agent on the gateway at gw with key and
// model, in PowerShell or a POSIX shell.
func (l *launchSpec) command(gw, key, model string, powershell bool) string {
	var b strings.Builder
	for _, kv := range l.vars(gw, key) {
		k, v, _ := strings.Cut(kv, "=")
		if powershell {
			b.WriteString(`$env:` + k + `="` + v + `"; `)
		} else {
			b.WriteString(k + "=" + v + " ")
		}
	}
	b.WriteString(l.bin)
	quote := posixQuote
	if powershell {
		quote = psQuote
	}
	for i := 0; i+1 < len(l.args); i += 2 {
		b.WriteString(" " + l.args[i] + " " + quote(l.args[i+1]))
	}
	if l.flag != "" {
		b.WriteString(" " + l.flag + " " + quote(model))
	}
	return b.String()
}

// posixHead opens the function in a POSIX shell's profile: "bin() {",
// which dash takes too, an alias of its name taken out first on a line of
// its own (agyPosixHead).
func (l *launchSpec) posixHead() string {
	return "unalias " + l.bin + " 2>/dev/null || true\n" + l.bin + "() {\n"
}

// function is the function in this machine's shell of kind, which runs
// prog (this magpie) to start the agent; the command on PATH when prog is
// gone.
func (l *launchSpec) function(kind, prog string) string {
	switch kind {
	case "fish":
		q := fishQuote(prog)
		return "function " + l.bin + "\n    if test -x " + q + "\n        " + q + " " + l.id + " run $argv\n    else\n        command " + l.bin + " $argv\n    end\nend"
	case "powershell":
		q := psQuote(prog)
		names := make([]string, 0, len(l.env))
		for _, n := range l.names() {
			names = append(names, psQuote(n))
		}
		set := "    $names = @(" + strings.Join(names, ", ") + ")\n" +
			"    $was = @{}\n" +
			"    foreach ($n in $names) { $was[$n] = [Environment]::GetEnvironmentVariable($n, 'Process'); [Environment]::SetEnvironmentVariable($n, $start[$n], 'Process') }\n"
		pre := ""
		for i := 0; i+1 < len(l.args); i += 2 {
			pre += " " + l.args[i] + " $start['" + strings.TrimLeft(l.args[i], "-") + "']"
		}
		run := "        & $real" + pre + " @args\n"
		if l.flag != "" {
			name := regexp.QuoteMeta(strings.TrimLeft(l.flag, "-"))
			run = "        if (@($args | Where-Object { \"$_\" -match '^--?" + name + "(=|$)' }).Count) { & $real" + pre + " @args } else { & $real" + pre + " " + l.flag + " $start.model @args }\n"
		}
		plain := ""
		if len(l.plain) > 0 {
			alts := make([]string, len(l.plain))
			for i, a := range l.plain {
				alts[i] = regexp.QuoteMeta(a)
			}
			plain = " -or @($args | Where-Object { \"$_\" -match '^(" + strings.Join(alts, "|") + ")(=|$)' }).Count"
		}
		return "function " + l.bin + " {\n" +
			"    $real = Get-Command " + l.bin + " -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1\n" +
			"    if (-not $real) { Write-Error '" + l.bin + " is not installed'; return }\n" +
			"    $start = @{}\n" +
			"    if (Test-Path -LiteralPath " + q + ") {\n" +
			"        foreach ($l in @(& " + q + " " + l.id + " env)) { $k, $v = \"$l\" -split '=', 2; if ($v) { $start[$k] = $v } }\n" +
			"    }\n" +
			"    if (-not $start.model" + plain + ") { & $real @args; return }\n" +
			set +
			"    try {\n" + run +
			"    } finally { foreach ($n in $names) { [Environment]::SetEnvironmentVariable($n, $was[$n], 'Process') } }\n" +
			"}"
	}
	q := posixQuote(prog)
	return l.posixHead() + "  if [ -x " + q + " ]; then " + q + " " + l.id + ` run "$@"; else command ` + l.bin + ` "$@"; fi` + "\n}"
}

// distroFunction is the function in a WSL distro's shell of kind: while
// read (a sed script over file, the config as the distro names it) prints
// a model, or model is held ("" for read), the command started with vars
// and the model after its flag (none when the user gives the flag); as it
// is otherwise.
func (l *launchSpec) distroFunction(kind string, vars []string, read, file, model string) string {
	var cases []string
	for _, f := range l.flags() {
		cases = append(cases, f, f+"=*")
	}
	if kind == "fish" {
		var b strings.Builder
		b.WriteString("function " + l.bin + "\n")
		if read != "" {
			b.WriteString("    set -l m (sed -n " + fishQuote(read) + " " + fishQuote(file) + " 2>/dev/null | head -n 1)\n")
		} else {
			b.WriteString("    set -l m " + fishQuote(model) + "\n")
		}
		b.WriteString("    if test -z \"$m\"\n        command " + l.bin + " $argv\n        return\n    end\n")
		if pc := l.plainCases(); len(pc) > 0 {
			qs := make([]string, len(pc))
			for i, c := range pc {
				qs[i] = fishQuote(c)
			}
			b.WriteString("    for a in $argv\n        switch $a\n            case " + strings.Join(qs, " ") + "\n                command " + l.bin + " $argv\n                return\n        end\n    end\n")
		}
		if len(cases) > 0 {
			qs := make([]string, len(cases))
			for i, c := range cases {
				qs[i] = fishQuote(c)
			}
			b.WriteString("    for a in $argv\n        switch $a\n            case " + strings.Join(qs, " ") + "\n                set m ''\n        end\n    end\n")
			b.WriteString("    if test -n \"$m\"\n        set argv " + l.flag + " $m $argv\n    end\n")
		}
		if len(l.args) > 0 {
			qs := make([]string, len(l.args))
			for i, a := range l.args {
				qs[i] = fishQuote(a)
			}
			b.WriteString("    set argv " + strings.Join(qs, " ") + " $argv\n")
		}
		b.WriteString("    env")
		for _, kv := range vars {
			k, v, _ := strings.Cut(kv, "=")
			b.WriteString(" " + k + "=" + fishQuote(v))
		}
		b.WriteString(" " + l.bin + " $argv\nend")
		return b.String()
	}
	var b strings.Builder
	b.WriteString(l.posixHead())
	b.WriteString("  local m a\n")
	if read != "" {
		b.WriteString("  m=$(sed -n " + posixQuote(read) + " " + posixQuote(file) + " 2>/dev/null | head -n 1)\n")
	} else {
		b.WriteString("  m=" + posixQuote(model) + "\n")
	}
	b.WriteString(`  if [ -z "$m" ]; then command ` + l.bin + ` "$@"; return; fi` + "\n")
	if pc := l.plainCases(); len(pc) > 0 {
		b.WriteString(`  for a in "$@"; do case "$a" in ` + strings.Join(pc, "|") + `) command ` + l.bin + ` "$@"; return;; esac; done` + "\n")
	}
	if len(cases) > 0 {
		b.WriteString(`  for a in "$@"; do case "$a" in ` + strings.Join(cases, "|") + `) m=;; esac; done` + "\n")
		b.WriteString(`  if [ -n "$m" ]; then set -- ` + l.flag + ` "$m" "$@"; fi` + "\n")
	}
	if len(l.args) > 0 {
		qs := make([]string, len(l.args))
		for i, a := range l.args {
			qs[i] = posixQuote(a)
		}
		b.WriteString("  set -- " + strings.Join(qs, " ") + ` "$@"` + "\n")
	}
	b.WriteString(" ")
	for _, kv := range vars {
		k, v, _ := strings.Cut(kv, "=")
		b.WriteString(" " + k + "=" + posixQuote(v))
	}
	b.WriteString(" command " + l.bin + ` "$@"` + "\n}")
	return b.String()
}

// sedBRE is s as a literal in a sed basic regular expression delimited
// by '|'.
func sedBRE(s string) string {
	return strings.NewReplacer(`\`, `\\`, `.`, `\.`, `*`, `\*`, `[`, `\[`, `]`, `\]`, `^`, `\^`, `$`, `\$`, `|`, `\|`).Replace(s)
}

// modelSed is the sed script that prints the model of a file of format
// at the top-level key while it starts with prefix (magpie's), for a
// distro's function; "" where a script can't say (a key below another,
// no prefix to tell magpie's by), and the function holds the model.
// magpie writes the key on a line of its own; one on a single line is
// found too, so long as nothing after it has the key's name.
func modelSed(format, key, prefix string) string {
	if prefix == "" || key == "" || strings.ContainsAny(key, `.\`) {
		return ""
	}
	k, p := sedBRE(key), sedBRE(prefix)
	switch format {
	case "json":
		return `s|^.*"` + k + `"[[:space:]]*:[[:space:]]*"\(` + p + `[^"]*\)".*$|\1|p`
	case "yaml":
		return `s|^` + k + `:[[:space:]]*["']\{0,1\}\(` + p + `[^"' #]*\).*$|\1|p`
	case "env":
		return `s|^[[:space:]]*\(export[[:space:]]\{1,\}\)\{0,1\}` + k + `=["']\{0,1\}\(` + p + `[^"' #]*\).*$|\2|p`
	}
	return ""
}

// launchShell gives the shells at the place the function of l, or takes
// it out of them: this machine's (as `magpie <id> run`), a distro's (vars
// and the model written in, distroProfiles). What fails leaves the setting
// as it is: the row's command still starts it.
type launchShell struct {
	l      *launchSpec
	at     place
	distro bool
	// fn is the function in a distro's shell of kind, as it is now
	fn func(kind string) string
}

func (s launchShell) set(on bool) {
	switch {
	case !s.distro:
		if here, _ := homes(); here != s.at.home {
			return
		}
		if on {
			update.ShellFunction(s.l.bin, s.l.function)
		} else {
			update.DropShellFunction(s.l.bin)
		}
	case on:
		update.ShellFunctionAt(agyDistroProfiles(s.at), s.l.bin, s.fn)
	default:
		update.DropShellFunctionAt(agyDistroProfiles(s.at), s.l.bin)
	}
}

// in are the profiles that have the function.
func (s launchShell) in() []string {
	if !s.distro {
		if here, _ := homes(); here != s.at.home {
			return nil
		}
		return update.ShellFunctionIn(s.l.bin)
	}
	return update.ShellFunctionInAt(agyDistroProfiles(s.at), s.l.bin)
}

// stale says a distro's function names what is no longer so (the
// gateway's address, which WSL's NAT changes on a restart): the profile
// it is in, "" for none.
func (s launchShell) stale() string {
	if !s.distro {
		return ""
	}
	for _, p := range s.in() {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, k := range []string{"posix", "fish"} {
			if strings.Contains(string(b), s.fn(k)) {
				goto next
			}
		}
		return p
	next:
	}
	return ""
}

// what an agent a plugin adds that is started on magpie says after a
// change (notice.go)
var (
	noticeLaunch      = newNotice("{agent} takes magpie's gateway and model only from how it is started — start it with {command}")
	noticeLaunchTyped = newNotice("Type {bin} in a new terminal: magpie added it to {profiles}, to start {agent} on magpie's gateway and model (in a terminal open now: {command}).")
)

// Starting is what starts an agent on magpie (Agent.Start): the command
// on PATH, the variables, the flags with their values put before the
// user's arguments (Args, flag then value: agy's --settings <file>), and
// the model after Flag (none when Flag is ""); Model "" while it isn't on
// magpie's, and it starts as it is. With one of Plain among the user's
// arguments (alone or with "=value") it starts as it is too.
type Starting struct {
	Bin   string
	Env   []string
	Args  []string
	Model string
	Flag  string
	Plain []string
}

// Plained says a is one of Plain, alone or with "=value".
func (s Starting) Plained(a string) bool {
	a, _, _ = strings.Cut(a, "=")
	return slices.Contains(s.Plain, a)
}

// Given says a is the model's flag in one of its forms (--model, -model),
// alone or with "=value".
func (s Starting) Given(a string) bool {
	if s.Flag == "" {
		return false
	}
	return (&launchSpec{flag: s.Flag}).given(a)
}

// Names are the variables' names.
func (s Starting) Names() []string {
	out := make([]string, 0, len(s.Env))
	for _, kv := range s.Env {
		k, _, _ := strings.Cut(kv, "=")
		out = append(out, k)
	}
	return out
}
