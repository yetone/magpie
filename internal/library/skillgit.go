package library

// Skills from any git repository (sunset on Discord): GitLab, a company's
// own GitLab on its intranet, Gitea, a bare repository on this machine.
// magpie can't ask such a host for a tarball or its commits the way it
// asks GitHub, so it runs the user's own git, which signs in to the host
// the way it does for the user (a credential helper, a token, an SSH key
// in the agent). The address is data: it reaches git as an argument after
// "--", never through a shell, and what git can't be told apart from an
// option, or a transport that runs a command, is refused before git runs.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/proc"
)

// gitPath is where git is, "" when it isn't here; a variable for tests.
var gitPath = func() string { return proc.FindTool("git") }

// gitTimeout is how long one git command may take before it is stopped:
// a host that never answers, or a sign-in no one can see, ends there.
var gitTimeout = 3 * time.Minute

var (
	// remoteHelper is git's <transport>::<address>, which hands the
	// address to a helper program (ext:: runs a command)
	remoteHelper = lazyre.New(`^[A-Za-z][A-Za-z0-9+.-]*::`)
	// scpLike is git's user@host:path for SSH
	scpLike = lazyre.New(`^(?:([\w.-]+)@)?([\w.-]+):(.+)$`)
	gitRef  = lazyre.New(`^[\w./+@-]+$`)
	hexRef  = lazyre.New(`^[0-9a-fA-F]{7,40}$`)
)

// gitSchemes are the URL schemes a git source may have. file:// is a
// repository on this machine or a mounted share, which the user named.
var gitSchemes = map[string]bool{"https": true, "http": true, "ssh": true, "git": true, "git+ssh": true, "ssh+git": true, "file": true}

// gitSource reads a git repository out of what the user typed: a URL
// (https://host/group/sub/repo(.git), ssh://, git://, file://), SSH's
// git@host:group/repo.git, or host/group/repo. A GitLab page's
// /-/tree/<ref>/<path> (or /-/blob/…/SKILL.md), and Gitea's
// /src/branch/<ref>/<path>, name a branch and a folder in it; the ref is
// the first part after them, so a branch with a / in its name is read
// as a ref and a folder. ok is false for what isn't meant as one; an
// error is one that is, refused.
func gitSource(s string) (src Source, ok bool, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Source{}, false, nil
	}
	if strings.HasPrefix(s, "-") {
		return Source{}, true, fmt.Errorf("a repository's address can't start with -")
	}
	if remoteHelper.MatchString(s) {
		return Source{}, true, fmt.Errorf("magpie doesn't install from a git address with a transport (%s): give the repository's https or SSH address", s[:strings.Index(s, "::")+2])
	}
	if strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return Source{}, false, nil
	}
	repo, rest := s, ""
	for _, sep := range []string{"/-/tree/", "/-/blob/", "/src/branch/", "/src/tag/", "/src/commit/"} {
		if i := strings.Index(s, sep); i > 0 {
			repo, rest = s[:i], s[i+len(sep):]
			break
		}
	}
	repo = strings.TrimSuffix(repo, "/")
	if strings.HasSuffix(repo, "/-") { // https://host/group/repo/-/ alone
		repo = strings.TrimSuffix(repo, "/-")
	}
	if !strings.Contains(repo, "://") {
		// user@host:path, or host.name:path; a Windows drive (C:) is a
		// folder, never a host
		if m := scpLike.FindStringSubmatch(repo); m != nil && len(m[2]) > 1 && (m[1] != "" || strings.Contains(m[2], ".")) {
			if strings.HasPrefix(m[3], "-") || strings.HasPrefix(m[2], "-") {
				return Source{}, true, fmt.Errorf("a repository's address can't start with -")
			}
		} else if first, _, _ := strings.Cut(repo, "/"); strings.Contains(first, ".") && strings.Count(repo, "/") >= 2 {
			repo = "https://" + repo // gitlab.example.com/group/repo
		} else {
			return Source{}, false, nil
		}
	}
	if strings.Contains(repo, "://") {
		u, err := url.Parse(repo)
		if err != nil || u.Scheme == "" {
			return Source{}, false, nil
		}
		if !gitSchemes[strings.ToLower(u.Scheme)] {
			return Source{}, true, fmt.Errorf("magpie doesn't install from a %s:// address: give the repository's https or SSH address", u.Scheme)
		}
		if _, set := u.User.Password(); set {
			// it would be kept in library.json, shown on the page and
			// carried to a backup
			return Source{}, true, fmt.Errorf("leave the password or token out of the address: magpie runs your own git, which signs in with its credential helper or your SSH key")
		}
		if strings.ToLower(u.Scheme) != "file" && (u.Host == "" || strings.HasPrefix(u.Host, "-")) {
			return Source{}, true, fmt.Errorf("%s names no host", repo)
		}
		if strings.Trim(u.Path, "/") == "" {
			return Source{}, true, fmt.Errorf("%s names no repository", repo)
		}
	}
	src = Source{Kind: "git", Repo: repo}
	if rest != "" {
		ref, path, _ := strings.Cut(strings.Trim(rest, "/"), "/")
		if ref != "HEAD" {
			src.Ref = ref
		}
		if q := strings.IndexAny(path, "?#"); q >= 0 {
			path = path[:q]
		}
		src.Path = strings.Trim(strings.TrimSuffix(strings.TrimSuffix(path, "SKILL.md"), "/"), "/")
		if src.Ref != "" && (!gitRef.MatchString(src.Ref) || strings.HasPrefix(src.Ref, "-") || strings.Contains(src.Ref, "..")) {
			return Source{}, true, fmt.Errorf("%q isn't a branch or tag git can fetch", src.Ref)
		}
		if strings.Contains("/"+src.Path+"/", "/../") {
			return Source{}, true, fmt.Errorf("%q isn't a folder in the repository", src.Path)
		}
	}
	return src, true, nil
}

// gitURL is a git source as the user can type it again: the repository,
// and its ref and folder as a GitLab page names them. An https address
// loses its .git there, so it opens the repository's page; GitLab and
// Gitea fetch it without one too.
func gitURL(s *Source) string {
	if s.Ref == "" && s.Path == "" {
		return s.Repo
	}
	ref := s.Ref
	if ref == "" {
		ref = "HEAD"
	}
	u := s.Repo
	if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
		u = strings.TrimSuffix(u, ".git") // the page, not the clone
	}
	u += "/-/tree/" + ref
	if s.Path != "" {
		u += "/" + s.Path
	}
	return u
}

// sameGitRepo is whether two addresses are one repository: by host and
// path, however it is reached (https, SSH, with .git or without).
func sameGitRepo(a, b string) bool {
	if strings.EqualFold(strings.TrimSuffix(a, ".git"), strings.TrimSuffix(b, ".git")) {
		return true
	}
	ra, rb := remoteRepo(a), remoteRepo(b)
	return ra != "" && strings.EqualFold(ra, rb)
}

// gitHeads is the commit each git download is at, by its folder.
var gitHeads = struct {
	sync.Mutex
	m map[string]string
}{m: map[string]string{}}

func gitHeadOf(dir string) string {
	gitHeads.Lock()
	defer gitHeads.Unlock()
	return gitHeads.m[dir]
}

func noteGitHead(dir, sha string) {
	gitHeads.Lock()
	defer gitHeads.Unlock()
	for d := range gitHeads.m {
		if _, err := os.Stat(d); err != nil {
			delete(gitHeads.m, d) // a download removed since
		}
	}
	gitHeads.m[dir] = sha
}

// errNoGit is git missing from this computer.
func errNoGit() error {
	return errors.New("installing from a git repository runs git, and git isn't installed here (or isn't on PATH): install git and try again")
}

// gitEnv is the environment git runs in: the user's, so their credential
// helper, SSH agent and config work, with every question asked on a
// terminal turned off (no one sees it, and it would wait for ever), and
// git's messages in English, as magpie reads them.
func gitEnv(git string) []string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "LANGUAGE=C")
	if os.Getenv("GIT_SSH_COMMAND") == "" && os.Getenv("GIT_SSH") == "" {
		// ssh asks for a passphrase or a new host's key on the terminal:
		// BatchMode refuses instead. A core.sshCommand of the user's own
		// (a key for the intranet) is kept, which GIT_SSH_COMMAND would
		// override.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := proc.CommandContext(ctx, git, "config", "--global", "--get", "core.sshCommand")
		cmd.Env = env
		if out, _ := cmd.Output(); strings.TrimSpace(string(out)) == "" {
			env = append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
		}
	}
	return env
}

// runGit runs git with args in dir, and gives back its output, or an
// error saying plainly what went wrong for src.
func runGit(ctx context.Context, git string, env []string, dir string, src Source, args ...string) ([]byte, error) {
	cmd := proc.CommandContext(ctx, git, append([]string{"-c", "protocol.ext.allow=never", "-c", "core.symlinks=false"}, args...)...)
	cmd.Dir, cmd.Env = dir, env
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("git took over %s to fetch %s and was stopped", gitTimeout, src.Repo)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return nil, fmt.Errorf("couldn't run git: %w", err)
	}
	return nil, gitError(src, stderr.String())
}

// gitError says what git's message means for the user.
func gitError(src Source, msg string) error {
	low := strings.ToLower(msg)
	said := gitSaid(msg)
	host := gitHost(src.Repo)
	switch {
	case strings.Contains(low, "xcrun: error") || strings.Contains(low, "invalid active developer path") || strings.Contains(low, "no developer tools were found"):
		return errors.New("installing from a git repository runs git, and git isn't installed here: install Apple's command line tools (xcode-select --install) and try again")
	case strings.Contains(low, "host key verification failed"):
		return fmt.Errorf("SSH doesn't know %s's host key yet: connect to it once with ssh or git in a terminal to accept it, then try again (git said: %s)", host, said)
	case strings.Contains(low, "could not read username") || strings.Contains(low, "could not read password") ||
		strings.Contains(low, "terminal prompts disabled") || strings.Contains(low, "authentication failed") ||
		strings.Contains(low, "permission denied") || strings.Contains(low, "access denied") ||
		strings.Contains(low, "the requested url returned error: 401") || strings.Contains(low, "the requested url returned error: 403"):
		return fmt.Errorf("git couldn't sign in to %s. magpie runs your own git, so sign in the way git does there (a credential helper or an SSH key that can read the repository), check `git clone %s` works in a terminal, and try again (git said: %s)", host, src.Repo, said)
	case strings.Contains(low, "couldn't find remote ref") || strings.Contains(low, "unknown revision"):
		return fmt.Errorf("%s has no branch or tag %s", src.Repo, src.Ref)
	case strings.Contains(low, "repository not found") || strings.Contains(low, "does not appear to be a git repository") ||
		strings.Contains(low, "not found") && strings.Contains(low, "repository") || strings.Contains(low, "returned error: 404"):
		return fmt.Errorf("there's no git repository at %s, or it can't be read without signing in (git said: %s)", src.Repo, said)
	case strings.Contains(low, "could not resolve host") || strings.Contains(low, "connection refused") ||
		strings.Contains(low, "connection timed out") || strings.Contains(low, "network is unreachable") || strings.Contains(low, "couldn't connect"):
		return fmt.Errorf("git couldn't reach %s (git said: %s)", host, said)
	}
	return fmt.Errorf("git couldn't fetch %s: %s", src.Repo, said)
}

// gitSaid is git's message, its last few lines, for an error.
func gitSaid(s string) string {
	var keep []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "hint:") {
			keep = append(keep, l)
		}
	}
	if len(keep) > 3 {
		keep = keep[len(keep)-3:]
	}
	out := strings.Join(keep, " / ")
	if r := []rune(out); len(r) > 400 {
		out = string(r[:399]) + "…"
	}
	if out == "" {
		out = "nothing"
	}
	return out
}

// gitHost is the host of a repository's address, for a message.
func gitHost(repo string) string {
	if u, err := url.Parse(repo); err == nil && u.Scheme != "" {
		if u.Host == "" {
			return repo
		}
		return u.Hostname()
	}
	if m := scpLike.FindStringSubmatch(repo); m != nil {
		return m[2]
	}
	return repo
}

// fetchGit fetches the repository's ref (its default branch when "")
// with the user's git, one commit deep, into a new folder, and gives it
// back without its .git: what GitHub's tarball would have. Links are
// checked out as small files holding where they point, as a tarball's
// are left out: never followed out of the folder.
func fetchGit(src Source) (string, error) {
	git := gitPath()
	if git == "" {
		return "", errNoGit()
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	env := gitEnv(git)
	tmp, err := os.MkdirTemp("", skillTemp)
	if err != nil {
		return "", err
	}
	fail := func(err error) (string, error) {
		os.RemoveAll(tmp)
		return "", err
	}
	if _, err := runGit(ctx, git, env, tmp, src, "init", "-q"); err != nil {
		return fail(err)
	}
	ref := src.Ref
	if ref == "" {
		ref = "HEAD"
	}
	if _, err := runGit(ctx, git, env, tmp, src, "fetch", "-q", "--depth", "1", "--no-tags", "--", src.Repo, ref); err != nil {
		// a host that serves git over plain ("dumb") HTTP can't fetch one
		// commit deep: the whole history then
		if !strings.Contains(strings.ToLower(err.Error()), "shallow") {
			return fail(err)
		}
		if _, err := runGit(ctx, git, env, tmp, src, "fetch", "-q", "--no-tags", "--", src.Repo, ref); err != nil {
			return fail(err)
		}
	}
	if _, err := runGit(ctx, git, env, tmp, src, "-c", "advice.detachedHead=false", "checkout", "-q", "FETCH_HEAD"); err != nil {
		return fail(err)
	}
	out, err := runGit(ctx, git, env, tmp, src, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return fail(err)
	}
	if err := os.RemoveAll(filepath.Join(tmp, ".git")); err != nil {
		return fail(err)
	}
	if size := dirSize(tmp); size > maxFetch {
		return fail(fmt.Errorf("%s is %s, over the %s magpie fetches", src.Repo, mb(size), mb(maxFetch)))
	}
	noteGitHead(tmp, strings.TrimSpace(string(out)))
	return tmp, nil
}

func dirSize(dir string) int64 {
	var n int64
	filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			n += fi.Size()
		}
		return nil
	})
	return n
}

// gitRemoteHead asks the repository, with git ls-remote, which commit its
// ref (its default branch when "") is at now. A ref that is a commit is
// that commit.
func gitRemoteHead(src Source) (string, error) {
	if hexRef.MatchString(src.Ref) {
		return strings.ToLower(src.Ref), nil
	}
	git := gitPath()
	if git == "" {
		return "", errNoGit()
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	args := []string{"ls-remote", "--", src.Repo}
	if src.Ref == "" {
		args = append(args, "HEAD")
	} else {
		args = append(args, "refs/heads/"+src.Ref, "refs/tags/"+src.Ref, "refs/tags/"+src.Ref+"^{}")
	}
	out, err := runGit(ctx, git, gitEnv(git), os.TempDir(), src, args...)
	if err != nil {
		return "", err
	}
	refs := map[string]string{}
	for _, l := range strings.Split(string(out), "\n") {
		if sha, name, ok := strings.Cut(strings.TrimSpace(l), "\t"); ok {
			refs[name] = sha
		}
	}
	for _, k := range []string{"HEAD", "refs/tags/" + src.Ref + "^{}", "refs/heads/" + src.Ref, "refs/tags/" + src.Ref} {
		if sha := refs[k]; hexRef.MatchString(sha) {
			return sha, nil
		}
	}
	if src.Ref == "" {
		return "", fmt.Errorf("%s names no default branch", src.Repo)
	}
	return "", fmt.Errorf("%s has no branch or tag %s any more", src.Repo, src.Ref)
}

// checkGitSkill checks a skill from a git repository: git ls-remote says
// which commit its ref is at; one the skill was fetched at is current,
// and for another the repository is fetched and the skill's folder
// compared with the library's, since a commit elsewhere in the repository
// doesn't change it. When its files are the repository's, it gives back
// the commit they're at and their hash, for the library to keep.
func checkGitSkill(s *Skill, f *fetcher, asked map[string]string) (SkillCheck, string, string) {
	src := *s.Source
	c := SkillCheck{Name: s.Name, Status: "unknown"}
	head, ok := asked[src.Ref]
	if !ok {
		var err error
		if head, err = gitRemoteHead(src); err != nil {
			c.Error = err.Error()
			return c, "", ""
		}
		asked[src.Ref] = head
	}
	c.Commit = head
	if s.Commit != "" && strings.EqualFold(s.Commit, head) {
		c.Status = "current"
		return c, "", ""
	}
	tmp, err := f.fetch(src)
	if err != nil {
		c.Error = err.Error()
		return c, "", ""
	}
	from := filepath.Join(tmp, filepath.FromSlash(src.Path))
	if _, ok := readMeta(from); !ok {
		if goneFrom(tmp, src.Path) {
			return goneCheck(&GoneError{Name: s.Name, Source: src.Repo}), "", ""
		}
		c.Error = fmt.Sprintf("%s has no SKILL.md at %s any more", src.Repo, src.Path)
		return c, "", ""
	}
	have := s.Hash
	if have == "" {
		have = hashDir(skillDir(s.Name))
	}
	if theirs := s.fetchedHash(from); theirs == "" || theirs != have {
		c.Status = "update"
		return c, "", ""
	}
	c.Status = "current"
	if h := gitHeadOf(tmp); h != "" {
		c.Commit = h
	}
	return c, c.Commit, have
}

// isGit is whether what the user typed is a git repository's address
// magpie fetches from.
func isGit(s string) bool {
	_, ok, err := gitSource(s)
	return ok && err == nil
}
