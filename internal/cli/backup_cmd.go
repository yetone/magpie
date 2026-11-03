package cli

import (
	"bufio"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"

	"golang.org/x/term"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/edit"
)

// backupCmd writes every provider, setting, profile and agent model, and
// the library, to one file sealed with a passphrase:
// magpie backup [--no-keys] [--no-library] [--sign-ins] [file].
func backupCmd(args []string) error {
	keys, lib, signIns, file := true, true, false, ""
	for _, a := range args {
		switch {
		case a == "--no-keys":
			keys = false
		case a == "--no-library":
			lib = false
		case a == "--sign-ins":
			signIns = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %s (magpie backup [--no-keys] [--no-library] [--sign-ins] [file])", a)
		case file == "":
			file = a
		default:
			return errors.New("usage: magpie backup [--no-keys] [--no-library] [--sign-ins] [file]")
		}
	}
	if file == "" {
		file = "magpie" + backup.Ext
	}
	b, err := backup.Collect(keys, version)
	if err != nil {
		return err
	}
	if !lib {
		b.Library = nil
	}
	if signIns {
		if err := backup.WithSignIns(&b); err != nil {
			return fmt.Errorf("the sign-ins couldn't be read, so no backup was made: %w", err)
		}
	}
	pass, err := passphrase("Passphrase for the backup: ", true)
	if err != nil {
		return err
	}
	data, err := backup.Seal(b, pass)
	if err != nil {
		return err
	}
	if err := edit.WriteAtomic(file, data); err != nil {
		return err
	}
	os.Chmod(file, 0o600)
	what := "with their keys"
	if !keys {
		what = "without keys"
	}
	fmt.Printf("%s %s: %d providers %s, %d profiles, %d agent settings",
		green.Render("saved"), file, len(b.Providers), what, len(b.Profiles), len(b.Agents))
	if l := b.Library; l != nil {
		fmt.Printf("; library: %d MCP servers, %d skills", len(l.MCP), len(l.Skills))
		if l.Subagents != nil && len(*l.Subagents) > 0 {
			fmt.Printf(", %d subagents", len(*l.Subagents))
		}
	}
	if s := b.SignIns; s != nil {
		fmt.Printf("; %d sign-ins", len(s.Logins)+len(s.Plugins))
	}
	fmt.Println()
	if b.SignIns != nil {
		fmt.Println(muted.Render("The subscriptions' and plugins' sign-ins are in it, sealed with the passphrase: anyone with the file and the passphrase can use those accounts. Keep the passphrase: without it the file can't be opened."))
	} else {
		fmt.Println(muted.Render("Subscriptions are not in it: sign in to them on the other machine (or back up with --sign-ins). Keep the passphrase: without it the file can't be opened."))
	}
	return nil
}

// restoreCmd puts a backup in: magpie restore [--no-agents] [--no-library] [--no-sign-ins] <file>.
func restoreCmd(args []string) error {
	parts, file := backup.All, ""
	for _, a := range args {
		switch {
		case a == "--no-agents":
			parts.Agents = false
		case a == "--no-library":
			parts.Library = false
		case a == "--no-sign-ins":
			parts.SignIns = false
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %s (magpie restore [--no-agents] [--no-library] [--no-sign-ins] <file>)", a)
		case file == "":
			file = a
		default:
			return errors.New("usage: magpie restore [--no-agents] [--no-library] [--no-sign-ins] <file>")
		}
	}
	if file == "" {
		return errors.New("usage: magpie restore [--no-agents] [--no-library] [--no-sign-ins] <file>")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	pass, err := passphrase("Passphrase: ", false)
	if err != nil {
		return err
	}
	b, err := backup.Open(data, pass)
	if err != nil {
		return err
	}
	r, err := backup.Restore(b, parts)
	if err != nil {
		return err
	}
	fmt.Printf("%s providers: %d added, %d replaced", green.Render("restored"), r.Added, r.Replaced)
	if r.Settings {
		fmt.Print("; settings")
	}
	fmt.Printf("; %d profiles; %d agent settings changed", r.Profiles, r.Agents)
	if r.Library {
		fmt.Print("; library")
	}
	if s := r.SignIns; s != nil {
		fmt.Printf("; sign-ins: %d added, %d updated, %d kept as they are here", s.Added, s.Updated, s.Kept)
		if s.Codex {
			fmt.Print(" (Codex signed in)")
		}
	}
	fmt.Println()
	if len(r.NeedKey) > 0 {
		fmt.Println(muted.Render("Needs a key (magpie provider key <id>): " + strings.Join(r.NeedKey, ", ")))
	}
	if len(r.SignIn) > 0 {
		fmt.Println(muted.Render("The backup's settings use these, not signed in on this computer (a backup carries settings, not sign-ins; to use one here: magpie accounts add <id>): " + strings.Join(r.SignIn, ", ")))
	}
	if len(r.Plugins) > 0 {
		fmt.Println(muted.Render("Plugins added: " + strings.Join(r.Plugins, ", ")))
	}
	for _, spec := range slices.Sorted(maps.Keys(r.PluginErrors)) {
		fmt.Println(muted.Render(fmt.Sprintf("Couldn't add the plugin %s: %s", spec, r.PluginErrors[spec])))
	}
	for _, p := range r.LibraryProblems {
		fmt.Println(muted.Render(fmt.Sprintf("Library: %s couldn't get %s: %s", p.Agent, p.What, p.Error)))
	}
	if len(r.Skipped) > 0 {
		fmt.Println(muted.Render("Left as they are (agent not here, or its model can't be reached yet): " + strings.Join(r.Skipped, ", ")))
	}
	return nil
}

// passphrase reads one from the terminal without echo, asked twice when
// it is being set; piped in, it is the next line of stdin.
func passphrase(prompt string, confirm bool) (string, error) {
	return secret("passphrase", prompt, confirm)
}

// stdin is read through one reader, so a second secret piped in is the
// second line and not lost to the first one's buffer. The reader is made
// over os.Stdin as it is when first read, never at package init: package
// variables are set before any init runs, and on Windows the desktop
// build's init (console_windows.go) only then swaps in the terminal's
// CONIN$. A reader made earlier keeps the invalid handle, and every
// prompt fails with "read /dev/stdin: The handle is invalid".
var (
	stdinMu   sync.Mutex
	stdinFile *os.File
	stdinBuf  *bufio.Reader
)

func stdin() *bufio.Reader {
	stdinMu.Lock()
	defer stdinMu.Unlock()
	if stdinBuf == nil || stdinFile != os.Stdin {
		stdinFile, stdinBuf = os.Stdin, bufio.NewReader(os.Stdin)
	}
	return stdinBuf
}

// secret reads what (a passphrase, a password) as passphrase does; piped
// in, it is the next line of stdin.
func secret(what, prompt string, confirm bool) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := stdin().ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if err != nil {
				return "", fmt.Errorf("no %s on stdin: %w", what, err)
			}
			return "", fmt.Errorf("the %s is empty", what)
		}
		return line, nil
	}
	read := func(p string) (string, error) {
		fmt.Fprint(os.Stderr, p)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	pass, err := read(prompt)
	if err != nil {
		return "", err
	}
	if pass == "" {
		return "", fmt.Errorf("the %s is empty", what)
	}
	if confirm {
		again, err := read("Again: ")
		if err != nil {
			return "", err
		}
		if again != pass {
			return "", fmt.Errorf("the %ss differ", what)
		}
	}
	return pass, nil
}
