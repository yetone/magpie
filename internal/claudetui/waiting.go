package claudetui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// running is a command Claude Code runs, and how long it has run.
type running struct {
	age  time.Duration
	args string
}

// undrawn is why a Claude Code that drew nothing hasn't: what it runs
// meanwhile. Claude Code reads its macOS Keychain sign-in with `security
// find-generic-password` before it draws anything, and one read has no
// time limit: a Keychain that waits for its user to allow the read, or to
// be unlocked, held it with nothing on its screen, and the error said
// only "0 bytes read" (sowhynot).
func (s *shim) undrawn() string {
	cmds, rss, ok := s.waiting()
	if !ok {
		return ""
	}
	for _, c := range cmds {
		if svc, ok := keychainRead(c.args); ok {
			return fmt.Sprintf("it waits on the macOS Keychain, whose `security find-generic-password` read of %q hasn't answered in %s: a Keychain window may be asking to allow it, or the login keychain is locked. Answer it (Always Allow), or unlock the keychain in Keychain Access, and try again", svc, c.age.Round(time.Second))
		}
	}
	if len(cmds) == 0 {
		return fmt.Sprintf("it runs no command, %d MiB resident", rss>>10)
	}
	var names []string
	for _, c := range cmds[:min(len(cmds), 3)] {
		names = append(names, fmt.Sprintf("%s (for %s)", program(c.args), c.age.Round(time.Second)))
	}
	return fmt.Sprintf("it waits on %s, %d MiB resident", strings.Join(names, ", "), rss>>10)
}

// keychainRead is whether args are a `security find-generic-password`
// read, a script named security too, and the item's service (-s).
func keychainRead(args string) (string, bool) {
	f := strings.Fields(args)
	at := -1
	for i := 1; i < len(f); i++ {
		if f[i] == "find-generic-password" && filepath.Base(f[i-1]) == "security" {
			at = i
			break
		}
	}
	if at < 0 {
		return "", false
	}
	svc := "its item"
	for i := at + 1; i+1 < len(f); i++ {
		if f[i] == "-s" {
			// ps joins the arguments with spaces: the service is the rest,
			// up to the next flag
			var w []string
			for _, v := range f[i+1:] {
				if strings.HasPrefix(v, "-") {
					break
				}
				w = append(w, v)
			}
			svc = strings.Trim(strings.Join(w, " "), `"`)
			break
		}
	}
	return svc, true
}

// program is the name of the program args run, and of the script an
// interpreter runs: never the rest of its arguments, which can carry a
// secret.
func program(args string) string {
	f := strings.Fields(args)
	if len(f) == 0 {
		return "a command"
	}
	name := filepath.Base(f[0])
	switch strings.TrimRight(name, "0123456789.") {
	case "sh", "bash", "zsh", "dash", "node", "bun", "python", "ruby", "perl":
		if len(f) > 1 && !strings.HasPrefix(f[1], "-") {
			name += " " + filepath.Base(f[1])
		}
	}
	return name
}
