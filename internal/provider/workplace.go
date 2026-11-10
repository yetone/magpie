package provider

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/yetone/magpie/internal/lazyre"
)

// gitSaidRe is whether the agent's folder is in a git repository, as the
// agent says it beside the folder: Claude Code's " - Is a git repository:
// false" (2.1.296; "Is directory a git repo: Yes" before), OpenCode's
// "  Is directory a git repo: yes".
var gitSaidRe = lazyre.New(`(?mi)^\s*(?:- )?Is (?:a git repository|directory a git repo): (true|false|yes|no)\s*$`)

// GitSaid is the last "yes" or "no" an agent's text says of its folder
// being in a git repository, "" when it says neither.
func GitSaid(text string) string {
	m := gitSaidRe.FindAllStringSubmatch(text, -1)
	if len(m) == 0 {
		return ""
	}
	switch strings.ToLower(m[len(m)-1][1]) {
	case "true", "yes":
		return "yes"
	}
	return "no"
}

// GitOnDisk says whether dir, a folder on this computer, is in a git
// repository as git finds one from there: the nearest .git in it or in a
// folder above it that git takes (GitAt). A .git folder git doesn't take,
// an empty one say, is passed over as git passes it over, so an empty
// /tmp/.git doesn't make every folder under /tmp a repository (#3675).
// "" when dir isn't a folder here (an agent on another computer, through
// a shared magpie) or a .git on the way can't be read (another user's,
// through a shared magpie): unknown, never "no".
func GitOnDisk(dir string) string {
	if !filepath.IsAbs(dir) {
		return ""
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return ""
	}
	for d := filepath.Clean(dir); ; {
		git, _, err := GitAt(filepath.Join(d, ".git"))
		if err != nil {
			return ""
		}
		if git != "" {
			return git
		}
		up := filepath.Dir(d)
		if up == d {
			return "no"
		}
		d = up
	}
}

// GitAt is what the .git at path is to git's own search for a repository
// (repo_discovery_find_dir in git 2.56's setup.c): "yes" for a folder git
// takes for a repository (gitDir), or a file naming one ("gitdir: …", as
// a linked worktree's or a submodule's is), with the folder that holds
// the repository's config, objects and refs; "no" for any other file,
// where git stops with "not a git repository"; "" for nothing to go by,
// no .git or a folder git passes over. An error is a .git it couldn't
// read.
func GitAt(path string) (git, common string, err error) {
	fi, err := os.Stat(path)
	if absent(err) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	if fi.IsDir() {
		if common, err = gitDir(path); common == "" {
			return "", "", err
		}
		return "yes", common, nil
	}
	if !fi.Mode().IsRegular() || fi.Size() > 1<<20 {
		return "no", "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	to, ok := strings.CutPrefix(strings.TrimRight(string(b), "\r\n"), "gitdir: ")
	if !ok || to == "" {
		return "no", "", nil
	}
	if !filepath.IsAbs(to) {
		to = filepath.Join(filepath.Dir(path), to)
	}
	switch common, err = gitDir(to); {
	case err != nil:
		return "", "", err
	case common == "":
		return "no", "", nil
	}
	return "yes", common, nil
}

// gitDir is the folder that holds the repository dir is the git directory
// of, as git takes one (is_git_directory): a HEAD git takes (gitHead), and
// objects and refs folders in the folder its commondir names when it has
// one (a linked worktree's), else in dir. "" when git doesn't take dir for
// one.
func gitDir(dir string) (string, error) {
	if ok, err := gitHead(filepath.Join(dir, "HEAD")); !ok {
		return "", err
	}
	common := dir
	switch b, err := os.ReadFile(filepath.Join(dir, "commondir")); {
	case err == nil:
		common = strings.TrimRight(string(b), "\r\n")
		if !filepath.IsAbs(common) {
			common = filepath.Join(dir, common)
		}
	case !absent(err):
		return "", err
	}
	for _, sub := range []string{"objects", "refs"} {
		fi, err := os.Stat(filepath.Join(common, sub))
		if absent(err) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if !fi.IsDir() {
			return "", nil
		}
	}
	return common, nil
}

// gitHead is whether the HEAD at path is one git takes
// (validate_headref): a link into refs/, or a file that starts with
// "ref: refs/" or with a commit's id.
func gitHead(path string) (bool, error) {
	fi, err := os.Lstat(path)
	if absent(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		to, err := os.Readlink(path)
		return strings.HasPrefix(to, "refs/"), err
	}
	if !fi.Mode().IsRegular() {
		return false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 255))
	if err != nil {
		return false, err
	}
	if ref, ok := bytes.CutPrefix(b, []byte("ref:")); ok && bytes.HasPrefix(bytes.TrimLeft(ref, " \t\n\v\f\r"), []byte("refs/")) {
		return true, nil
	}
	if len(b) < 40 {
		return false, nil
	}
	_, err = hex.DecodeString(string(b[:40]))
	return err == nil, nil
}

// absent is whether err says a path isn't there, rather than that it
// couldn't be read.
func absent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}
