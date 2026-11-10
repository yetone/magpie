package library

import (
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
)

// A registry entry's runtimeArguments for an OCI package are docker run's
// own options, written by whoever published the entry; anyone can publish
// to the MCP registry the market searches, and the market shows such a
// server as "docker" alone. Taken whole, `--network host` puts the
// container on this computer's loopback, where magpie's gateway and its
// GUI API take a request as the owner's, and `--privileged`, a bind mount
// of / or of docker.sock, `--pid host` or `--env-file ~/.aws/…` reach past
// the container to the computer. So an option is taken only when it keeps
// the container a container: the ones below, a volume only when it is a
// named or anonymous one, never a folder of this computer's. Any other
// is left out, and a server that needs one isn't offered.

// dockerFlags are docker run's options that take no value and are safe.
var dockerFlags = map[string]bool{"-i": true, "--interactive": true, "--rm": true, "--init": true, "--read-only": true}

// dockerValued are docker run's options that take a value and are safe
// with any value: the container's own environment, entrypoint, folder,
// user and platform.
var dockerValued = map[string]bool{"-e": true, "--env": true, "--entrypoint": true, "-w": true,
	"--workdir": true, "-u": true, "--user": true, "--platform": true, "--name": true, "--pull": true}

// volumeName is a named volume as docker names one.
var volumeName = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// volumeOpts are what may follow a -v's container path.
var volumeOpts = map[string]bool{"ro": true, "rw": true, "z": true, "Z": true, "nocopy": true,
	"readonly": true, "consistent": true, "cached": true, "delegated": true}

// dockerOptSafe says whether runtime argument a, written on the line as
// toks, is one of docker run's options that keeps the container one.
func dockerOptSafe(a registryArg, toks []string) bool {
	name, val, has := "", "", false
	switch {
	case a.Type == "named" && len(toks) == 2:
		name, val, has = toks[0], toks[1], true
	case len(toks) == 1:
		name, val, has = strings.Cut(toks[0], "=")
		if !strings.HasPrefix(name, "-") {
			return false // a positional value docker would read as the image
		}
	default:
		return false
	}
	if !has {
		return dockerFlags[name]
	}
	if dockerValued[name] {
		return true
	}
	switch name {
	case "-v", "--volume":
		return safeVolume(val)
	case "--mount":
		return safeMount(val)
	case "--network", "--net":
		return val == "none" || val == "bridge"
	}
	return false
}

// safeVolume: a -v that is a named volume, or an anonymous one, mounted
// at a container path; never a path, ~, or a Windows drive of this
// computer.
func safeVolume(v string) bool {
	if strings.Contains(v, `\`) {
		return false
	}
	parts := strings.Split(v, ":")
	switch len(parts) {
	case 1: // an anonymous volume at that path in the container
		return strings.HasPrefix(parts[0], "/")
	case 2, 3:
		if !volumeName.MatchString(parts[0]) || !strings.HasPrefix(parts[1], "/") {
			return false
		}
		if len(parts) == 3 {
			for _, o := range strings.Split(parts[2], ",") {
				if !volumeOpts[o] {
					return false
				}
			}
		}
		return true
	}
	return false
}

// safeMount: a --mount of a volume (named, or none named) or a tmpfs,
// never a bind or anything else of this computer's.
func safeMount(v string) bool {
	typ := "volume" // docker's own default
	for _, f := range strings.Split(v, ",") {
		k, val, _ := strings.Cut(f, "=")
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "type":
			typ = strings.ToLower(val)
		case "src", "source":
			if !volumeName.MatchString(val) {
				return false
			}
		case "bind-propagation", "bind-recursive", "volume-driver", "volume-opt":
			return false // a driver's options can name a folder of this computer's
		}
	}
	return typ == "volume" || typ == "tmpfs"
}
