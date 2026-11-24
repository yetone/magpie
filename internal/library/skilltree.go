package library

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	slash "path" // a repository's paths, always with /
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/source"
)

// ---- fetching only a repository's skills ----------------------------------
//
// A repository that is mostly something else, an app with its skills in a
// folder beside it (vectorize-io/hindsight: 250 MB, its skills a few KB,
// #1941), is more than the whole-repository download takes. Its skills'
// folders alone are fetched instead: GitHub's API lists the files (one
// request), and each file in a folder with a SKILL.md is read from
// raw.githubusercontent.com, checked against the hash the list gives.

// maxFetch is the most a repository's files, or its skills' files, may
// come to on disk; a variable for tests.
var maxFetch int64 = 200 << 20

var errTooBig = errors.New("the repository is too big to install skills from")

// tooBig is the repositories found too big this run, or too slow to
// download whole within wholeTimeout: fetched again (an
// install after its probe, an update check), they go straight to their
// skills' folders rather than download the whole of it again first.
var tooBig = &repoSet{m: map[string]bool{}}

type repoSet struct {
	sync.Mutex
	m map[string]bool
}

func (t *repoSet) key(src Source) string { return strings.ToLower(src.Repo) + "@" + src.Ref }

func (t *repoSet) has(src Source) bool {
	t.Lock()
	defer t.Unlock()
	return t.m[t.key(src)]
}

func (t *repoSet) add(src Source) {
	t.Lock()
	defer t.Unlock()
	t.m[t.key(src)] = true
}

// rawURL is where a file of a repository at a ref is read from; a variable
// for tests.
var rawURL = func(repo, ref, path string) string {
	if ref == "" {
		ref = "HEAD"
	}
	parts := strings.Split(path, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return "https://raw.githubusercontent.com/" + repo + "/" + url.PathEscape(ref) + "/" + strings.Join(parts, "/")
}

// fetchSkillFiles fetches, into a new folder laid out as the repository
// is, every folder of it with a SKILL.md under src.Path, whatever is in
// it, and the other folders there empty: what candidates, origin and an
// update read of a fetched repository. With name, only the skills whose
// folder is called that are fetched, if any is: the market installs one
// skill by its name, and sickn33/agentic-awesome-skills' 8,589 skills
// come to 287 MB, over maxFetch (#2794). A skill's folder is its name
// (agentskills.io's SKILL.md spec).
func fetchSkillFiles(src Source, name string) (string, error) {
	tree, err := treeUnder(src.Repo, src.Ref, src.Path)
	if err != nil {
		return "", err
	}
	if tree.Truncated {
		// too many files for GitHub to list in one answer
		return "", errTooBig
	}
	var skillDirs, named []string
	for _, e := range tree.Tree {
		if e.Type == "blob" && slash.Base(e.Path) == "SKILL.md" {
			if d := slash.Dir(e.Path); inRepoDir(d, src.Path) {
				skillDirs = append(skillDirs, d)
				if name != "" && slash.Base(d) == name {
					named = append(named, d)
				}
			}
		}
	}
	if len(named) > 0 {
		skillDirs = named
	}
	inSkill := func(p string) bool {
		for _, d := range skillDirs {
			if d == "." || strings.HasPrefix(p, d+"/") {
				return true
			}
		}
		return false
	}
	// the skill a file is in: the deepest folder with a SKILL.md over it
	skillOf := func(p string) string {
		in := ""
		for _, d := range skillDirs {
			if (d == "." || strings.HasPrefix(p, d+"/")) && (in == "" || in == "." || len(d) > len(in)) {
				in = d
			}
		}
		return in
	}
	type file struct {
		path, sha string
		exec      bool
	}
	// links and submodules are left out, as the whole download leaves
	// them out
	size := map[string]int64{}
	for _, e := range tree.Tree {
		if e.Type == "blob" && e.Mode != "120000" && inSkill(e.Path) {
			size[skillOf(e.Path)] += e.Size
		}
	}
	// A skill over the limit on its own is left out, and the others are
	// fetched: one skill of 297 MB (mikubaka88/CCFA-Skills' ccf-paper-
	// writer, its exemplar papers' PDFs) stopped its 16 others of 1.5 MB
	// from being installed (#2062).
	var left []LeftOut
	for _, d := range skillDirs {
		if size[d] > maxFetch {
			p := d
			if p == "." {
				p = ""
			}
			left = append(left, LeftOut{Path: p, Size: size[d]})
		}
	}
	// a folder asked for whose skills are all too big is listed empty,
	// naming them, as the whole repository's list would
	if len(left) == len(skillDirs) && len(left) > 0 && src.Path == "" {
		return "", fmt.Errorf("the skills in %s are too big to install (over %s)", src.Repo, mb(maxFetch))
	}
	var files []file
	total := int64(0)
	for _, e := range tree.Tree {
		if e.Type != "blob" || e.Mode == "120000" || !inSkill(e.Path) || size[skillOf(e.Path)] > maxFetch {
			continue
		}
		if total += e.Size; total > maxFetch {
			return "", fmt.Errorf("the skills in %s are too big to install (over %s)", src.Repo, mb(maxFetch))
		}
		files = append(files, file{e.Path, e.SHA, e.Mode == "100755"})
	}
	tmp, err := os.MkdirTemp("", skillTemp)
	if err != nil {
		return "", err
	}
	place := func(p string) (string, bool) {
		dst := filepath.Join(tmp, filepath.FromSlash(p))
		return dst, strings.HasPrefix(dst, tmp+string(filepath.Separator)) // never outside
	}
	for _, e := range tree.Tree {
		// only the named skills' folders when narrowed to them: the rest
		// of a big repository is thousands of empty folders
		if e.Type == "tree" && inRepoDir(e.Path, src.Path) && (len(named) == 0 || inSkill(e.Path)) {
			if dst, ok := place(e.Path); ok {
				if err := os.MkdirAll(dst, 0o755); err != nil {
					os.RemoveAll(tmp)
					return "", err
				}
			}
		}
	}
	c := &http.Client{Timeout: time.Minute}
	work := make(chan file)
	errs := make(chan error, len(files))
	var wg sync.WaitGroup
	for range min(8, len(files)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range work {
				dst, ok := place(f.path)
				if !ok {
					continue
				}
				if err := fetchBlob(c, src, f.path, f.sha, dst, f.exec); err != nil {
					errs <- err
				}
			}
		}()
	}
	for _, f := range files {
		work <- f
	}
	close(work)
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	if len(left) > 0 {
		leftOut.Lock()
		for d := range leftOut.m {
			if _, err := os.Stat(d); err != nil {
				delete(leftOut.m, d) // a fetch no probe took, removed since
			}
		}
		leftOut.m[tmp] = left
		leftOut.Unlock()
	}
	return tmp, nil
}

// inRepoDir is whether a repository's path p is under the folder dir, or
// is it; everything is under "". A skill at the repository's root is ".".
func inRepoDir(p, dir string) bool {
	return dir == "" || p == dir || strings.HasPrefix(p, dir+"/")
}

// LeftOut is a skill of a repository too big to install on its own
// (fetchSkillFiles): where it is, and what its files come to.
type LeftOut struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// leftOut is the skills left out of each folder fetchSkillFiles fetched,
// by the folder; taken by tookLeftOut.
var leftOut = struct {
	sync.Mutex
	m map[string][]LeftOut
}{m: map[string][]LeftOut{}}

// tookLeftOut is the skills left out of a fetched folder, under sub, and
// forgets them.
func tookLeftOut(dir, sub string) []LeftOut {
	leftOut.Lock()
	all := leftOut.m[dir]
	delete(leftOut.m, dir)
	leftOut.Unlock()
	var out []LeftOut
	for _, l := range all {
		if sub == "" || l.Path == sub || strings.HasPrefix(l.Path, sub+"/") {
			out = append(out, l)
		}
	}
	return out
}

// mb is a size in MB, as the limit is said.
func mb(n int64) string { return strconv.FormatInt(n>>20, 10) + " MB" }

// fetchBlob writes one file of the repository to dst: from
// raw.githubusercontent.com, or for a private repository from GitHub's API
// with the user's token. What arrives must hash to the sha GitHub's list
// gave it.
func fetchBlob(c *http.Client, src Source, path, sha, dst string, exec bool) error {
	token := false
	get := func(u, accept string) (*http.Response, error) {
		req, _ := http.NewRequest("GET", u, nil)
		req.Header.Set("User-Agent", "magpie")
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		resp, t, err := doGitHub(req, func(r *http.Request) (*http.Response, error) { return source.DoOfficial(c, r) })
		token = t
		return resp, err
	}
	resp, err := get(rawURL(src.Repo, src.Ref, path), "")
	if err != nil {
		return fmt.Errorf("couldn't reach GitHub: %w", err)
	}
	if resp.StatusCode == 404 {
		if t, _ := GitHubToken(); t != "" {
			resp.Body.Close()
			if resp, err = get(githubAPI+"/repos/"+src.Repo+"/git/blobs/"+url.PathEscape(sha), "application/vnd.github.raw"); err != nil {
				return fmt.Errorf("couldn't reach GitHub: %w", err)
			}
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if e, ok := limitedBy(resp, body, token); ok {
			return e
		}
		return fmt.Errorf("GitHub answered %s for %s in %s", resp.Status, path, src.Repo)
	}
	mode := os.FileMode(0o644)
	if exec {
		mode = 0o755
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	// git's own hash of a file: "blob <size>\x00" and its bytes
	h := sha1.New()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetch+1))
	if err == nil {
		h.Write([]byte("blob " + strconv.Itoa(len(body)) + "\x00"))
		h.Write(body)
		_, err = out.Write(body)
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); sha != "" && got != sha {
		return fmt.Errorf("%s in %s changed while it was being fetched; try again", path, src.Repo)
	}
	return nil
}
