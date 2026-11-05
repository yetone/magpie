package gateway

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// claudeTrusts is whether Claude Code, under the account it is signed in
// to, runs in folder work without asking whether to trust it, by the
// answers the user gave it (provider.ClaudeTrustedProjects); err when
// those can't be read, which is unknown. Either path of work counts, as
// written and with its links followed: Claude Code keys a folder by the
// one its working directory has, and a Mac's getcwd says /private/var/…
// for /var/… (claudeTrustKeys).
func claudeTrusts(work string) (bool, error) {
	projects, err := provider.ClaudeTrustedProjects()
	if err != nil {
		return false, err
	}
	for _, w := range []string{work, evalSymlinks(work)} {
		if claudeTrustedBy(projects, filepath.Clean(w)) {
			return true, nil
		}
	}
	return false, nil
}

// claudeTrustedBy is Claude Code's own trust check (2.1.316, read from its
// code), for a run in dir: the folder is trusted when the user trusted
// the main checkout of the repository dir is in (its own root, or for a
// worktree the checkout it was added from), dir itself when it is in
// none, or dir or a folder above it up to the root of the repository
// it is in, never past it: a trusted repository doesn't trust a
// repository inside it. Outside a repository every folder above counts.
// Accepting the question records the first of these (claudeTrustKeys'
// form), which is why a worktree of a trusted checkout isn't asked about.
func claudeTrustedBy(projects map[string]bool, dir string) bool {
	root := claudeGitRoot(dir)
	key := dir
	if root != "" {
		key = claudeMainCheckout(root)
	}
	if projects[claudeTrustKey(key)] {
		return true
	}
	for d := dir; ; {
		if projects[claudeTrustKey(d)] {
			return true
		}
		if d == root {
			return false
		}
		up := filepath.Dir(d)
		if up == d {
			return false
		}
		d = up
	}
}

// claudeGitRoot is the folder nearest dir, dir itself first, that holds a
// .git, a folder or a file (a worktree's, a submodule's) or a link to
// one, as Claude Code's findGitRoot takes it; "" when none does.
func claudeGitRoot(dir string) string {
	for d := dir; ; {
		if fi, err := os.Stat(filepath.Join(d, ".git")); err == nil && (fi.IsDir() || fi.Mode().IsRegular()) {
			return d
		}
		up := filepath.Dir(d)
		if up == d {
			return ""
		}
		d = up
	}
}

// claudeMainCheckout is the checkout a worktree at root was added from,
// as Claude Code finds it: root's .git file names the worktree's git
// folder (<common>/worktrees/<name>), whose commondir names the
// repository's own and whose gitdir names root's .git back; the checkout
// is the folder holding that common .git, or the common folder itself
// for a bare repository. Anything else, a repository's own root and a
// submodule's included, is root.
func claudeMainCheckout(root string) string {
	b, err := os.ReadFile(filepath.Join(root, ".git"))
	if err != nil {
		return root // a folder, or unreadable
	}
	line := strings.TrimSpace(string(b))
	if !strings.HasPrefix(line, "gitdir:") {
		return root
	}
	gitDir := resolveFrom(root, strings.TrimSpace(strings.TrimPrefix(line, "gitdir:")))
	common, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return root
	}
	commonDir := resolveFrom(gitDir, strings.TrimSpace(string(common)))
	if filepath.Dir(gitDir) != filepath.Join(commonDir, "worktrees") {
		return root
	}
	back, err := os.ReadFile(filepath.Join(gitDir, "gitdir"))
	if err != nil {
		return root
	}
	if evalSymlinks(resolveFrom(gitDir, strings.TrimSpace(string(back)))) != filepath.Join(evalSymlinks(root), ".git") {
		return root
	}
	if filepath.Base(commonDir) != ".git" {
		if _, err := os.Lstat(filepath.Join(commonDir, ".git")); err == nil {
			return root
		}
		return commonDir
	}
	return filepath.Dir(commonDir)
}

// resolveFrom is path p read from base, as Node's path.resolve(base, p).
func resolveFrom(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
}
