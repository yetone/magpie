package library

// How a registry entry's package is started. The registry says a package's
// runtime (npx, uvx, docker), the runtime's own arguments
// (runtimeArguments) and the program's (packageArguments). Publishers
// don't all read the schema the same way: Serena puts its program's
// start-mcp-server among uvx's arguments, Docling names its executable
// there behind --from, Visualtorch puts --from among the program's. And a
// package's executable can be named otherwise than the package
// (alfanous3-mcp runs alfanous-mcp), which uvx doesn't guess
// (fwxkogaia on X: Serena installed from the market exited 2).
//
// So the arguments are read as a line the runtime will parse: the
// runtime's own options before the package, the rest after it, and the
// executable checked against the ones the package has, where the market's
// crawl knows them (Bins).

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
)

type registryArg struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Value       string `json:"value"`
	Default     string `json:"default"`
	ValueHint   string `json:"valueHint"`
	Description string `json:"description"`
	IsRequired  bool   `json:"isRequired"`
}

type registryPackage struct {
	RegistryType string                `json:"registryType"`
	Identifier   string                `json:"identifier"`
	RuntimeHint  string                `json:"runtimeHint"`
	Transport    struct{ Type string } `json:"transport"`
	Env          []registryVar         `json:"environmentVariables"`
	RuntimeArgs  []registryArg         `json:"runtimeArguments"`
	Arguments    []registryArg         `json:"packageArguments"`
	// Bins are the executables the package's latest version has, as
	// the market's crawl found them (npm's bin, a wheel's
	// console_scripts); nil where it isn't known, as for anything the
	// registry is searched for.
	Bins []string `json:"bins"`
}

// tokens is an argument as it's written on the line: nothing for one with
// no value (or a placeholder for one), need true when that one is required.
func (a registryArg) tokens() (out []string, need bool) {
	v := a.Value
	if v == "" {
		v = a.Default
	}
	if placeholderRe.MatchString(v) {
		v = ""
	}
	switch {
	case v != "" && a.Type == "named":
		return []string{a.Name, v}, false
	case v != "":
		return []string{v}, false
	}
	return nil, a.IsRequired
}

// uvx's and npx's own options, with the ones that take a value.
var (
	uvOpts = map[string]bool{"--from": true, "--with": true, "-w": true, "--with-editable": true,
		"--with-requirements": true, "-p": true, "--python": true, "--prerelease": true, "--index": true,
		"--index-url": true, "-i": true, "--extra-index-url": true, "--default-index": true,
		"--resolution": true, "--constraints": true, "-c": true, "--overrides": true,
		"--isolated": false, "--refresh": false, "--offline": false, "-q": false, "--quiet": false,
		"-n": false, "--no-cache": false, "--no-config": false}
	npxOpts = map[string]bool{"-p": true, "--package": true, "-y": false, "--yes": false}
)

// runtimeOption says a runtime argument is one of the runtime's own
// options, not the program's.
func runtimeOption(opts map[string]bool, a registryArg, toks []string) bool {
	if len(toks) == 0 {
		return false
	}
	if a.Type == "named" {
		_, ok := opts[a.Name]
		return ok
	}
	_, ok := opts[toks[0]]
	return ok
}

// launchLine is the command and arguments that start package p, and the
// inputs it leaves to the user (arguments the program needs that the
// registry gives no value for). ok is false for a package that can't be
// started as written: a required named argument with no value, or a
// package the crawl found has no executable to run.
func launchLine(p *registryPackage) (cmd string, args []string, inputs []Input, ok bool) {
	var rt, opts, prog []string
	for _, a := range p.RuntimeArgs {
		t, need := a.tokens()
		if need {
			return "", nil, nil, false
		}
		switch p.RegistryType {
		case "pypi":
			if runtimeOption(uvOpts, a, t) {
				opts = append(opts, t...)
			} else {
				prog = append(prog, t...)
			}
		case "npm":
			if runtimeOption(npxOpts, a, t) {
				opts = append(opts, t...)
			} else {
				prog = append(prog, t...)
			}
		default: // docker run's options, all of them before the image
			// only the ones that keep the container a container
			// (dockerOptSafe): the line is the publisher's, and the
			// user, shown "docker", never sees it
			if len(t) > 0 && !dockerOptSafe(a, t) {
				if a.IsRequired {
					return "", nil, nil, false
				}
				continue
			}
			opts = append(opts, t...)
		}
		rt = append(rt, t...)
	}
	var pa []string
	for i, a := range p.Arguments {
		t, need := a.tokens()
		switch {
		case t != nil:
			pa = append(pa, t...)
		case need && a.Type == "named":
			return "", nil, nil, false // a named argument magpie can't fill
		case need:
			label := a.ValueHint
			if label == "" {
				label = fmt.Sprintf("Argument %d", i+1)
			}
			inputs = append(inputs, Input{Key: fmt.Sprintf("arg%d", i), Where: "arg", Label: label, Description: a.Description, Required: true})
		}
	}
	switch p.RegistryType {
	case "npm":
		cmd, args, ok = npxLine(p, rt, opts, prog, pa)
	case "pypi":
		cmd, args, ok = uvxLine(p, rt, opts, prog, pa)
	case "oci":
		cmd, args, ok = "docker", []string{"run", "-i", "--rm"}, true
		for _, e := range p.Env {
			if e.IsRequired || e.IsSecret {
				args = append(args, "-e", e.Name)
			}
		}
		for i := 0; i < len(opts); i++ {
			if o := opts[i]; o == "-i" || o == "--rm" || o == "--interactive" {
				continue
			}
			args = append(args, opts[i])
		}
		args = append(append(args, p.Identifier), pa...)
	}
	return cmd, args, inputs, ok
}

// npxLine is `npx -y [--package pkg] <command> args…`.
func npxLine(p *registryPackage, rt, opts, prog, pa []string) (string, []string, bool) {
	var line []string
	if slices.Contains(rt, p.Identifier) { // the publisher wrote npx's whole line
		line = append(slices.Clone(rt), pa...)
	} else {
		line = append(append(append(append([]string{}, opts...), p.Identifier), prog...), pa...)
	}
	pkg, i := "", 0
scan:
	for ; i < len(line); i++ {
		switch a := line[i]; {
		case a == "-y" || a == "--yes":
		case (a == "-p" || a == "--package") && i+1 < len(line):
			pkg = line[i+1]
			i++
		case strings.HasPrefix(a, "--package="):
			pkg = strings.TrimPrefix(a, "--package=")
		default:
			break scan
		}
	}
	if i == len(line) {
		return "", nil, false
	}
	exe, rest := line[i], line[i+1:]
	if pkg == "" && exe == p.Identifier && p.Bins != nil {
		// npx runs the bin named after the package, or its only one
		if len(p.Bins) == 0 {
			return "", nil, false
		}
		if !slices.Contains(p.Bins, lastPart(p.Identifier)) && len(p.Bins) != 1 {
			b := pickBin(p.Bins, p.Identifier)
			if b == "" {
				return "", nil, false
			}
			pkg, exe = p.Identifier, b
		}
	}
	out := []string{"-y"}
	if pkg != "" && exe != lastPart(pkg) {
		out = append(out, "--package", pkg)
	} else if pkg != "" {
		exe = pkg
	}
	return "npx", append(append(out, exe), rest...), true
}

// uvxLine is `uvx [options] [--from spec] <executable> args…`.
func uvxLine(p *registryPackage, rt, opts, prog, pa []string) (string, []string, bool) {
	if i := slices.Index(rt, "-m"); i >= 0 && i+1 < len(rt) { // python -m module
		return "uvx", append([]string{"--from", p.Identifier, "python", "-m", rt[i+1]}, pa...), true
	}
	if len(pa) > 0 && pa[0] == "--with" { // uvx's option put among the program's
		opts, pa = append(slices.Clone(opts), pa[:min(2, len(pa))]...), pa[min(2, len(pa)):]
	}
	var line []string
	from := hasOpt(opts, "--from")
	switch {
	case len(pa) > 0 && pa[0] == "--from": // the whole line put among the program's
		line = append(append(slices.Clone(opts), prog...), pa...)
	case from && len(prog) == 0: // --from names the package; its executable is the package's name
		line = append(append(slices.Clone(opts), p.Identifier), pa...)
	case from || slices.Contains(prog, p.Identifier): // the executable is among them
		line = append(append(slices.Clone(opts), prog...), pa...)
	default:
		line = append(append(append(slices.Clone(opts), p.Identifier), prog...), pa...)
	}
	var head []string
	spec, i := "", 0
	for ; i < len(line); i++ {
		a := line[i]
		takes, isOpt := uvOpts[a]
		if !isOpt {
			break
		}
		if takes && i+1 < len(line) {
			if a == "--from" {
				spec = line[i+1]
			} else {
				head = append(head, a, line[i+1])
			}
			i++
			continue
		}
		head = append(head, a)
	}
	if i == len(line) {
		return "", nil, false
	}
	exe, rest := line[i], line[i+1:]
	if p.Bins != nil && !slices.Contains(p.Bins, exe) {
		b := pickBin(p.Bins, p.Identifier)
		if b == "" {
			return "", nil, false
		}
		if spec == "" {
			spec = exe
		}
		exe = b
	}
	if spec != "" && spec != exe {
		head = append(head, "--from", spec)
	}
	return "uvx", append(append(head, exe), rest...), true
}

func hasOpt(line []string, o string) bool {
	for i := 0; i < len(line); i++ {
		if line[i] == o {
			return true
		}
		if uvOpts[line[i]] {
			i++
		}
	}
	return false
}

// pickBin is the executable that serves a package with several, none
// named after it: the only one, or the one named for MCP, or (for a scoped
// npm package like @llmtrim/cli) the one named for its scope. "" when none
// stands out.
func pickBin(bins []string, id string) string {
	if len(bins) == 1 {
		return bins[0]
	}
	one := func(f func(string) bool) string {
		var got []string
		for _, b := range bins {
			if f(b) {
				got = append(got, b)
			}
		}
		if len(got) == 1 {
			return got[0]
		}
		return ""
	}
	for _, f := range []func(string) bool{
		func(b string) bool {
			return strings.HasSuffix(b, "-mcp") || strings.HasSuffix(b, "_mcp") || strings.HasSuffix(b, "-mcp-server")
		},
		func(b string) bool { return strings.Contains(strings.ToLower(b), "mcp") },
		func(b string) bool {
			scope, _, ok := strings.Cut(strings.TrimPrefix(id, "@"), "/")
			return ok && strings.HasPrefix(id, "@") && b == scope
		},
	} {
		if b := one(f); b != "" {
			return b
		}
	}
	return ""
}

// oldLine is the line magpie offered package p with before
// runtimeArguments and the package's executables were read: the runtime,
// the package, and its packageArguments as they came.
func oldLine(p *registryPackage) (string, []string) {
	var cmd string
	var args []string
	switch p.RegistryType {
	case "npm":
		cmd, args = "npx", []string{"-y", p.Identifier}
	case "pypi":
		cmd, args = "uvx", []string{p.Identifier}
	default:
		return "", nil
	}
	for _, a := range p.Arguments {
		t, _ := a.tokens()
		args = append(args, t...)
	}
	return cmd, args
}

func lineKey(cmd string, args []string) string {
	return cmd + "\x00" + strings.Join(args, "\x00")
}

// marketFixes are the popular servers whose line is not the one magpie
// offered before, by that old line: a library that has one installed from
// the market unchanged (Serena's `uvx serena-agent`, which exits 2) is
// given the line that starts it.
var marketFixes = sync.OnceValue(func() map[string]*Server {
	var snap []registryServer
	if json.Unmarshal(serversSnapshot, &snap) != nil {
		return nil
	}
	fixes := map[string]*Server{}
	for i := range snap {
		m, ok := fromRegistry(&snap[i])
		if !ok || m.server.Remote() {
			continue
		}
		kind := map[string]string{"npx": "npm", "uvx": "pypi"}[m.server.Command]
		for j := range snap[i].Packages {
			p := &snap[i].Packages[j]
			if p.RegistryType != kind || p.Identifier == "" || (p.Transport.Type != "" && p.Transport.Type != "stdio") {
				continue // not the package fromRegistry took
			}
			if cmd, args := oldLine(p); lineKey(cmd, args) != lineKey(m.server.Command, m.server.Args) {
				s := m.server
				fixes[lineKey(cmd, args)] = &s
			}
			break
		}
	}
	return fixes
})

// healMarketLines gives each server installed from the market with a line
// that never started the line that does, keeping all else of it.
func (l *Library) healMarketLines() {
	fixes := marketFixes()
	if len(fixes) == 0 {
		return
	}
	for _, s := range l.MCP {
		if s == nil || s.Remote() || (s.Command != "uvx" && s.Command != "npx") {
			continue
		}
		if f := fixes[lineKey(s.Command, s.Args)]; f != nil {
			s.Args = slices.Clone(f.Args)
		}
	}
}
