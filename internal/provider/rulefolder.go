package provider

// A rule's folders (Rule.Folders): the request's working folder, as the
// agent states it in the request (gateway/workdir.go), is one of them or
// under one (Cario0x on X: routing by the project being worked on).

import (
	"fmt"
	"os"
	"path"
	"runtime"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
)

// foldCase: folders are told apart without case, as macOS's and
// Windows's file systems tell them by default; tests set it.
var foldCase = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// ruleHome is the home folder a rule's ~ stands for; tests set it.
var ruleHome = func() string { h, _ := os.UserHomeDir(); return h }

var (
	drivePath = lazyre.New(`^[A-Za-z]:(/|$)`)
	wslMount  = lazyre.New(`^/mnt/([A-Za-z])(/|$)`)
	wslShare  = lazyre.New(`(?i)^//wsl(\$|\.localhost)/[^/]+(/|$)`)
)

// folderKey puts a folder the one way it is compared: forward slashes, ~
// as the home folder, a Windows drive as WSL mounts it (C:\x and
// /mnt/c/x are one), a WSL distro's share as the distro's own path
// (\\wsl$\Ubuntu\home\x is /home/x), no trailing slash, and without case
// where the system's folders have none (a drive's always). "" when it
// isn't a whole path.
func folderKey(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	if p == "~" || strings.HasPrefix(p, "~/") {
		h := strings.ReplaceAll(ruleHome(), `\`, "/")
		if h == "" {
			return ""
		}
		p = h + p[1:]
	}
	fold := foldCase
	if m := wslShare.FindString(p); m != "" {
		p = "/" + p[len(m):]
	}
	if drivePath.MatchString(p) {
		p = "/mnt/" + strings.ToLower(p[:1]) + p[2:]
	}
	if m := wslMount.FindStringSubmatch(p); m != nil {
		p = "/mnt/" + strings.ToLower(m[1]) + p[len("/mnt/")+1:]
		fold = true // a Windows drive, from either side
	}
	if !strings.HasPrefix(p, "/") {
		return ""
	}
	p = path.Clean(p)
	if fold {
		p = strings.ToLower(p)
	}
	return p
}

// FolderUnder reports whether folder is root or a folder inside it: at a
// path's boundary, so /a/b is not under /a/bc. An unknown folder ("") is
// under none.
func FolderUnder(folder, root string) bool {
	f, r := folderKey(folder), folderKey(root)
	if f == "" || r == "" {
		return false
	}
	return f == r || r == "/" || strings.HasPrefix(f, r+"/")
}

// cleanFolder checks a rule's folder is a whole path, ~/x or C:\x, and
// keeps it as the user typed it but for a trailing slash.
func cleanFolder(p string) (string, error) {
	p = strings.TrimSpace(p)
	if t := strings.TrimRight(p, `/\`); t != "" && !(len(t) == 2 && t[1] == ':') {
		p = t // the root, and a drive's, keep theirs
	}
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) && !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, `\\`) && !drivePath.MatchString(strings.ReplaceAll(p, `\`, "/")) {
		return "", fmt.Errorf("the folder %q is not a whole path (/Users/me/work, ~/work, C:\\work)", p)
	}
	return p, nil
}

// cleanFolders checks a rule's folders, each once, none blank.
func cleanFolders(in []string) ([]string, error) {
	var out []string
	for _, p := range in {
		if strings.TrimSpace(p) == "" {
			continue
		}
		c, err := cleanFolder(p)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out, nil
}
