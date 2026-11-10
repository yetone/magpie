package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/proc"
)

// `magpie <agent> run [args…]` is what the function magpie puts in the
// user's shells for an agent that takes magpie's gateway and model only
// from how it is started (Agent.Start: the built-in agy, an agent plugin
// with launch) runs: the agent's command on PATH, given magpie's
// variables and model while the agent is on one of magpie's models, and
// as it is otherwise (and with an argument that runs it plain, agy's
// remote-control, which needs agy's Google sign-in). Every argument is the
// agent's, --help too. `magpie <agent> env` prints those variables, the
// value of each flag it is started with (settings=<file> for --settings)
// and the model, one NAME=value a line and nothing off magpie, for
// PowerShell's function.

func startUsage(id string, s agent.Starting) string {
	vars := strings.Join(s.Names(), ", ")
	if vars == "" {
		vars = "magpie's gateway"
	}
	model := ""
	for i := 0; i+1 < len(s.Args); i += 2 {
		vars += ", " + s.Args[i] + " " + s.Args[i+1]
	}
	if s.Flag != "" {
		model = " and\n" + s.Flag + " <its model> (not when you give " + s.Flag + " yourself)"
	}
	return fmt.Sprintf(`magpie %[1]s run [args…]   start %[2]s, on magpie while its model is one of magpie's

The %[2]s on your PATH starts with %[3]s%[4]s; every argument is
%[2]s's. Off magpie's models it starts as it is. The function %[2]s magpie
puts in your shell's profile runs this.

magpie %[1]s env           the variables and model it starts %[2]s with, a
                         NAME=value a line; nothing while it isn't on magpie
`, id, s.Bin, vars, model)
}

// startCmd is `magpie <agent> run|env …` for an agent magpie starts
// (agent.Starter); ok false for another form.
func startCmd(args []string) (ok bool, err error) {
	if len(args) < 2 || args[1] != "run" && args[1] != "env" {
		return false, nil
	}
	a := agent.Starter(args[0])
	if a == nil {
		return false, nil
	}
	s := a.Start()
	if args[1] == "run" {
		return true, startRun(s, args[2:])
	}
	if len(args) > 2 {
		if slices.Contains([]string{"-h", "--help", "help"}, args[2]) {
			fmt.Print(startUsage(args[0], s))
			return true, nil
		}
		return true, fmt.Errorf("magpie %s env takes nothing after it, not %q", args[0], args[2])
	}
	if s.Model == "" {
		return true, nil
	}
	for _, kv := range s.Env {
		fmt.Println(kv)
	}
	for i := 0; i+1 < len(s.Args); i += 2 {
		fmt.Println(strings.TrimLeft(s.Args[i], "-") + "=" + s.Args[i+1])
	}
	fmt.Println("model=" + s.Model)
	return true, nil
}

// startExec starts the program at path as name with args and env, in
// place of magpie where the system can (start_exec_*.go); tests set it.
var startExec = startExecOS

// startLook is the command on PATH; tests set it.
var startLook = proc.Look

func startRun(s agent.Starting, args []string) error {
	path, err := startLook(s.Bin)
	if err != nil {
		return fmt.Errorf("%s isn't installed, or isn't on your PATH", s.Bin)
	}
	if self, err := os.Executable(); err == nil && sameProgram(self, path) {
		return errors.New("the " + s.Bin + " on your PATH is magpie itself; install the agent")
	}
	env := os.Environ()
	if slices.ContainsFunc(args, s.Plained) {
		s.Model = ""
	}
	if s.Model != "" {
		names := s.Names()
		env = slices.DeleteFunc(env, func(kv string) bool {
			k, _, _ := strings.Cut(kv, "=")
			return slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(k, n) })
		})
		env = append(env, s.Env...)
		if s.Flag != "" && !slices.ContainsFunc(args, s.Given) {
			args = append([]string{s.Flag, s.Model}, args...)
		}
		args = append(slices.Clone(s.Args), args...)
	}
	return startExec(path, s.Bin, args, env)
}

func sameProgram(a, b string) bool {
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		a = ra
	}
	if rb, err := filepath.EvalSymlinks(b); err == nil {
		b = rb
	}
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(sa, sb)
}
