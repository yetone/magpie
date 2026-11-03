package agent

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
)

// LodyScheme opens Lody's local-machine provider confirmation dialog.
const LodyScheme = "lody://provider/import?"

// LodyLink offers all supported runtimes. Lody accepts only loopback HTTP
// and its public attribution token, never a LAN key or a vendor credential.
func LodyLink(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.User != nil || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return ""
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1":
	default:
		return ""
	}
	if strings.HasSuffix(u.Host, ":") || (u.Hostname() == "::1" && !strings.HasPrefix(u.Host, "[::1]")) {
		return ""
	}
	host := u.Hostname()
	if host == "::1" {
		host = "[::1]"
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return ""
		}
		if n != 80 {
			host += ":" + strconv.Itoa(n)
		}
	}
	base = "http://" + host
	type endpoint struct {
		Protocol  string   `json:"protocol"`
		BaseURL   string   `json:"baseUrl"`
		Targets   []string `json:"targets"`
		ModelsURL string   `json:"modelsUrl"`
	}
	payload := struct {
		Kind      string            `json:"kind"`
		ID        string            `json:"id"`
		Name      string            `json:"name"`
		Auth      map[string]string `json:"auth"`
		Endpoints []endpoint        `json:"endpoints"`
	}{
		Kind: "custom", ID: "magpie", Name: "Magpie",
		Auth: map[string]string{"method": "apiKey", "apiKey": gateway.TokenFor("lody")},
		Endpoints: []endpoint{
			{"anthropic-messages", base, []string{"claude-code"}, base + "/v1/models"},
			{"openai-responses", base + "/v1", []string{"codex"}, base + "/v1/models"},
			{"openai-chat", base + "/v1", []string{"pi", "dsh", "kimi-code", "grok", "bub"}, base + "/v1/models"},
		},
	}
	b, _ := json.Marshal(payload)
	return LodyScheme + "v=1&data=" + base64.RawURLEncoding.EncodeToString(b)
}

// lodyMarks are entries Lody makes in its home folder (~/.lody, or
// ~/.lody-oss for its OSS build) once it has run: a folder of that name
// another tool made, with none of them in it, isn't Lody.
var lodyMarks = []string{"workspace-catalog.json", "loro-repo", "agent-binaries", "code-collab-v2"}

// lodyNames are the names Lody's builds go by: its app bundle on a Mac and
// its data folder under the OS's config folder (Electron's userData:
// ~/Library/Application Support/Lody, %APPDATA%\Lody, ~/.config/Lody).
var lodyNames = []string{"Lody", "Lody OSS", "Lody Nightly"}

// lodyBundle reports whether an app bundle in folders is one of Lody's:
// ai.lody.desktop (or another ai.lody. build), or a plist that isn't XML,
// which says nothing either way. A Lody.app of another vendor's isn't.
func lodyBundle(folders []string) bool {
	for _, dir := range folders {
		for _, name := range lodyNames {
			b, err := os.ReadFile(filepath.Join(dir, name+".app", "Contents", "Info.plist"))
			if err != nil {
				continue
			}
			if id := plistIDRe.FindSubmatch(b); id == nil || strings.HasPrefix(strings.TrimSpace(string(id[1])), "ai.lody.") {
				return true
			}
		}
	}
	return false
}

func lody(home string) *Agent {
	homes := []string{filepath.Join(home, ".lody"), filepath.Join(home, ".lody-oss")}
	var dirs []string
	if cfg, err := os.UserConfigDir(); err == nil {
		for _, name := range lodyNames {
			dirs = append(dirs, filepath.Join(cfg, name))
		}
	}
	return &Agent{
		ID: "lody", Name: "Lody", Icon: "lody", UA: []string{"lody"},
		detect: func() bool {
			for _, dir := range homes {
				for _, m := range lodyMarks {
					if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
						return true
					}
				}
			}
			for _, dir := range dirs {
				if isDir(dir) {
					return true
				}
			}
			return runtime.GOOS == "darwin" && lodyBundle(macAppFolders(home))
		},
		Import: func() string { return LodyLink(gateway.URL()) },
		// There is no public status API. Never infer import success from a
		// click, generated runtime files, or Lody's private document store.
	}
}

// lodyEntries are the desktop entries Lody's Linux builds go by, stable
// first: ai.lody.desktop (Lody, from its .deb in /usr/share/applications,
// or written by its AppImage to $XDG_DATA_HOME/applications at each
// start), ai.lody.desktop.nightly (Lody Nightly) and dev.loro.lody.oss
// (Lody OSS).
var lodyEntries = []string{"ai.lody.desktop.desktop", "ai.lody.desktop.nightly.desktop", "dev.loro.lody.oss.desktop"}

// lodyGOOS is the system OpenLody hands the link over on; tests stand in
// another.
var lodyGOOS = runtime.GOOS

// startLody starts Lody with the link and reaps it once it ends; tests
// stand in for it.
var startLody = func(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() //nolint:errcheck
	return nil
}

// OpenLody hands Lody's import link to the Lody installed here. On Linux
// it runs that Lody with the link itself, as its desktop entry says, which
// is what xdg-open runs when lody:// is registered: Lody's AppImage makes
// itself the default only for its own channel's scheme (ai.lody.stable),
// and leaves lody:// to the desktop database, which nothing builds without
// update-desktop-database, so xdg-open, gio and a browser turn the link
// away though Lody is running (Atlas, 10-25: "failed to open uri"). A
// second Lody passes the link to the running one and quits. With no entry
// of Lody's, and on macOS and Windows, where Lody's bundle and installer
// claim lody:// themselves, open opens it.
func OpenLody(link string, open func(string) error) error {
	if argv := lodyCommand(link); argv != nil {
		if err := startLody(proc.Command(argv[0], argv[1:]...)); err == nil {
			return nil
		}
	}
	return open(link)
}

// lodyCommand is the command Lody's desktop entry runs for link, nil with
// no entry whose program is there.
func lodyCommand(link string) []string {
	if lodyGOOS == "darwin" || lodyGOOS == "windows" {
		return nil
	}
	dirs := xdgDataDirs()
	for _, name := range lodyEntries {
		// the first folder with an entry of that name has the one that
		// counts, as for xdg-open
		for _, dir := range dirs {
			if dir == "" {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, "applications", name))
			if err != nil {
				continue
			}
			if argv := desktopExec(string(b), link); argv != nil {
				return argv
			}
			break
		}
	}
	return nil
}

// desktopExec is the command a desktop entry's Exec runs for one URL, as
// the Desktop Entry Specification reads it: arguments in double quotes with
// backslash escapes, %u and %U the URL, %% a %, the other field codes
// dropped, and the URL put last when Exec takes none. It is nil with no
// Exec, or with no program there to run.
func desktopExec(entry, link string) []string {
	argv := desktopArgs(entry, link)
	if len(argv) == 0 {
		return nil
	}
	if !filepath.IsAbs(argv[0]) {
		p, err := proc.LookPath(argv[0])
		if err != nil {
			return nil
		}
		argv[0] = p
	} else if st, err := os.Stat(argv[0]); err != nil || st.IsDir() || st.Mode()&0111 == 0 {
		// an AppImage moved or deleted since Lody wrote its entry
		return nil
	}
	return argv
}

// xdgDataDirs are the folders whose applications/ hold desktop entries,
// the user's first, as xdg-open reads them.
func xdgDataDirs() []string {
	data := appdir.Getenv("XDG_DATA_HOME")
	if data == "" {
		if home, err := os.UserHomeDir(); err == nil {
			data = filepath.Join(home, ".local", "share")
		}
	}
	dirs := []string{data}
	sys := os.Getenv("XDG_DATA_DIRS")
	if sys == "" {
		sys = "/usr/local/share:/usr/share"
	}
	for _, d := range filepath.SplitList(sys) {
		if filepath.IsAbs(d) {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// desktopArgs is the command line a desktop entry's Exec gives for link,
// its program not looked for yet; nil with no Exec.
func desktopArgs(entry, link string) []string {
	line := ""
	in := false
	for _, l := range strings.Split(entry, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "[") {
			in = l == "[Desktop Entry]"
			continue
		}
		if k, v, ok := strings.Cut(l, "="); in && ok && strings.TrimSpace(k) == "Exec" {
			line = strings.TrimSpace(v)
			break
		}
	}
	var argv []string
	var arg strings.Builder
	has, quoted, gaveURL := false, false, false
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case quoted && c == '\\' && i+1 < len(line):
			i++
			arg.WriteByte(line[i])
		case c == '"':
			quoted, has = !quoted, true
		case !quoted && (c == ' ' || c == '\t'):
			if has {
				argv = append(argv, arg.String())
			}
			arg.Reset()
			has = false
		case c == '%' && i+1 < len(line):
			i++
			switch line[i] {
			case '%':
				arg.WriteByte('%')
				has = true
			case 'u', 'U':
				arg.WriteString(link)
				has, gaveURL = true, true
			}
		default:
			arg.WriteByte(c)
			has = true
		}
	}
	if has {
		argv = append(argv, arg.String())
	}
	if len(argv) == 0 {
		return nil
	}
	if !gaveURL {
		argv = append(argv, link)
	}
	return argv
}

// desktopHandler is the program the desktop entry name runs, looked for
// in dirs' applications/ as xdg-open does. xdg-mime names an entry that
// was written to mimeapps.list, which may never have been made, or whose
// program has moved since: Electron's setAsDefaultProtocolClient writes
// the name, and an AppImage that wasn't integrated has no entry. xdg-open
// then falls back to the browser, which opened a blank page instead of
// Cindy on Omarchy (geddy, 11-03). With no entry, exe is where the user's
// own would be, which isn't there; with its program gone, that program.
// An entry that can't be read is unknown.
func desktopHandler(name string, dirs []string) (exe string, found bool, err error) {
	if !strings.HasSuffix(name, ".desktop") || strings.ContainsAny(name, "/\\") {
		return "", true, nil
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		for _, rel := range entryPaths(name) {
			path := filepath.Join(dir, "applications", rel)
			b, rerr := os.ReadFile(path)
			if errors.Is(rerr, fs.ErrNotExist) {
				continue
			}
			if rerr != nil {
				return "", false, rerr
			}
			argv := desktopArgs(string(b), "")
			if len(argv) == 0 {
				// no Exec: xdg-open passes it by, as with none
				return path, true, nil
			}
			if filepath.IsAbs(argv[0]) {
				return argv[0], true, nil
			}
			if p, lerr := proc.LookPath(argv[0]); lerr == nil {
				return p, true, nil
			}
			return "", false, nil
		}
	}
	if len(dirs) == 0 || dirs[0] == "" {
		return "", false, nil
	}
	return filepath.Join(dirs[0], "applications", name), true, nil
}

// entryPaths are where under applications/ an entry's id can be: the
// id itself, or with its dashes standing for subfolders (kde-foo.desktop
// as kde/foo.desktop), as the Desktop Entry Specification names them.
func entryPaths(id string) []string {
	paths := []string{id}
	for i := 0; i < len(id); i++ {
		if id[i] == '-' {
			paths = append(paths, filepath.Join(strings.ReplaceAll(id[:i], "-", "/"), id[i+1:]))
		}
	}
	return paths
}
