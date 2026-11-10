package library

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/proc"
)

// Pi's package market (nineruiing on X). Pi's gallery, pi.dev/packages, is
// npm's packages with the keyword "pi-package", drawn on its server with no
// API of its own; magpie reads the same list from npm's search, kept as
// the skills market keeps skills.sh's: six hours in memory, on disk for
// when npm can't be reached, else the snapshot magpie shipped with.
//
// A package is installed and removed by Pi's own `pi install npm:<name>`
// and `pi remove`, run with PI_CODING_AGENT_DIR set to that Pi's agent
// folder (@earendil-works/pi-coding-agent 1.5.1, package-manager-cli.js and
// core/package-manager.js: installAndPersist installs it under
// <agent dir>/npm and adds its source to settings.json; removeAndPersist
// uninstalls it and takes out every entry of the same identity). A Pi
// whose command magpie can't run here, one in WSL or one without its CLI
// found, is given the entry alone, which Pi installs when it next starts
// (resolve → installMissing), as a copy from another Pi is.

// PiMarketPackage is one package the market offers.
type PiMarketPackage struct {
	// Source is what Pi installs it by: npm:<name>
	Source      string `json:"source"`
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	Publisher   string `json:"publisher,omitempty"`
	// Downloads is npm's count for the last month
	Downloads int    `json:"downloads,omitempty"`
	Icon      string `json:"icon,omitempty"`
	NPM       string `json:"npm"`
	Repo      string `json:"repo,omitempty"`
	Homepage  string `json:"homepage,omitempty"`
	Updated   string `json:"updated,omitempty"`
}

// piListed is a package as the snapshot and the disk cache keep it.
type piListed struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	Publisher   string `json:"publisher,omitempty"`
	Downloads   int    `json:"downloads,omitempty"`
	Repository  string `json:"repository,omitempty"`
	Homepage    string `json:"homepage,omitempty"`
	Date        string `json:"date,omitempty"`
}

// piNPMURL is npm's search; a variable for tests.
var piNPMURL = "https://registry.npmjs.org/-/v1/search"

//go:embed market_pi.json
var piSnapshot []byte

var piPopular = struct {
	sync.Mutex
	list []piListed
	at   time.Time
}{}

// popularPi is the most downloaded of the first 500 packages npm lists by
// the keyword: Pi's gallery's own source.
func popularPi() []piListed {
	piPopular.Lock()
	defer piPopular.Unlock()
	if piPopular.list != nil && time.Since(piPopular.at) < 6*time.Hour {
		return piPopular.list
	}
	cache := marketCache("pi-packages.json")
	if list, err := fetchPiPopular(); err == nil && len(list) > 0 {
		piPopular.list, piPopular.at = list, time.Now()
		if b, err := json.Marshal(list); err == nil && cache != "" {
			_ = os.MkdirAll(filepath.Dir(cache), 0o755)
			_ = os.WriteFile(cache, b, 0o644)
		}
		return list
	}
	var list []piListed
	if b, err := os.ReadFile(cache); err != nil || json.Unmarshal(b, &list) != nil || len(list) == 0 {
		_ = json.Unmarshal(piSnapshot, &list)
	}
	piPopular.list, piPopular.at = list, time.Now().Add(-6*time.Hour+5*time.Minute) // try again in a while
	return list
}

func fetchPiPopular() ([]piListed, error) {
	var list []piListed
	for from := 0; from < 500; from += 250 {
		page, total, err := searchPiNPM("", 250, from)
		if err != nil {
			if len(list) > 0 {
				break
			}
			return nil, err
		}
		list = append(list, page...)
		if from+250 >= total {
			break
		}
	}
	slices.SortStableFunc(list, func(a, b piListed) int { return cmp.Compare(b.Downloads, a.Downloads) })
	return piOnce(list), nil
}

// searchPiNPM is one page of npm's search for packages with the keyword,
// and how many there are.
func searchPiNPM(q string, size, from int) ([]piListed, int, error) {
	text := "keywords:pi-package"
	if q = strings.TrimSpace(q); q != "" {
		text += " " + q
	}
	var body struct {
		Total   int `json:"total"`
		Objects []struct {
			Downloads struct {
				Monthly int `json:"monthly"`
			} `json:"downloads"`
			Package struct {
				Name        string   `json:"name"`
				Version     string   `json:"version"`
				Description string   `json:"description"`
				Keywords    []string `json:"keywords"`
				Date        string   `json:"date"`
				Publisher   struct {
					Username string `json:"username"`
				} `json:"publisher"`
				Links struct {
					Homepage   string `json:"homepage"`
					Repository string `json:"repository"`
				} `json:"links"`
			} `json:"package"`
		} `json:"objects"`
	}
	u := piNPMURL + "?" + url.Values{"text": {text}, "size": {strconv.Itoa(size)}, "from": {strconv.Itoa(from)}}.Encode()
	if err := getJSON(u, &body); err != nil {
		return nil, 0, &SearchError{Source: "npm", Err: err}
	}
	var out []piListed
	for _, o := range body.Objects {
		p := o.Package
		// a word of the search can match a package by its text alone; Pi's
		// gallery lists only those with the keyword
		if !slices.Contains(p.Keywords, "pi-package") || !piNameRe.MatchString(p.Name) {
			continue
		}
		d := strings.TrimSpace(p.Description)
		if r := []rune(d); len(r) > 280 {
			d = strings.TrimSpace(string(r[:277])) + "…"
		}
		out = append(out, piListed{Name: p.Name, Version: p.Version, Description: d, Publisher: p.Publisher.Username,
			Downloads: o.Downloads.Monthly, Repository: p.Links.Repository, Homepage: p.Links.Homepage, Date: p.Date})
	}
	return out, body.Total, nil
}

func piOnce(list []piListed) []piListed {
	seen := map[string]bool{}
	return slices.DeleteFunc(list, func(p piListed) bool {
		if seen[p.Name] {
			return true
		}
		seen[p.Name] = true
		return false
	})
}

// MarketPiPackages is what the market shows for a search: the popular
// packages that match it, then what npm finds for it.
func MarketPiPackages(q string) ([]PiMarketPackage, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	var list []piListed
	for _, p := range popularPi() {
		if q == "" || strings.Contains(strings.ToLower(p.Name+" "+p.Publisher+" "+p.Description), q) {
			list = append(list, p)
		}
	}
	var err error
	if len([]rune(q)) >= 2 {
		var found []piListed
		found, _, err = searchPiNPM(q, 100, 0)
		list = piOnce(append(list, found...))
	}
	out := []PiMarketPackage{}
	for _, p := range list {
		out = append(out, piMarketOf(p))
	}
	return out, err
}

func piMarketOf(p piListed) PiMarketPackage {
	m := PiMarketPackage{Source: "npm:" + p.Name, Name: p.Name, Version: p.Version, Description: p.Description,
		Publisher: p.Publisher, Downloads: p.Downloads, NPM: "https://www.npmjs.com/package/" + p.Name}
	if repo := piRepoURL(p.Repository); repo != "" {
		m.Repo = repo
		if owner := githubOwner(repo); owner != "" {
			m.Icon = gh(owner)
		}
	}
	if strings.HasPrefix(p.Homepage, "https://") {
		m.Homepage = p.Homepage
	}
	if len(p.Date) >= 10 {
		m.Updated = p.Date[:10]
	}
	return m
}

// piRepoURL is a package's repository as a page to open: npm's
// git+https://…/x.git as https://…/x; nothing that isn't https.
func piRepoURL(s string) string {
	s = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(s), "git+"), ".git")
	if !strings.HasPrefix(s, "https://") {
		return ""
	}
	return s
}

// ---- install and remove -----------------------------------------------------

// piNameRe is an npm package's name (validate-npm-package-name): never one
// that starts with "-" or ".", which a command would read as a flag or a
// path.
var piNameRe = lazyre.New(`^(@[a-z0-9][a-z0-9._~-]*/)?[a-z0-9_][a-z0-9._~-]*$`)

// PiInstalled is what an install or a removal did, for the page to say.
type PiInstalled struct {
	*PiExtensions
	// Pending: only settings.json was changed; Pi installs the package
	// (or no longer loads it) when it next starts
	Pending bool `json:"pending,omitempty"`
}

// piBin is the pi command magpie runs for p, "" when it can't run one: a
// Pi in WSL, or one whose CLI isn't found; a var for tests.
var piBin = func(p piPlace) string {
	if p.a.WSL != "" {
		return ""
	}
	return p.a.BinPath()
}

// piRun runs `pi <args>` for the Pi whose agent folder is dir, with
// nothing to answer it; a var for tests.
var piRun = func(bin, dir string, args ...string) (string, error) {
	const timeout = 5 * time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := proc.CommandContext(ctx, bin, args...)
	cmd.Stdin = nil
	cmd.Dir = dir
	// pi is a node script: its folder first, so its node is found
	cmd.Env = append(os.Environ(), "PI_CODING_AGENT_DIR="+dir, "PATH="+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return text, fmt.Errorf("pi %s didn't finish in %s", strings.Join(args, " "), timeout)
		}
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("pi %s: %s", strings.Join(args, " "), lastLines(text, 4))
	}
	return text, nil
}

// InstallPiPackage installs an npm package from the market into a Pi.
func InstallPiPackage(pi, source string) (*PiInstalled, error) {
	name, ok := strings.CutPrefix(strings.TrimSpace(source), "npm:")
	if !ok || !piNameRe.MatchString(name) {
		return nil, fmt.Errorf("%q isn't an npm package Pi can install", source)
	}
	source = "npm:" + name
	piMu.Lock()
	defer piMu.Unlock()
	p, err := piPlaceOf(pi)
	if err != nil {
		return nil, err
	}
	s, err := p.settings()
	if err != nil {
		return nil, err
	}
	id := p.piIdentity(source)
	for _, r := range s.pkgs {
		if x, ok := piPackageOf(r); ok && p.piIdentity(x.Source) == id {
			return nil, fmt.Errorf("%s has %s already", piLabel(p), name)
		}
	}
	if err := newBackups().keep(p.a.ID, p.settingsPath()); err != nil {
		return nil, err
	}
	if bin := piBin(p); bin != "" {
		if _, err := piRun(bin, p.dir, "install", source); err != nil {
			return nil, err
		}
		if has, err := p.has(id); err != nil {
			return nil, err
		} else if !has {
			return nil, fmt.Errorf("pi install %s finished, but %s's settings.json doesn't list it", source, piLabel(p))
		}
		return &PiInstalled{PiExtensions: ReadPiExtensions()}, nil
	}
	b, _ := json.Marshal(source)
	next := append(slices.Clone(s.pkgs), b)
	if err := os.MkdirAll(p.dir, 0o755); err != nil {
		return nil, err
	}
	if err := edit.SetJSON(p.settingsPath(), edit.KV{Path: "packages", Value: next}); err != nil {
		return nil, err
	}
	return &PiInstalled{PiExtensions: ReadPiExtensions(), Pending: true}, nil
}

// RemovePiPackage takes a package out of a Pi, as `pi remove` does.
func RemovePiPackage(pi, source string) (*PiInstalled, error) {
	source = strings.TrimSpace(source)
	if source == "" || strings.HasPrefix(source, "-") {
		return nil, fmt.Errorf("%q isn't a package", source)
	}
	piMu.Lock()
	defer piMu.Unlock()
	p, err := piPlaceOf(pi)
	if err != nil {
		return nil, err
	}
	s, err := p.settings()
	if err != nil {
		return nil, err
	}
	id := p.piIdentity(source)
	var next []json.RawMessage
	for _, r := range s.pkgs {
		if x, ok := piPackageOf(r); ok && p.piIdentity(x.Source) == id {
			continue
		}
		next = append(next, r)
	}
	if len(next) == len(s.pkgs) {
		return nil, fmt.Errorf("%s has no package %s", piLabel(p), source)
	}
	if err := newBackups().keep(p.a.ID, p.settingsPath()); err != nil {
		return nil, err
	}
	if bin := piBin(p); bin != "" {
		if _, err := piRun(bin, p.dir, "remove", source); err != nil {
			return nil, err
		}
		if has, err := p.has(id); err != nil {
			return nil, err
		} else if has {
			return nil, fmt.Errorf("pi remove %s finished, but %s's settings.json still lists it", source, piLabel(p))
		}
		return &PiInstalled{PiExtensions: ReadPiExtensions()}, nil
	}
	if next == nil {
		next = []json.RawMessage{}
	}
	if err := edit.SetJSON(p.settingsPath(), edit.KV{Path: "packages", Value: next}); err != nil {
		return nil, err
	}
	return &PiInstalled{PiExtensions: ReadPiExtensions(), Pending: true}, nil
}

// has says whether p's settings.json lists a package of this identity;
// one that can't be read says nothing either way.
func (p piPlace) has(id string) (bool, error) {
	s, err := p.settings()
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(s.pkgs, func(r json.RawMessage) bool {
		x, ok := piPackageOf(r)
		return ok && p.piIdentity(x.Source) == id
	}), nil
}
