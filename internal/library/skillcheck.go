package library

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/ghtoken"
	"github.com/yetone/magpie/internal/source"
)

// ---- which skills GitHub has changed --------------------------------------

// githubAPI is where GitHub's REST API is; a variable for tests.
var githubAPI = "https://api.github.com"

// SkillCheck is what a check for updates found of one skill from GitHub.
type SkillCheck struct {
	Name string `json:"name"`
	// Status is current when GitHub has the skill as the library does,
	// update when it has changed it since, gone when it was read without
	// the skill (removed there, skill_gone.go), and unknown when it
	// couldn't be told (Error says why)
	Status  string `json:"status"`
	Commit  string `json:"commit,omitempty"`  // the last commit to touch the skill's folder
	Date    string `json:"date,omitempty"`    // when it was made
	Message string `json:"message,omitempty"` // its first line
	// URL shows what changed: the commits since the one installed, or the
	// folder's history when which that was isn't known
	URL   string `json:"url,omitempty"`
	Error string `json:"error,omitempty"`
	// Limited is set when it couldn't be told because GitHub's rate limit
	// was used up, for the page to say so in its own words
	Limited *Limited `json:"limited,omitempty"`
}

// Limited is GitHub's rate limit used up: until when (RFC 3339, "" when
// GitHub didn't say), and whether the requests carried a GitHub token.
type Limited struct {
	Until string `json:"until,omitempty"`
	Token bool   `json:"token,omitempty"`
}

func limitedView(e errLimited) *Limited {
	l := &Limited{Token: e.Token}
	if !e.Until.IsZero() {
		l.Until = e.Until.UTC().Format(time.RFC3339)
	}
	return l
}

// The last check's findings stay for the page until a skill is updated,
// installed again or removed.
var checks = struct {
	sync.Mutex
	m map[string]SkillCheck
}{m: map[string]SkillCheck{}}

func lastCheck(name string) *SkillCheck {
	checks.Lock()
	defer checks.Unlock()
	c, ok := checks.m[name]
	if !ok {
		return nil
	}
	return &c
}

func forgetCheck(name string) {
	checks.Lock()
	delete(checks.m, name)
	checks.Unlock()
}

// checkedAt is when the skills were last checked for updates (unix
// nanoseconds), 0 before the first check since magpie started.
var checkedAt atomic.Int64

// checkEvery is how long the Library page goes before it checks for
// updates by itself, as it opens the skills (#1449).
const checkEvery = 12 * time.Hour

// checkDue is whether the page should check the library's skills from
// GitHub or git for updates, and the folders the user added skills from
// for new ones: none checked since magpie started, or not for checkEvery.
func checkDue(l *Library) bool {
	if !slices.ContainsFunc(l.Skills, func(s *Skill) bool { return s.Source.remote() }) && !l.addedFromFolders() {
		return false
	}
	at := checkedAt.Load()
	return at == 0 || time.Since(time.Unix(0, at)) >= checkEvery
}

// commit is one commit as GitHub's API lists it.
type commit struct {
	SHA    string `json:"sha"`
	URL    string `json:"html_url"`
	Commit struct {
		Message   string `json:"message"`
		Committer struct {
			Date string `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
}

// lastCommit is the last commit to touch a folder of a repository on a ref.
// It asks with the ETag of the answer it had before, if any: GitHub's 304
// for an unchanged one doesn't count against the rate limit.
func lastCommit(src Source) (*commit, error) {
	q := url.Values{"per_page": {"1"}}
	if src.Path != "" {
		q.Set("path", src.Path)
	}
	if src.Ref != "" {
		q.Set("sha", src.Ref)
	}
	u := githubAPI + "/repos/" + src.Repo + "/commits?" + q.Encode()
	body, code, token, err := apiGet(u, 8<<20)
	if err != nil {
		return nil, err
	}
	switch {
	case code == 401 && token:
		return nil, ghtoken.Refused()
	case code == 404, code == 422:
		if src.Ref != "" {
			return nil, fmt.Errorf("GitHub has no %s at %s any more", src.Repo, src.Ref)
		}
		return nil, fmt.Errorf("GitHub has no repository %s any more", src.Repo)
	case code != 200:
		return nil, fmt.Errorf("GitHub answered %d %s", code, http.StatusText(code))
	}
	var list []commit
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("GitHub's answer wasn't understood: %w", err)
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("%s has nothing at %s any more", src.Repo, src.Path)
	}
	return &list[0], nil
}

// cacheBody is the most of an answer kept by URL for a 304 to reuse;
// bigger ones are read but not kept, so a huge file list isn't held for
// the process's life.
const cacheBody = 8 << 20

// errOverCap is an answer over the most its caller reads (apiGet's max).
var errOverCap = errors.New("over the most magpie reads")

// apiGet asks GitHub's API for u, with the ETag of the answer it had
// before, if any: GitHub's 304 for an unchanged one doesn't count against
// the rate limit, and is answered as the 200 it had. It says whether the
// request carried a GitHub token, and a rate limit used up is an
// errLimited. A rate limit is told from the status and headers even when
// the answer can't be read whole. A 200 over max bytes is an error, never
// a cut-off body handed to a caller, and no answer over cacheBody is kept.
func apiGet(u string, max int64) ([]byte, int, bool, error) {
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", "magpie")
	req.Header.Set("Accept", "application/vnd.github+json")
	etags.Lock()
	had, cached := etags.m[u]
	etags.Unlock()
	if cached {
		req.Header.Set("If-None-Match", had.etag)
	}
	// a file list of tens of MB takes longer than 20s on a slow line
	timeout := 20 * time.Second
	if max > cacheBody {
		timeout = 2 * time.Minute
	}
	c := &http.Client{Timeout: timeout}
	resp, token, err := doGitHub(req, func(r *http.Request) (*http.Response, error) { return source.Do(c, r) })
	if err != nil {
		return nil, 0, token, fmt.Errorf("couldn't reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if e, ok := limitedBy(resp, body, token); ok {
		return nil, resp.StatusCode, token, e
	}
	if err != nil {
		return nil, resp.StatusCode, token, fmt.Errorf("couldn't read GitHub's answer: %w", err)
	}
	if resp.StatusCode == 304 && cached {
		return had.body, 200, token, nil
	}
	if resp.StatusCode == 200 && int64(len(body)) > max {
		return nil, resp.StatusCode, token, fmt.Errorf("GitHub's answer was over %s: %w", mb(max), errOverCap)
	}
	// an empty 200 kept for a 304 would answer nothing forever (#3045)
	if resp.StatusCode == 200 && len(body) == 0 {
		return nil, resp.StatusCode, token, fmt.Errorf("GitHub answered nothing")
	}
	if et := resp.Header.Get("ETag"); resp.StatusCode == 200 && et != "" && int64(len(body)) <= cacheBody {
		etags.Lock()
		etags.m[u] = etagged{etag: et, body: body}
		etags.Unlock()
	}
	return body, resp.StatusCode, token, nil
}

// hashDir is a hash of a skill's files, their names and what's in them,
// as copyDir copies them.
func hashDir(dir string) string { return hashFiles(dir, false) }

// hashFiles is hashDir, leaving aside the .DS_Store files Finder leaves in
// a folder it shows when noFinder is set. A folder unchanged since it was
// last hashed isn't read again (treememo.go).
func hashFiles(dir string, noFinder bool) string {
	if noFinder {
		return rememberedHash(dir, leaveFinder)
	}
	return rememberedHash(dir, leaveNothing)
}

// leave is what a hash of a skill's folder leaves aside, beyond its .git
// and magpie's mark.
type leave uint8

const (
	leaveNothing   leave = iota
	leaveFinder          // .DS_Store
	leaveBuilt           // that, and what installing dependencies builds (fromInstall)
	leaveInstalled       // that, and the lock files an install writes again
)

// hashWalk walks dir as hashFiles reads it, stamping each file and link
// it would hash (treememo.go); with read, it also hashes their bytes. ok
// is false when it can't be walked, or a file can't be read.
func hashWalk(dir string, lv leave, read bool) (hash, stamp string, settled, ok bool) {
	h := sha256.New()
	st := newStamper()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if p != dir && lv >= leaveBuilt && fromInstall(d.Name(), d.IsDir() || d.Type()&fs.ModeSymlink != 0, lv == leaveInstalled) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case d.IsDir():
			if d.Name() == ".git" && p != dir {
				return filepath.SkipDir
			}
		case d.Type()&fs.ModeSymlink != 0:
			fi, err := d.Info()
			if err != nil {
				return err
			}
			st.add(rel, fi)
			if read {
				t, _ := os.Readlink(p)
				fmt.Fprintf(h, "link %s %s\n", rel, filepath.ToSlash(t))
			}
		case d.Type().IsRegular() && rel != marker && !(lv >= leaveFinder && d.Name() == ".DS_Store"):
			fi, err := d.Info()
			if err != nil {
				return err
			}
			st.add(rel, fi)
			if !read {
				return nil
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			fi, _ = f.Stat()
			fmt.Fprintf(h, "file %s %d\n", rel, fi.Size())
			if _, err := io.Copy(h, f); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", "", false, false
	}
	stamp, settled = st.done()
	if read {
		hash = hex.EncodeToString(h.Sum(nil))
	}
	return hash, stamp, settled, true
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 120 {
		s = string(r[:119]) + "…"
	}
	return s
}

// gitLooked is, by repoKey, the commit a git repository was last read at
// for new skills: one at the same commit has none since, and isn't
// fetched again for them.
var gitLooked sync.Map

func gitLookedAt(repo, ref, head string) bool {
	h, ok := gitLooked.Load(repoKey(repo, ref))
	if !ok {
		return false
	}
	newFound.Lock()
	_, kept := newFound.m[repoKey(repo, ref)]
	newFound.Unlock()
	return kept && strings.EqualFold(h.(string), head)
}

// CheckSkills asks GitHub, for every skill installed from there, for the
// last commit to touch its folder, and tells whether it's newer than what
// the library has: by the commit a skill is known to be at, or, while
// that isn't known yet, by fetching the repository once and comparing the
// folder's files with the skill's. A skill GitHub couldn't be asked about
// is unknown, and the others are checked all the same; once GitHub limits
// requests, the rest aren't asked about. A skill from another git
// repository is checked with the user's git (checkGitSkill). Each
// repository, and each folder the user added skills from, is also looked
// in for skills added beside them since (skillnew.go).
func CheckSkills() ([]SkillCheck, error) {
	mu.Lock()
	l, err := load()
	mu.Unlock()
	if err != nil {
		return nil, err
	}
	byRepo := map[string][]*Skill{}
	for _, s := range l.Skills {
		if s.Source.remote() {
			byRepo[s.Source.Kind+"\x00"+s.Source.Repo] = append(byRepo[s.Source.Kind+"\x00"+s.Source.Repo], s)
		}
	}
	f := &fetcher{}
	defer f.clean()
	var (
		out     []SkillCheck
		known   = map[string][2]string{} // name: the commit found, and the hash it was found by
		omu     sync.Mutex
		wg      sync.WaitGroup
		sem     = make(chan struct{}, 4)
		limited atomic.Pointer[errLimited]
	)
	for _, skills := range byRepo {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			asked := map[string]*commit{} // a repository's skills in one folder are asked about once
			heads := map[string]string{}  // a git repository's refs, asked about once
			refs := []string{}
			for _, s := range skills {
				var c SkillCheck
				var sha, hash string
				if s.Source.Kind == "git" {
					c, sha, hash = checkGitSkill(s, f, heads)
				} else {
					c, sha, hash = checkSkill(s, f, asked, &limited)
				}
				omu.Lock()
				out = append(out, c)
				if sha != "" {
					known[s.Name] = [2]string{sha, hash}
				}
				omu.Unlock()
				if !slices.Contains(refs, s.Source.Ref) {
					refs = append(refs, s.Source.Ref)
				}
			}
			// and what the repository has added beside them since: GitHub's
			// list of its files says; another host's repository is read
			// from its fetch, once for each commit its ref is at
			for _, ref := range refs {
				repo := skills[0].Source.Repo
				var found []NewSkill
				var ok bool
				switch head := heads[ref]; {
				case skills[0].Source.Kind == "github":
					found, ok = findNew(l, repo, ref, f, &limited)
				case head != "" && !gitLookedAt(repo, ref, head):
					if found, ok = findNewGit(l, repo, ref, f); ok {
						gitLooked.Store(repoKey(repo, ref), head)
					}
				}
				if ok {
					newFound.Lock()
					newFound.m[repoKey(repo, ref)] = found
					newFound.Unlock()
				}
			}
		}()
	}
	// the folders the user added skills from, for ones put there since
	lookInFolders(l)
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if len(known) > 0 {
		// what the check learnt is kept, for the next to ask GitHub only
		// for the last commit; a skill updated meanwhile is left alone
		mu.Lock()
		if l, err := load(); err == nil {
			for _, s := range l.Skills {
				k, ok := known[s.Name]
				// a git repository's commit is the whole repository's: a
				// check that found the files the same at a later one keeps that
				if ok && s.Source.remote() && (s.Commit == "" || s.Source.Kind == "git") && (s.Hash == "" || s.Hash == k[1]) {
					s.Commit, s.Hash = k[0], k[1]
				}
			}
			_ = l.save()
		}
		mu.Unlock()
	}
	var gone, there []string
	for _, c := range out {
		switch c.Status {
		case "gone":
			gone = append(gone, c.Name)
		case "current", "update":
			there = append(there, c.Name)
		}
	}
	markGone(gone, there)
	checks.Lock()
	for _, c := range out {
		checks.m[c.Name] = c
	}
	checks.Unlock()
	checkedAt.Store(time.Now().UnixNano())
	if out == nil {
		out = []SkillCheck{}
	}
	return out, nil
}

// checkSkill checks one skill; when its files were found to be GitHub's,
// it gives back the commit they're at and their hash, for the library to
// keep.
func checkSkill(s *Skill, f *fetcher, asked map[string]*commit, limited *atomic.Pointer[errLimited]) (SkillCheck, string, string) {
	src := *s.Source
	c := SkillCheck{Name: s.Name, Status: "unknown"}
	key := src.Ref + "\x00" + src.Path
	last := asked[key]
	if last == nil {
		if e := limited.Load(); e != nil {
			c.Error, c.Limited = e.Error(), limitedView(*e)
			return c, "", ""
		}
		var err error
		last, err = lastCommit(src)
		if err != nil {
			var e errLimited
			if errors.As(err, &e) {
				limited.Store(&e)
				c.Limited = limitedView(e)
			}
			c.Error = err.Error()
			return c, "", ""
		}
		asked[key] = last
	}
	c.Commit, c.Date, c.Message = last.SHA, last.Commit.Committer.Date, firstLine(last.Commit.Message)
	ref := src.Ref
	if ref == "" {
		ref = "HEAD"
	}
	history := "https://github.com/" + src.Repo + "/commits/" + ref
	if src.Path != "" {
		history += "/" + src.Path
	}
	if s.Commit != "" {
		if s.Commit == last.SHA {
			c.Status = "current"
			return c, "", ""
		}
		if s.Gone != "" {
			// the last commit to touch its folder may be the one that
			// removed it: it stays removed till an update finds it there
			g := goneCheck(&GoneError{Name: s.Name, Source: src.Repo})
			g.Commit, g.Date, g.Message = c.Commit, c.Date, c.Message
			return g, "", ""
		}
		c.Status, c.URL = "update", "https://github.com/"+src.Repo+"/compare/"+s.Commit+"..."+last.SHA
		return c, "", ""
	}
	// which commit it's at isn't known: its files are compared with GitHub's
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
		have = hashDir(skillDir(s.Name)) // installed before magpie kept it
	}
	if theirs := s.fetchedHash(from); theirs == "" || theirs != have {
		c.Status, c.URL = "update", history
		return c, "", ""
	}
	c.Status = "current"
	return c, last.SHA, have
}
