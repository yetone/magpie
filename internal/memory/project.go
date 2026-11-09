package memory

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// ProjectOf is the project a folder belongs to: the git repository it is
// in, keyed by its origin remote (host/owner/repo), so every clone and
// worktree of one repository is one project; a repository with no remote
// by its main folder; a folder outside any repository by itself. nil for
// no folder.
func ProjectOf(dir string) *Project {
	dir = strings.TrimSpace(dir)
	if dir == "" || !filepath.IsAbs(dir) {
		return nil
	}
	dir = filepath.Clean(dir)
	root, gitDir := gitRoot(dir)
	if root == "" {
		return &Project{Key: "dir:" + filepath.ToSlash(dir), Name: filepath.Base(dir), Roots: []string{dir}}
	}
	common := commonDir(gitDir)
	if u := originURL(filepath.Join(common, "config")); u != "" {
		if key := RemoteKey(u); key != "" {
			return &Project{Key: key, Name: lastPart(key), Roots: []string{root}}
		}
	}
	// a repository with no remote is its main checkout's folder: a
	// worktree's common git folder is <main>/.git
	main := root
	if filepath.Base(common) == ".git" {
		main = filepath.Dir(common)
	}
	return &Project{Key: "dir:" + filepath.ToSlash(main), Name: filepath.Base(main), Roots: []string{root}}
}

// gitRoot is the folder holding dir's .git, and the git folder it names
// ("" when dir isn't in a repository). A worktree's .git is a file that
// names its git folder.
func gitRoot(dir string) (root, gitDir string) {
	for d := dir; ; {
		p := filepath.Join(d, ".git")
		if st, err := os.Stat(p); err == nil {
			if st.IsDir() {
				return d, p
			}
			if b, err := os.ReadFile(p); err == nil {
				if g, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:"); ok {
					g = strings.TrimSpace(g)
					if !filepath.IsAbs(g) {
						g = filepath.Join(d, g)
					}
					return d, filepath.Clean(g)
				}
			}
		}
		up := filepath.Dir(d)
		if up == d {
			return "", ""
		}
		d = up
	}
}

// commonDir is the git folder a worktree's shares with its main checkout
// (its commondir file), else gitDir itself.
func commonDir(gitDir string) string {
	b, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return gitDir
	}
	c := strings.TrimSpace(string(b))
	if c == "" {
		return gitDir
	}
	if !filepath.IsAbs(c) {
		c = filepath.Join(gitDir, c)
	}
	return filepath.Clean(c)
}

// originURL is the url of [remote "origin"] in a git config file, else of
// the first remote it has.
func originURL(config string) string {
	f, err := os.Open(config)
	if err != nil {
		return ""
	}
	defer f.Close()
	section, first, origin := "", "", ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			section = strings.ToLower(strings.Join(strings.Fields(strings.Trim(line, "[]")), " "))
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || !strings.HasPrefix(section, "remote ") || strings.ToLower(strings.TrimSpace(k)) != "url" {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"`)
		if first == "" {
			first = v
		}
		if section == `remote "origin"` && origin == "" {
			origin = v
		}
	}
	if origin != "" {
		return origin
	}
	return first
}

// RemoteKey is a git remote's address as a project key, the same for each
// way of writing it: https://github.com/a/b.git, git@github.com:a/b and
// ssh://git@github.com/a/b all are github.com/a/b. "" for a local path.
func RemoteKey(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	if i := strings.Index(u, "://"); i >= 0 {
		scheme := strings.ToLower(u[:i])
		if scheme == "file" {
			return ""
		}
		u = u[i+3:]
		if at := strings.LastIndex(strings.SplitN(u, "/", 2)[0], "@"); at >= 0 {
			u = u[at+1:]
		}
		host, path, _ := strings.Cut(u, "/")
		if h, _, ok := strings.Cut(host, ":"); ok {
			host = h // a port
		}
		u = host + "/" + path
	} else if host, path, ok := strings.Cut(u, ":"); ok && !strings.Contains(host, "/") && len(host) > 1 {
		// scp-like: [user@]host:path (a one-letter host is a Windows drive)
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
		u = host + "/" + strings.TrimPrefix(path, "/")
	} else {
		return ""
	}
	u = strings.TrimSuffix(strings.TrimRight(u, "/"), ".git")
	host, path, _ := strings.Cut(u, "/")
	if host == "" || path == "" {
		return ""
	}
	return strings.ToLower(host) + "/" + path
}

func lastPart(key string) string {
	key = strings.TrimRight(key, "/")
	if i := strings.LastIndex(key, "/"); i >= 0 {
		return key[i+1:]
	}
	return key
}

// ScopeOf is the scope of a project's memories.
func ScopeOf(p *Project) string {
	if p == nil || p.Key == "" {
		return ""
	}
	return "project:" + p.Key
}
