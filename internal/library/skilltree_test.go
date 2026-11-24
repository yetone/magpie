package library

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	slash "path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBigRepo stands in for codeload, GitHub's API and
// raw.githubusercontent.com for a repository laid out as
// vectorize-io/hindsight is (#1941): an app whose whole download is over
// the limit, with its skills in a few small folders beside it. It notes
// the paths raw was asked for and how often codeload was.
type fakeBigRepo struct {
	mu       sync.Mutex
	raw      []string
	tarballs int
	files    map[string]string
	serve    map[string]string // what raw hands out, where it differs
	trees    []string          // the tree-ish each file list was asked at
	pad      int               // files of the app's own, in the whole list only
	slow     bool              // codeload sends a little, then nothing until the client gives up
}

func gitSHA(body string) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00%s", len(body), body)
	return hex.EncodeToString(h.Sum(nil))
}

func newFakeBigRepo(t *testing.T) *fakeBigRepo {
	t.Helper()
	big := strings.Repeat("PK\x03\x04 jar ", 20<<10) // 240 KB, past the test's limit
	g := &fakeBigRepo{serve: map[string]string{}, files: map[string]string{
		"README.md": "# Hindsight",
		"hindsight-clients/go/openapi-generator-cli.jar":                 big,
		"hindsight-clients/go/openapi-generator-config.yaml":             "packageName: hindsight\n",
		"skills/hindsight-docs/SKILL.md":                                 "---\nname: hindsight-docs\ndescription: Hindsight's docs\n---\n# Docs\n",
		"skills/hindsight-docs/references/best-practices.md":             "# Best practices\n",
		"skills/hindsight-docs/references/changelog/index.md":            "# Changelog\n",
		"skills/hindsight-local/SKILL.md":                                "---\nname: hindsight-local\ndescription: Run Hindsight locally\n---\n",
		"skills/hindsight-local/scripts/start.sh":                        "#!/bin/sh\necho start\n",
		".claude/skills/ship-it/SKILL.md":                                "---\nname: ship-it\n---\n",
		"hindsight-integrations/cursor/skills/hindsight-recall/SKILL.md": "---\nname: hindsight-recall\ndescription: Recall\n---\n",
	}}
	codeload := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.tarballs++
		slow := g.slow
		g.mu.Unlock()
		body := tarball(t, g.files)
		if slow {
			w.Write(body[:len(body)/4])
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		w.Write(body)
	}))
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ish, found := strings.CutPrefix(r.URL.Path, "/repos/vectorize-io/hindsight/git/trees/")
		if !found {
			http.NotFound(w, r)
			return
		}
		g.mu.Lock()
		g.trees = append(g.trees, ish)
		pad := g.pad
		g.mu.Unlock()
		// git's ref:path is that folder's own list, its paths from there,
		// as GitHub answers it; no such folder is a 404
		_, under, _ := strings.Cut(ish, ":")
		// GitHub's answer, as it lists hindsight: folders, then files with
		// their mode, git hash and size
		var tree []map[string]any
		dirs := map[string]bool{}
		paths := []string{}
		for p := range g.files {
			paths = append(paths, p)
			for d := slash.Dir(p); d != "."; d = slash.Dir(d) {
				dirs[d] = true
			}
		}
		for d := range dirs {
			paths = append(paths, d)
		}
		sort.Strings(paths)
		if under != "" && !dirs[under] {
			http.NotFound(w, r)
			return
		}
		for _, p := range paths {
			at := p
			if under != "" {
				var in bool
				if at, in = strings.CutPrefix(p, under+"/"); !in {
					continue
				}
			}
			if dirs[p] {
				tree = append(tree, map[string]any{"mode": "040000", "path": at, "sha": "e488653ba64c6cfc3551ec46454b965421d50176", "type": "tree"})
				continue
			}
			mode := "100644"
			if strings.HasSuffix(p, ".sh") {
				mode = "100755"
			}
			tree = append(tree, map[string]any{"mode": mode, "path": at, "sha": gitSHA(g.files[p]), "size": len(g.files[p]), "type": "blob"})
		}
		for i := 0; under == "" && i < pad; i++ {
			tree = append(tree, map[string]any{"mode": "100644", "path": fmt.Sprintf("hindsight-app/src/renderer/components/panels/sidebar/file%05d.ts", i),
				"sha": "e488653ba64c6cfc3551ec46454b965421d50176", "size": 321, "type": "blob",
				"url": "https://api.github.com/repos/vectorize-io/hindsight/git/blobs/e488653ba64c6cfc3551ec46454b965421d50176"})
		}
		json.NewEncoder(w).Encode(map[string]any{"sha": "52ef60fad332184c3b8a1f9fff4756247fc07146", "tree": tree, "truncated": false})
	}))
	raw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := strings.CutPrefix(r.URL.Path, "/vectorize-io/hindsight/HEAD/")
		g.mu.Lock()
		g.raw = append(g.raw, p)
		body, ok := g.serve[p]
		g.mu.Unlock()
		if !ok {
			if body, ok = g.files[p]; !ok {
				http.NotFound(w, r)
				return
			}
		}
		fmt.Fprint(w, body)
	}))
	oldT, oldA, oldR, oldM := tarballURL, githubAPI, rawURL, maxFetch
	tarballURL = func(repo, ref string) string { return codeload.URL + "/tar/" + repo }
	githubAPI = api.URL
	rawURL = func(repo, ref, path string) string {
		if ref == "" {
			ref = "HEAD"
		}
		return raw.URL + "/" + repo + "/" + ref + "/" + path
	}
	maxFetch = 100 << 10
	t.Cleanup(func() {
		tarballURL, githubAPI, rawURL, maxFetch = oldT, oldA, oldR, oldM
		codeload.Close()
		api.Close()
		raw.Close()
		tooBig.Lock()
		clear(tooBig.m)
		tooBig.Unlock()
	})
	return g
}

// #1941 (Goalonez): a repository too big to download whole, its skills in
// small folders beside an app, installs them: only the skills' folders are
// fetched, every file of them, and a second fetch goes straight to them.
func TestSkillsFromATooBigRepository(t *testing.T) {
	sandbox(t)
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	g := newFakeBigRepo(t)

	p, err := ProbeSkills("https://github.com/vectorize-io/hindsight")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	var names []string
	for _, c := range p.Candidates {
		names = append(names, c.Path)
	}
	// ship-it is the repository's own, in .claude/skills, which the skills
	// CLI lists too (#1964)
	if want := []string{"skills/hindsight-docs", "skills/hindsight-local", "hindsight-integrations/cursor/skills/hindsight-recall", ".claude/skills/ship-it"}; strings.Join(names, " ") != strings.Join(want, " ") {
		t.Fatalf("candidates %v, want %v", names, want)
	}
	ok(t)(InstallSkills("https://github.com/vectorize-io/hindsight", []string{"skills/hindsight-docs", "skills/hindsight-local"}, []string{"claude"}))
	for _, f := range []string{"hindsight-docs/SKILL.md", "hindsight-docs/references/changelog/index.md", "hindsight-local/scripts/start.sh"} {
		b, err := os.ReadFile(filepath.Join(skillDir("."), f))
		if err != nil || string(b) != g.files["skills/"+f] {
			t.Errorf("%s: %q, %v", f, b, err)
		}
	}
	g.mu.Lock()
	for _, p := range g.raw {
		if strings.HasPrefix(p, "hindsight-clients/") || p == "README.md" {
			t.Errorf("raw was asked for %s, which no skill has", p)
		}
	}
	g.mu.Unlock()

	// a subfolder deep in the repository, as a link to it gives
	p, err = ProbeSkills("https://github.com/vectorize-io/hindsight/tree/HEAD/hindsight-integrations/cursor")
	if err != nil || len(p.Candidates) != 1 || p.Candidates[0].Name != "hindsight-recall" {
		t.Fatalf("subfolder probe: %+v, %v", p, err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tarballs != 1 {
		t.Errorf("codeload was asked %d times; once found too big, the repository's skills are fetched alone", g.tarballs)
	}
}

// What raw hands out must be the file GitHub's list names; skills too big
// themselves still say so.
func TestTooBigRepositoryChecksWhatArrives(t *testing.T) {
	sandbox(t)
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	g := newFakeBigRepo(t)
	g.serve["skills/hindsight-docs/references/best-practices.md"] = "something else"
	if _, err := ProbeSkills("vectorize-io/hindsight"); err == nil || !strings.Contains(err.Error(), "changed while it was being fetched") {
		t.Fatalf("a file that isn't the listed one: %v", err)
	}
	delete(g.serve, "skills/hindsight-docs/references/best-practices.md")
	for p := range g.files {
		if strings.HasSuffix(p, "SKILL.md") {
			g.files[slash.Dir(p)+"/model.bin"] = strings.Repeat("x", 200<<10)
		}
	}
	if _, err := ProbeSkills("vectorize-io/hindsight"); err == nil || !strings.Contains(err.Error(), "skills in vectorize-io/hindsight are too big") {
		t.Fatalf("skills too big themselves: %v", err)
	}
}

// #2062 (liangxyz): mikubaka88/CCFA-Skills has 17 skills, one of them
// (ccf-paper-writer, its exemplar papers' PDFs) 297 MB and the others 1.5
// MB together. The one over the limit is left out and named; the others
// are listed and install, and raw is never asked for its files.
func TestOneSkillTooBigLeavesTheOthers(t *testing.T) {
	sandbox(t)
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	g := newFakeBigRepo(t)
	big := strings.Repeat("%PDF-1.5 ", 15<<10) // 135 KB, past the test's limit of 100 KB
	g.files["skills/hindsight-local/references/papers/llava-4d.pdf"] = big
	g.files["skills/hindsight-local/references/papers/2212.09748.pdf"] = big

	p, err := ProbeSkills("vectorize-io/hindsight")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	var names []string
	for _, c := range p.Candidates {
		names = append(names, c.Path)
	}
	if want := []string{"skills/hindsight-docs", "hindsight-integrations/cursor/skills/hindsight-recall", ".claude/skills/ship-it"}; strings.Join(names, " ") != strings.Join(want, " ") {
		t.Fatalf("candidates %v, want %v", names, want)
	}
	if len(p.Left) != 1 || p.Left[0].Path != "skills/hindsight-local" || p.Left[0].Size < int64(2*len(big)) {
		t.Fatalf("left out %+v, want skills/hindsight-local with its size", p.Left)
	}
	ok(t)(InstallSkills("vectorize-io/hindsight", []string{"skills/hindsight-docs"}, []string{"claude"}))
	if _, err := os.Stat(filepath.Join(skillDir("hindsight-docs"), "SKILL.md")); err != nil {
		t.Fatalf("hindsight-docs wasn't installed: %v", err)
	}
	g.mu.Lock()
	for _, r := range g.raw {
		if strings.HasPrefix(r, "skills/hindsight-local/") {
			t.Errorf("raw was asked for %s, of the skill left out", r)
		}
	}
	g.mu.Unlock()

	// a link to the big skill's own folder: nothing to list, and it named
	p, err = ProbeSkills("https://github.com/vectorize-io/hindsight/tree/HEAD/skills/hindsight-local")
	if err != nil || len(p.Candidates) != 0 || len(p.Left) != 1 {
		t.Fatalf("the big skill's folder: %+v, %v", p, err)
	}
}

// #2794 (CaiusLuo): the market installs clean-code from
// sickn33/agentic-awesome-skills, whose whole download is too big, whose
// file list is 11 MB (over the 8 MB GitHub's answers were cut at, so it
// failed as "unexpected end of JSON input"), and whose 8,589 skills come
// to 287 MB, over the limit together. The list is read whole, only the
// skill by that name is fetched, and an update asks for its folder's list
// alone. Two skills of it updated together are each fetched, neither
// taken for removed.
func TestMarketSkillFromAHugeRepository(t *testing.T) {
	sandbox(t)
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	g := newFakeBigRepo(t)
	g.pad = 60_000 // ~11 MB of list, as sickn33/agentic-awesome-skills'
	// each under the limit, together over it
	g.files["skills/hindsight-local/references/big.md"] = strings.Repeat("local ", 10<<10)
	g.files["hindsight-integrations/cursor/skills/hindsight-recall/references/big.md"] = strings.Repeat("recall ", 10<<10)

	ok(t)(InstallMarketSkill("vectorize-io/hindsight", "hindsight-docs", []string{"claude"}))
	if b, err := os.ReadFile(filepath.Join(skillDir("hindsight-docs"), "references/changelog/index.md")); err != nil || string(b) != "# Changelog\n" {
		t.Fatalf("hindsight-docs: %q, %v", b, err)
	}
	g.mu.Lock()
	for _, r := range g.raw {
		if !strings.HasPrefix(r, "skills/hindsight-docs/") {
			t.Errorf("raw was asked for %s, outside the one skill the market installs", r)
		}
	}
	g.raw, g.trees = nil, nil
	g.mu.Unlock()

	ok(t)(InstallMarketSkill("vectorize-io/hindsight", "hindsight-recall", []string{"claude"}))
	g.mu.Lock()
	g.trees = nil
	g.mu.Unlock()
	r, err := UpdateSkills()
	if err != nil || len(r.Gone) > 0 || len(r.Unupdated) > 0 || len(r.Updated) != 2 {
		t.Fatalf("update: %+v, %v", r, err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	sort.Strings(g.trees)
	if want := []string{"HEAD:hindsight-integrations/cursor/skills/hindsight-recall", "HEAD:skills/hindsight-docs"}; !slices.Equal(g.trees, want) {
		t.Errorf("an update listed %q, want each skill's own folder %q", g.trees, want)
	}
}

// A list GitHub cut off itself (over 100,000 files) says the repository
// is too big; it is never read as the whole of it.
func TestTruncatedListIsTooBig(t *testing.T) {
	sandbox(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"sha":"x","tree":[{"path":"skills/a/SKILL.md","type":"blob","mode":"100644","sha":"y","size":3}],"truncated":true}`)
	}))
	defer srv.Close()
	oldA := githubAPI
	githubAPI = srv.URL
	defer func() { githubAPI = oldA }()
	for _, path := range []string{"", "skills/a"} {
		if _, err := fetchSkillFiles(Source{Kind: "github", Repo: "o/r", Path: path}, ""); !errors.Is(err, errTooBig) {
			t.Errorf("path %q: %v", path, err)
		}
	}
}

// VincentChan (Discord): next-cache-components-adoption, synced from another
// computer, was "not fetched from its source on this computer yet (Update
// tries again): context deadline exceeded (Client.Timeout or context
// cancellation while reading body)" on every sync. The repository's whole
// download, under the size limit, outlasted its time on a slow line,
// which the size fallback never saw. A download that runs out of time
// fetches the skills' folders alone, as one too big does.
func TestSkillsFromATooSlowRepository(t *testing.T) {
	sandbox(t)
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	g := newFakeBigRepo(t)
	g.slow = true
	forgetProbes() // another test's probe of the same repository
	maxFetch = 200 << 20
	old := wholeTimeout
	wholeTimeout = time.Second
	t.Cleanup(func() { wholeTimeout = old })
	if !downloadTimedOut(context.DeadlineExceeded) || downloadTimedOut(errTooBig) {
		t.Fatal("downloadTimedOut")
	}

	ok(t)(InstallSkills("https://github.com/vectorize-io/hindsight", []string{"skills/hindsight-docs"}, []string{"claude"}))
	b, err := os.ReadFile(filepath.Join(skillDir("."), "hindsight-docs", "references", "best-practices.md"))
	if err != nil || string(b) != g.files["skills/hindsight-docs/references/best-practices.md"] {
		t.Fatalf("hindsight-docs: %q, %v", b, err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tarballs != 1 {
		t.Errorf("codeload was asked %d times; once too slow, the repository's skills are fetched alone", g.tarballs)
	}
}
