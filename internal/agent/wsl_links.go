package agent

// Links in a WSL distro, made, read and taken away from inside it.
//
// Through \\wsl.localhost Windows sees a link the distro has as an entry
// it can't follow: Lstat says it is there (Go reports it irregular), but
// Readlink fails ("Incorrect function"), and opening, listing or removing
// it says it isn't there. A link Windows makes there (os.Symlink) is an
// empty folder in the distro, not a link at all. So magpie makes, reads
// and removes a distro's links with wsl.exe, in the distro, where they are
// read (tried on Windows 11 with WSL 2 and Ubuntu 24.04).

import (
	"errors"
	"path"
	"strings"
	"sync"
	"time"
)

// wslExec runs argv in the distro; a var for tests.
var wslExec = func(distro string, argv ...string) ([]byte, error) {
	return wslRun(30*time.Second, append([]string{"-d", distro, "--exec"}, argv...)...)
}

// distroAt is the running distro local, a path as magpie opens it, is in:
// under its root, or under where one of its Links leads (distro.native).
func distroAt(local string) (distro, bool) {
	wsl.Lock()
	defer wsl.Unlock()
	// a command run in a stopped distro would start it (wslUp)
	wslRunningLocked()
	for name, d := range wsl.seen {
		if d == nil || d.Root == "" || !wsl.running[name] {
			continue
		}
		if _, ok := below(local, d.Root); ok {
			return *d, true
		}
		for _, to := range d.Links {
			if to == "" {
				continue
			}
			if _, ok := below(local, d.where(to)); ok {
				return *d, true
			}
		}
	}
	return distro{}, false
}

// agentDistro is the distro a's files are in, as last probed.
func agentDistro(a *Agent) (distro, error) {
	if a == nil || a.WSL == "" {
		return distro{}, errors.New("not an agent in WSL")
	}
	wsl.Lock()
	d := wsl.seen[a.WSL]
	wsl.Unlock()
	if d == nil || d.Root == "" {
		return distro{}, errors.New("WSL " + a.WSL + " hasn't been looked in")
	}
	return *d, nil
}

// wslLinkScript makes $2 a link to $1: in place of a link there, never of
// a folder or file, which it refuses with 3 (said on stdout, which is
// what wslRun reads).
const wslLinkScript = `if [ -e "$2" ] && [ ! -L "$2" ]; then echo "$2 is not a link"; exit 3; fi
mkdir -p -- "$(dirname -- "$2")" && ln -sfn -- "$1" "$2"`

// wslUnlinkScript removes $1 when it is a link, and nothing else.
const wslUnlinkScript = `if [ -L "$1" ]; then rm -f -- "$1"; elif [ -e "$1" ]; then echo "$1 is not a link"; exit 3; fi`

// wslLinksScript lists the links in the folder $1, one name, a tab and
// where it leads to a line.
const wslLinksScript = `for f in "$1"/* "$1"/.[!.]*; do [ -L "$f" ] && printf '%s\t%s\n' "${f##*/}" "$(readlink -- "$f")"; done; true`

// WSLSymlink makes link a link to target, both in a's distro as magpie
// opens them, from inside the distro: a link the agent there follows. A
// link at link already is replaced; a folder or file there is refused.
func WSLSymlink(a *Agent, target, link string) error {
	d, err := agentDistro(a)
	if err != nil {
		return err
	}
	out, err := wslExec(d.Name, "sh", "-c", wslLinkScript, "sh", d.native(target), d.native(link))
	forgetLinks(link)
	if err != nil {
		return wslFailed("link "+d.native(link), out, err)
	}
	return nil
}

// WSLRemoveLink takes the link at local, a path in a running distro as
// magpie opens it, away from inside the distro; what it leads to stays.
// Anything there but a link is refused.
func WSLRemoveLink(local string) error {
	d, ok := distroAt(local)
	if !ok {
		return errors.New(local + " isn't in a running WSL distro")
	}
	out, err := wslExec(d.Name, "sh", "-c", wslUnlinkScript, "sh", d.native(local))
	forgetLinks(local)
	if err != nil {
		return wslFailed("remove "+d.native(local), out, err)
	}
	return nil
}

func wslFailed(what string, out []byte, err error) error {
	if s := strings.TrimSpace(string(out)); s != "" {
		return errors.New(what + " in WSL: " + s)
	}
	return errors.New(what + " in WSL: " + err.Error())
}

// links keeps each folder's links as the distro listed them, for
// linksAge: a page read looks at every agent's skills, each a wsl.exe
// otherwise.
var links struct {
	sync.Mutex
	m map[string]linkList
}

type linkList struct {
	at time.Time
	to map[string]string // by name, as the distro spells where it leads
}

const linksAge = 3 * time.Second

// forgetLinks drops what was listed of the folder local is in.
func forgetLinks(local string) {
	dir, _ := splitLocal(local)
	links.Lock()
	delete(links.m, strings.ToLower(dir))
	links.Unlock()
}

// splitLocal is local's folder and name, cut at its last separator.
func splitLocal(local string) (string, string) {
	i := strings.LastIndexAny(local, `\/`)
	if i < 0 {
		return "", local
	}
	return local[:i], local[i+1:]
}

// WSLReadlink is where the link at local, a path in a running distro as
// magpie opens it, leads, as magpie opens that: false when local is in no
// running distro or isn't a link there. It is asked where Windows can't
// read the link itself (Readlink fails through \\wsl.localhost).
func WSLReadlink(local string) (string, bool) {
	d, ok := distroAt(local)
	if !ok {
		return "", false
	}
	dir, name := splitLocal(local)
	key := strings.ToLower(dir)
	links.Lock()
	l, ok := links.m[key]
	links.Unlock()
	if !ok || time.Since(l.at) > linksAge {
		out, err := wslExec(d.Name, "sh", "-c", wslLinksScript, "sh", d.native(dir))
		if err != nil {
			return "", false
		}
		l = linkList{at: time.Now(), to: parseLinks(string(out))}
		links.Lock()
		if links.m == nil {
			links.m = map[string]linkList{}
		}
		links.m[key] = l
		links.Unlock()
	}
	to, ok := l.to[name]
	if !ok || to == "" {
		return "", false
	}
	if !strings.HasPrefix(to, "/") {
		to = path.Join(d.native(dir), to)
	}
	return d.where(path.Clean(to)), true
}

// parseLinks reads wslLinksScript's lines.
func parseLinks(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		if name, to, ok := strings.Cut(line, "\t"); ok && name != "" {
			m[name] = to
		}
	}
	return m
}

// WSLInDistro is whether local, a path of a's as magpie opens it, is in
// the distro's own files (under \\wsl.localhost\<distro>), not on a
// Windows drive a link there leads to: where the distro can make a link
// magpie reads back.
func WSLInDistro(a *Agent, local string) bool {
	d, err := agentDistro(a)
	if err != nil || local == "" {
		return false
	}
	_, ok := below(local, d.Root)
	return ok
}
