package library

// magpie's own servers for an agent in WSL (#900, #7696): image here, and
// the rest the same way, without --wsl. The library names
// it by this machine's binary, a Windows program, which the agent starts
// through WSL's interop all the same: by the binary's path inside the
// distro (/mnt/d/…/magpie-windows-amd64.exe), with --wsl telling it the
// distro, so the paths the agent gives (its roots, a reference image, where
// to save) are opened through \\wsl.localhost and the paths it answers are
// the distro's. The Windows program reaches the gateway at 127.0.0.1 and
// reads this machine's settings, as the app does; a Linux magpie in the
// distro would need neither the gateway's address from inside WSL nor to be
// the same version, and isn't looked for. A user's own Windows program is
// still refused: its paths and variables wouldn't cross over.

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/wslrun"
)

// isSelf is whether s is magpie's own image server, whatever its binary.
func isSelf(s *Server) bool {
	return !s.Remote() && len(s.Args) >= 2 && s.Args[0] == "mcp" && s.Args[1] == "image"
}

// isBuiltinWSL is whether s is one of magpie's own `magpie mcp <name>`
// servers (image, search, artifacts, browser, vision, memory, sessions,
// api, and any added later), which an agent in WSL starts through interop
// by the binary's path in the distro (#7696). Only image is told the
// distro (--wsl); the others take no arguments of that kind.
func isBuiltinWSL(s *Server) bool {
	return isSelf(s) || ownServer(s) && len(s.Args) >= 2
}

// side is s as the file's agent is given it: magpie's own server, for an
// agent in WSL, by the binary's path there; an error says why the agent
// can't start s.
func (f *mcpFile) side(s *Server) (*Server, error) {
	if !f.WSL || s.Remote() || !windowsPath.MatchString(s.Command) {
		return s, nil
	}
	if !isBuiltinWSL(s) || f.Distro == "" {
		return nil, fmt.Errorf("it runs a Windows program (%s), which an agent in WSL can't start: give it a command WSL has", s.Command)
	}
	exe, mount, err := wslExe(f.Distro, s.Command)
	if err != nil {
		return nil, err
	}
	c := *s
	c.Command = exe
	if isSelf(s) {
		c.Args = []string{"mcp", "image", "--wsl", f.Distro}
		if mount != "/mnt/" {
			c.Args = append(c.Args, "--mount", mount)
		}
		if f.Home != "" {
			c.Args = append(c.Args, "--home", f.Home)
		}
	} else {
		c.Args = slices.Clone(s.Args)
	}
	if len(s.Env) > 0 {
		// a Windows program is given only the variables WSLENV names
		c.Env = map[string]string{}
		var names []string
		for k, v := range s.Env {
			c.Env[k] = v
			names = append(names, k)
		}
		slices.Sort(names)
		c.Env["WSLENV"] = strings.Join(names, ":")
	}
	return &c, nil
}

// errNoInterop is why a distro whose interop is off can't start magpie's
// own server.
var errNoInterop = errors.New("WSL's interop is off in this distro, so an agent there can't start magpie (a Windows program): turn it on in /etc/wsl.conf ([interop] enabled=true) and restart WSL")

// wslProbe runs script in the distro and gives what it printed; a var for
// tests. A distro that isn't running isn't asked, which would start it.
var wslProbe = func(distro, script string) (string, error) {
	if !wslrun.On {
		return "", errors.New("no WSL")
	}
	if !wslrun.Up(distro) {
		return "", errors.New("WSL " + distro + " isn't running")
	}
	b, err := wslrun.Run(10*time.Second, "-d", distro, "--exec", "/bin/sh", "-c", script)
	return string(b), err
}

var wslExes struct {
	sync.Mutex
	at  map[string]time.Time
	out map[string][3]string // exe, mount, "off" when interop is
}

// wslExe is where the Windows program win is inside the distro, and where
// the distro mounts Windows' drives (/mnt/); an error when the distro's
// interop is off. Asked of the distro once in ten minutes; when it can't
// be asked, the default mount is taken.
func wslExe(distro, win string) (exe, mount string, err error) {
	key := distro + "\x00" + win
	wslExes.Lock()
	defer wslExes.Unlock()
	if wslExes.at == nil {
		wslExes.at, wslExes.out = map[string]time.Time{}, map[string][3]string{}
	}
	if at, ok := wslExes.at[key]; !ok || time.Since(at) > 10*time.Minute {
		o := [3]string{}
		q := "'" + strings.ReplaceAll(win, "'", `'\''`) + "'"
		out, perr := wslProbe(distro, "wslpath -u "+q+"; if [ -e /proc/sys/fs/binfmt_misc/WSLInterop ] || [ -e /proc/sys/fs/binfmt_misc/WSLInterop-late ]; then echo interop:on; else echo interop:off; fi")
		if perr == nil {
			for _, l := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
				switch {
				case l == "interop:off":
					o[2] = "off"
				case strings.HasPrefix(l, "/"):
					o[0] = l
				}
			}
		}
		wslExes.at[key], wslExes.out[key] = time.Now(), o
	}
	o := wslExes.out[key]
	if o[2] == "off" {
		return "", "", errNoInterop
	}
	def := wslrun.Tool{}.Linux(win)
	if o[0] == "" {
		return def, "/mnt/", nil
	}
	// /mnt/d/tools/x.exe is /mnt/ + d/tools/x.exe
	mount = "/mnt/"
	if tail := strings.TrimPrefix(def, "/mnt/"); strings.HasSuffix(strings.ToLower(o[0]), strings.ToLower(tail)) {
		mount = o[0][:len(o[0])-len(tail)]
	}
	return o[0], mount, nil
}

// linuxHome is a WSL agent's $HOME as the distro spells it, from where
// magpie opens it: \\wsl.localhost\Ubuntu\home\me is /home/me.
func linuxHome(local string) string {
	for _, pre := range []string{`\\wsl.localhost\`, `\\wsl$\`} {
		if len(local) > len(pre) && strings.EqualFold(local[:len(pre)], pre) {
			rest := local[len(pre):]
			if i := strings.IndexByte(rest, '\\'); i >= 0 {
				return strings.ReplaceAll(rest[i:], `\`, "/")
			}
			return "/"
		}
	}
	return ""
}
