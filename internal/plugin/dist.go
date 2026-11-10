package plugin

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/source"
	"github.com/yetone/magpie/internal/update"
)

// The community repo's dist branch: every version of each
// @magpie-community package, packed by the repo's publish workflow
// (scripts/dist.mjs there) as soon as it is merged. npm can't take a new
// package's first version without an owner's 2FA (trusted publishing sets
// up only a package that exists), so a reviewed plugin used to wait,
// unmerged, for someone to publish it by hand. magpie asks npm first, as
// before, and turns here only for what npm doesn't give: a package or
// version npm doesn't have, or npm not answering. A tarball is installed
// only when its sha512 is the one index.json names.

// distBase is where the branch is read; moved by tests. Read through the
// 「国内镜像」 switch's jsDelivr copy first when it is on (source.DoFaithful).
var distBase = "https://raw.githubusercontent.com/magpie-community/plugins/dist"

// distScope is the scope of the packages the branch has.
const distScope = "@magpie-community/"

type distVersion struct {
	File      string          `json:"file"`      // tgz/<npm pack's file name>
	Integrity string          `json:"integrity"` // sha512-<base64>, as npm writes it
	Manifest  json.RawMessage `json:"manifest"`  // the version's package.json
}

type distPackage struct {
	Latest   string                 `json:"latest"`
	Readme   string                 `json:"readme"`
	Versions map[string]distVersion `json:"versions"`
}

type distIndex struct {
	Packages map[string]distPackage `json:"packages"`
}

var (
	distMu   sync.Mutex
	distIdx  *distIndex
	distAt   time.Time // when distIdx was read, or last failed to be
	distOK   bool      // whether the last read was distIdx's
	distErr  error     // why the last read failed, when it did
	distKeep = 10 * time.Minute
	distWait = time.Minute // after a read failed, before asking again
	// distReading is closed when the read under way ends, nil when none is
	distReading chan struct{}
	// distReadFor is how long a read of the index is given: its own
	// time, not one package's ask's. index.json is ~1.6MB, which a slow
	// link took longer than npmEach's 6s to bring, and a read cut off
	// there said the branch had nothing (ferock on Discord: the Chat
	// page plugin, on the dist branch only, showed Coming soon). A read
	// that ends after its asker stopped waiting is kept for the next ask.
	distReadFor = 2 * time.Minute
)

// errDistUnread is a package the dist branch may have, unknown for the
// branch not having been read.
var errDistUnread = errors.New("the community repo's dist branch can't be read")

// distFile is a file the index may name: one tarball under tgz/, nothing
// that leaves the branch or plugins/dist.
var distFile = lazyre.New(`^tgz/[A-Za-z0-9._-]+\.tgz$`)

// distPkg is what the dist branch has of the package: nil with no error
// when the branch was read and has none, or the package isn't one of its
// scope; an error wrapping errDistUnread when that isn't known, as the
// branch couldn't be read (a failed read is not "it has none"). The read
// goes on apart from ctx, and ctx ending is waited for no longer: what
// was read last answers, and a package it doesn't have is unknown.
func distPkg(ctx context.Context, name string) (*distPackage, error) {
	if !strings.HasPrefix(name, distScope) || distBase == "" {
		return nil, nil
	}
	distMu.Lock()
	stale := distIdx == nil || time.Since(distAt) > distKeep
	if stale && distReading == nil && (distOK || distAt.IsZero() || time.Since(distAt) > distWait) {
		distReading = make(chan struct{})
		go readDist(context.WithoutCancel(ctx), distReading)
	}
	reading := distReading
	distMu.Unlock()
	var waited error
	if reading != nil {
		select {
		case <-reading:
		case <-ctx.Done():
			waited = ctx.Err()
		}
	}
	distMu.Lock()
	defer distMu.Unlock()
	if distIdx != nil {
		if p, ok := distIdx.Packages[name]; ok {
			return &p, nil
		}
	}
	switch {
	case waited != nil:
		return nil, fmt.Errorf("%w: %w", errDistUnread, waited)
	case !distOK && distErr != nil:
		return nil, fmt.Errorf("%w: %w", errDistUnread, distErr)
	case !distOK:
		return nil, errDistUnread
	}
	return nil, nil
}

// readDist reads the index into distIdx, closing done when it has: the
// last read kept until one works. ctx gives it values only, not its time.
func readDist(ctx context.Context, done chan struct{}) {
	c, cancel := context.WithTimeout(ctx, distReadFor)
	idx, err := readDistIndex(c)
	cancel()
	distMu.Lock()
	if err == nil {
		distIdx, distOK, distErr = idx, true, nil
	} else {
		// what was read last stays, until a read works
		distOK, distErr = false, err
	}
	distAt = time.Now()
	distReading = nil
	distMu.Unlock()
	close(done)
}

var (
	// distMirror is the copy of the branch's file at u asked before it,
	// the 「国内镜像」 switch's jsDelivr, "" when none is (moved by tests)
	distMirror = source.ChinaFirst
	// distGrace is how much longer GitHub's own index.json is waited for
	// once the mirror's has come: where GitHub can't be reached, it isn't
	// waited for to the end of the request's time
	distGrace = 5 * time.Second
)

// readDistIndex reads the branch's index.json. jsDelivr's copy, asked first
// with the 「国内镜像」 switch on, is the branch as it was when the edge
// cached it, up to 12 hours before (s-maxage=43200): a Gcore edge served
// CodeArts' 0.2.3 for hours after 0.2.4 was packed, and with npm not having
// the package at all, the hourly update, Check for updates and Update each
// found nothing newer and the page said every plugin was up to date
// (star21321321124 on Discord). So GitHub's own copy is asked beside it and
// the two are merged (mergeDist). Each version still comes with the
// sha512 its own copy of the branch names, which installDist checks the
// tarball against, as it did. Where GitHub can't be reached, the mirror's
// alone is read, as before.
func readDistIndex(ctx context.Context) (*distIndex, error) {
	u := distBase + "/index.json"
	m := distMirror(u)
	if m == "" {
		return parseDistIndex(fetchJSONFrom(ctx, u, 32<<20, source.DoOfficial))
	}
	type read struct {
		idx *distIndex
		err error
	}
	octx, cancel := context.WithCancel(ctx)
	defer cancel()
	official, mirror := make(chan read, 1), make(chan read, 1)
	go func() {
		idx, err := parseDistIndex(fetchJSONFrom(octx, u, 32<<20, source.DoOfficial))
		official <- read{idx, err}
	}()
	go func() {
		idx, err := parseDistIndex(fetchJSONFrom(ctx, m, 32<<20, source.DoOfficial))
		mirror <- read{idx, err}
	}()
	mr := <-mirror
	var or read
	if mr.err != nil {
		or = <-official
	} else {
		t := time.NewTimer(distGrace)
		select {
		case or = <-official:
		case <-t.C:
			cancel()
			or = <-official
		}
		t.Stop()
	}
	switch {
	case or.err != nil && mr.err != nil:
		return nil, errors.Join(or.err, mr.err)
	case or.err != nil:
		return mr.idx, nil
	case mr.err != nil:
		return or.idx, nil
	}
	return mergeDist(or.idx, mr.idx), nil
}

func parseDistIndex(b []byte, err error) (*distIndex, error) {
	if err != nil {
		return nil, err
	}
	var idx distIndex
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, err
	}
	return &idx, nil
}

// mergeDist is two copies of the branch's index as one: every version
// either names (the branch only ever gains versions), a's entry where both
// do, and each package's newer latest, with the README of the copy that
// named it.
func mergeDist(a, b *distIndex) *distIndex {
	out := &distIndex{Packages: map[string]distPackage{}}
	for name, p := range a.Packages {
		out.Packages[name] = p
	}
	for name, pb := range b.Packages {
		pa, ok := out.Packages[name]
		if !ok {
			out.Packages[name] = pb
			continue
		}
		vs := make(map[string]distVersion, len(pa.Versions)+len(pb.Versions))
		for v, x := range pb.Versions {
			vs[v] = x
		}
		for v, x := range pa.Versions {
			vs[v] = x
		}
		p := pa
		if update.Newer(pb.Latest, pa.Latest) {
			p.Latest, p.Readme = pb.Latest, pb.Readme
		}
		p.Versions = vs
		out.Packages[name] = p
	}
	return out
}

// distInfo is the package's newest version on the dist branch, as npm's
// /latest would say it, and whether there is one; an error (distPkg's)
// when that isn't known.
func distInfo(ctx context.Context, name string) (NPM, bool, error) {
	d, err := distPkg(ctx, name)
	if d == nil {
		return NPM{}, false, err
	}
	v, ok := d.Versions[d.Latest]
	if !ok {
		return NPM{}, false, nil
	}
	var l npmLatest
	if json.Unmarshal(v.Manifest, &l) != nil {
		return NPM{}, false, nil
	}
	info := npmOf(l)
	info.Version = d.Latest
	return info, true, nil
}

// distWanted is the package and version an npm spec asks for, when the
// dist branch could serve it: a @magpie-community package, at a version
// or "latest" (not a range or another tag).
func distWanted(spec string) (name, version string, ok bool) {
	if IsPath(spec) || IsGit(spec) {
		return "", "", false
	}
	name = Name(spec)
	if !strings.HasPrefix(name, distScope) || !pkgName.MatchString(name) {
		return "", "", false
	}
	version = strings.TrimPrefix(strings.TrimPrefix(spec, name), "@")
	if version == "" {
		version = "latest"
	}
	if version != "latest" && !exactVersion.MatchString(version) {
		return "", "", false
	}
	return name, version, true
}

var exactVersion = lazyre.New(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

// installDist installs the package's version from the dist branch: its
// tarball fetched, checked against the index's sha512, kept in
// plugins/dist (bun.lock names it there, and a later bun add installs it
// again from it), and added with bun. installMu held.
func installDist(ctx context.Context, bun, name, version string) error {
	d, err := distPkg(ctx, name)
	if err != nil {
		return err
	}
	if d == nil {
		return fmt.Errorf("the community repo's dist branch has no %s", name)
	}
	if version == "latest" {
		version = d.Latest
	}
	v, ok := d.Versions[version]
	if !ok {
		return fmt.Errorf("the community repo's dist branch has no %s@%s", name, version)
	}
	if !distFile.MatchString(v.File) {
		return fmt.Errorf("the community repo's dist branch names %q for %s@%s, not a tarball of its own", v.File, name, version)
	}
	b, err := fetchJSONFaithful(ctx, distBase+"/"+v.File, 64<<20)
	if err != nil {
		return err
	}
	sum := sha512.Sum512(b)
	if got := "sha512-" + base64.StdEncoding.EncodeToString(sum[:]); got != v.Integrity {
		return fmt.Errorf("%s from the community repo's dist branch isn't the tarball its index names (sha512 %s, not %s)", v.File, got, v.Integrity)
	}
	dir := filepath.Join(Dir(), "dist")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	file := path.Base(v.File)
	tmp, err := os.CreateTemp(dir, file+".*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), filepath.Join(dir, file))
	}
	if werr != nil {
		os.Remove(tmp.Name())
		return werr
	}
	return bunAdd(ctx, bun, "./dist/"+file)
}

// pruneDist takes out the tarballs in plugins/dist that neither
// plugins/package.json nor bun.lock's packages has a package at: one an
// update from npm, or a newer one, took the place of. Bun installs a
// package from the tarball the lock's packages has it at, which
// package.json can have moved on from (#3622), so that one stays too.
// package.json is read as text, every mention counting: Bun 1.4.0 adds a
// tarball's package again instead of replacing it, and of the two keys
// encoding/json takes the second where Bun takes the first, so the tarball
// taken out was the one Bun installs from. A tarball only the lock's
// workspaces names (Bun 1.4.2 and 1.4.3 keep the one replaced there, and
// drop it on their next command) goes. installMu held. One it can't tell
// about (package.json, or a bun.lock that is there, unreadable) stays.
func pruneDist() {
	pj, err := os.ReadFile(filepath.Join(Dir(), "package.json"))
	if err != nil {
		return
	}
	lock, err := os.ReadFile(filepath.Join(Dir(), "bun.lock"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return
	}
	locked := map[string]bool{}
	for _, m := range distLockedAt.FindAllSubmatch(lockHead(lock), -1) {
		locked[string(m[2])] = true
	}
	ents, err := os.ReadDir(filepath.Join(Dir(), "dist"))
	if err != nil {
		return
	}
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".tgz") && !locked[e.Name()] && !distNamed(pj, e.Name()) {
			os.Remove(filepath.Join(Dir(), "dist", e.Name()))
		}
	}
}

// distNamed is whether package.json b names the tarball plugins/dist/file
// anywhere: "./dist/file" (the spec magpie hands Bun) or "file:dist/file".
func distNamed(b []byte, file string) bool {
	return bytes.Contains(b, []byte("dist/"+file+`"`))
}

// distLockedAt is a package bun.lock's packages has at a tarball in
// plugins/dist, and that tarball: "<name>": ["<name>@./dist/<file>", …].
var distLockedAt = lazyre.New(`\["((?:@[^"/@]+/)?[^"/@]+)@(?:file:)?(?:\./)?dist/([A-Za-z0-9._-]+\.tgz)"`)

// mendDist sends bun.lock back to package.json for the packages it has at a
// dist tarball package.json doesn't name, when package.json has them at one
// plugins/dist holds. Bun installs what the lock has, at every command that
// changes plugins/: from that tarball, which pruneDist keeps, or from its
// copy of it, it put the older version back, and with neither it failed
// every install, update and removal, of any plugin, with "ENOENT extracting
// tarball from <it>". Bun 1.4.4 keys its copies by the tarball's sha512, so
// it has none of those 1.4.2 and 1.4.3 made (#3622, gkd2323c on Windows,
// Bun 1.4.4: autoclaw, codearts and cloudflare-workers-ai were locked at
// tarballs pruneDist had taken, while package.json and the lock's
// workspaces had moved on). A `bun update` of them all at once resolves
// them from package.json again: `bun add` doesn't on Bun 1.4.2 and 1.4.3,
// which keep what the lock has, and with no copy a package left out fails
// the update with it. A package.json that names the lock's tarball too (Bun
// 1.4.0 names a package twice) isn't behind. Each mend is logged, as
// nothing here says why a lock falls behind. installMu held.
func mendDist(ctx context.Context, bun string) {
	lock, err := os.ReadFile(filepath.Join(Dir(), "bun.lock"))
	if err != nil {
		return
	}
	b, err := os.ReadFile(filepath.Join(Dir(), "package.json"))
	if err != nil {
		return
	}
	var pj struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if json.Unmarshal(b, &pj) != nil {
		return
	}
	var stale []string
	for _, m := range distLockedAt.FindAllSubmatch(lockHead(lock), -1) {
		name, locked := string(m[1]), string(m[2])
		file, ok := distSpec(pj.Dependencies[name])
		if ok && file != locked && distHas(file) == nil && !distNamed(b, locked) && !slices.Contains(stale, name) {
			stale = append(stale, name)
		}
	}
	if len(stale) > 0 {
		// what this can't mend, the command it comes before says
		out, err := bunCommand(ctx, bun, Dir(), append([]string{"update", "--ignore-scripts"}, stale...)...).CombinedOutput()
		if err != nil {
			log.Printf("plugins: bun.lock behind package.json for %s; bun update: %v: %s", strings.Join(stale, ", "), err, lastLines(string(out), 3))
		} else {
			log.Printf("plugins: bun.lock behind package.json for %s, sent back", strings.Join(stale, ", "))
		}
	}
}

// lockHead is bun.lock's first object, what Bun reads of it: Bun goes on
// past bytes left after it (tried on 1.4.4), which a shorter lock written
// over a longer one without truncating leaves, as package.json had
// (plugins#405). Read as packages, such a tail's old tarballs would be
// kept and sent to bun update at every command. A lock with no whole
// object is read as it is.
func lockHead(b []byte) []byte {
	depth, inStr, esc := 0, false, false
	for i, c := range b {
		switch {
		case esc:
			esc = false
		case inStr:
			esc = c == '\\'
			inStr = c != '"'
		case c == '"':
			inStr = true
		case c == '{' || c == '[':
			depth++
		case c == '}' || c == ']':
			depth--
			if depth == 0 {
				return b[:i+1]
			}
		}
	}
	return b
}

// distSpec is the tarball in plugins/dist a package.json dependency is at.
func distSpec(spec string) (string, bool) {
	s := filepath.ToSlash(strings.TrimPrefix(spec, "file:"))
	return strings.CutPrefix(strings.TrimPrefix(s, "./"), "dist/")
}

// distHas is os.Stat's answer for plugins/dist/file.
func distHas(file string) error {
	_, err := os.Stat(filepath.Join(Dir(), "dist", file))
	return err
}
