package provider

// Some subscriptions are used through the vendor's own CLI — Cursor's,
// Grok's, Devin's — and signed in with it too. One that isn't installed is
// installed with the vendor's own installer when its sign-in starts, as the
// vendor's page tells a person to, rather than leaving the sign-in to fail
// or, as Devin's did, to finish with nothing to show for it.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/proc"
)

// agentCLI is a CLI a subscription runs through: how to find it, and the
// vendor's install one-liners for a POSIX shell and for PowerShell. native,
// when there is one, does what the shell one does without a shell, for a
// magpie with none (its Docker image has no bash, curl or wget). setup: the
// installer ends by running the CLI's own setup, which asks how to sign in;
// magpie signs in itself once the CLI is there.
type agentCLI struct {
	Name   string
	find   func() string
	sh     string
	ps     string
	native func(ctx context.Context) error
	setup  bool
}

func cliFor(agent string) (agentCLI, bool) {
	switch agent {
	case "devin":
		return agentCLI{Name: "Devin CLI", find: func() string { return DevinExecutable() },
			sh:    "curl -fsSL https://cli.devin.ai/install.sh | bash",
			ps:    "irm https://static.devin.ai/cli/setup.ps1 | iex",
			setup: true}, true
	case "cursor":
		return agentCLI{Name: "Cursor CLI", find: func() string { return CursorExecutable() },
			sh: "curl https://cursor.com/install -fsS | bash",
			ps: "irm 'https://cursor.com/install?win32=true' | iex"}, true
	case "grok":
		return agentCLI{Name: "Grok Build", find: func() string { return GrokExecutable() },
			sh:     "curl -fsSL https://x.ai/cli/install.sh | bash",
			ps:     "irm https://x.ai/cli/install.ps1 | iex",
			native: installGrokBuild}, true
	}
	return agentCLI{}, false
}

// missingCLI is the CLI an agent's sign-in needs and this machine lacks.
func missingCLI(agent string) (agentCLI, bool) {
	c, ok := cliFor(agent)
	if !ok || c.find() != "" {
		return agentCLI{}, false
	}
	return c, true
}

// installTimeout bounds an installer: they download a few hundred MB at most.
var installTimeout = 10 * time.Minute

// runInstaller runs a vendor's installer; a var so tests can fake it.
var runInstaller = func(ctx context.Context, c agentCLI) ([]byte, error) {
	if runtime.GOOS != "windows" && c.native != nil && !shellInstallerRuns() {
		return nil, c.native(ctx)
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// the one-liner goes to PowerShell on stdin, as one typed into it
		// does: on PowerShell's command line, Microsoft Defender took
		// "irm <url> | iex" for a Trojan (Trojan:Win32/Commando.A!ml) and
		// stopped it before it ran, which came back as "Access is denied"
		cmd = proc.CommandContext(ctx, "powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", "-")
		cmd.Stdin = strings.NewReader(c.ps + "\n")
	} else {
		cmd = proc.CommandContext(ctx, "bash", "-c", c.sh)
	}
	cmd.Dir, _ = os.UserHomeDir()
	cmd.Env = netproxy.Env(nil)
	if runtime.GOOS == "windows" && c.setup {
		return runUntilInstalled(ctx, cmd, c)
	}
	// nothing more on stdin: an installer that would ask something takes
	// its default
	return cmd.CombinedOutput()
}

// installerTail is how long an installer that ends in its CLI's setup is
// given to end by itself once the CLI is in place; a var so tests can
// shorten it.
var installerTail = 5 * time.Second

// runUntilInstalled runs an installer that ends in its CLI's setup, on
// Windows, and ends it, with what it started, installerTail after the CLI
// is in place. Devin's runs `devin setup`, whose menu asking how to sign in
// waits for a key on the console, and no one can answer the console an
// installer gets from magpie (it has no window, and magpie keeps its
// output): the sign-in waited out installTimeout, then went on with
// `devin setup` still running.
func runUntilInstalled(ctx context.Context, cmd *exec.Cmd, c agentCLI) ([]byte, error) {
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	t, err := proc.StartTree(cmd)
	if err != nil {
		return nil, err
	}
	ended := make(chan struct{})
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		var tail <-chan time.Time
		for {
			select {
			case <-ended:
				return
			case <-ctx.Done():
				t.Kill()
				return
			case <-tail:
				t.Kill()
				return
			case <-tick.C:
				if tail != nil {
					continue
				}
				// the installer puts the CLI's folder on the user's PATH,
				// which a running program reads from the registry
				refreshPath()
				if c.find() != "" {
					tail = time.After(installerTail)
				}
			}
		}
	}()
	err = t.Wait()
	close(ended)
	return out.Bytes(), err
}

// shellInstallerRuns is whether a vendor's `curl … | bash` can run here:
// bash, and curl, which every one-liner (and the scripts behind them)
// downloads with. magpie's Docker image had none of them, and the install
// failed there with "exec: bash: not found". Since 7bfe3137 it has bash and
// busybox's wget but still no curl, and the one-liner failed there too. A
// var so tests can say there is none.
var shellInstallerRuns = func() bool {
	for _, p := range []string{"bash", "curl"} {
		if _, err := exec.LookPath(p); err != nil {
			return false
		}
	}
	return true
}

// installCLI installs c with its vendor's installer and makes sure magpie
// finds it afterwards.
func installCLI(ctx context.Context, c agentCLI) error {
	ctx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()
	out, err := runInstaller(ctx, c)
	// an installer adds its directory to the user's PATH, which a running
	// program never sees
	refreshPath()
	manual := c.sh
	if runtime.GOOS == "windows" {
		manual = c.ps
	}
	if ctx.Err() == context.Canceled {
		return ctx.Err()
	}
	// what counts is the CLI being there: an installer may end in a step
	// that wants a terminal — Devin's runs `devin setup`, whose sign-in
	// gives up without one — and fail after it installed everything
	if c.find() != "" {
		return nil
	}
	if err != nil {
		msg := lastLine(out)
		if msg == "" {
			msg = err.Error()
		}
		return errorf("installing %s failed (%s); install it yourself with `%s`, then sign in again", c.Name, msg, manual)
	}
	return errorf("the %s installer finished, but magpie can't find it; install it yourself with `%s`, then sign in again", c.Name, manual)
}

// lastLine is the last line of a command's output with something on it,
// without its colours.
func lastLine(out []byte) string {
	lines := strings.Split(ansi.ReplaceAllString(string(out), ""), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}
