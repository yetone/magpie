package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// agency is CHENGJIAHEGOXIS's leftover: a repository with no package.json,
// in plugins/package.json from an add that failed, on no plugin's entry.
const agency = "agency-agents"

// depsSandbox is offSandbox with plugins/package.json's dependencies and
// node_modules as given (a folder each, with a package.json for those
// with a version), and plugins.json as list.
func depsSandbox(t *testing.T, deps map[string]string, list string) {
	t.Helper()
	offSandbox(t)
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := setDeps(deps); err != nil {
		// no package.json yet: setDeps reads one first
		if err := os.WriteFile(filepath.Join(Dir(), "package.json"), []byte(`{"name":"magpie-plugins","private":true}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := setDeps(deps); err != nil {
			t.Fatal(err)
		}
	}
	for n, s := range deps {
		at := filepath.Join(Dir(), "node_modules", filepath.FromSlash(n))
		if err := os.MkdirAll(at, 0o755); err != nil {
			t.Fatal(err)
		}
		if IsGit(s) {
			// agency-agents: markdown, no package.json
			if err := os.WriteFile(filepath.Join(at, "README.md"), []byte("# agents\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		v := strings.TrimPrefix(strings.TrimPrefix(s, n), "@")
		if err := os.WriteFile(filepath.Join(at, "package.json"), []byte(`{"name":"`+n+`","version":"`+v+`","main":"index.js"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(at, "index.js"), []byte("export default async () => ({})\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeList(t, list)
}

func depsOf(t *testing.T) map[string]string {
	t.Helper()
	d, err := pkgDeps()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func ctxMinute(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// The reporter's case: a dependency no plugin is installed as, which Bun
// can't install again (a file of its copy held open on Windows), failed
// every add after it. It is taken out, and the add goes on.
func TestLeftoverDoesNotFailAnotherAdd(t *testing.T) {
	depsSandbox(t, map[string]string{agency: "github:msitarzewski/agency-agents"}, `{"plugins":[]}`)
	t.Setenv("MAGPIE_FAKE_BUN_BUSY", agency)
	const spec = "opencode-goany-auth@1.0.0"
	if _, err := AddExact(ctxMinute(t), spec); err != nil {
		t.Fatalf("add failed over a leftover no plugin needs: %v", err)
	}
	if !slices.ContainsFunc(Load().Plugins, func(e Entry) bool { return e.Spec == spec }) {
		t.Fatalf("%s not listed: %+v", spec, Load().Plugins)
	}
	d := depsOf(t)
	if _, in := d[agency]; in {
		t.Fatalf("the leftover is still a dependency: %v", d)
	}
	if d["opencode-goany-auth"] != spec {
		t.Fatalf("the plugin isn't a dependency: %v", d)
	}
	if l := Leftovers(); len(l) != 0 {
		t.Fatalf("leftovers after: %+v", l)
	}
}

// A failed add takes out what Bun wrote into package.json for it: else it
// is a leftover that every bun command installs again.
func TestFailedAddLeavesNoDependency(t *testing.T) {
	depsSandbox(t, map[string]string{}, `{"plugins":[]}`)
	t.Setenv("MAGPIE_FAKE_BUN_BUSY", "opencode-broken-auth")
	if _, err := AddExact(ctxMinute(t), "opencode-broken-auth@1.0.0"); err == nil {
		t.Fatal("an add Bun failed succeeded")
	}
	if d := depsOf(t); len(d) != 0 {
		t.Fatalf("a failed add left %v in package.json", d)
	}
	if _, err := os.Stat(filepath.Join(Dir(), "node_modules", "opencode-broken-auth")); !os.IsNotExist(err) {
		t.Fatalf("its folder stays: %v", err)
	}
	if len(Load().Plugins) != 0 {
		t.Fatalf("listed: %+v", Load().Plugins)
	}
}

// Another plugin's package Bun can't install fails no other add once that
// one is in: the other plugin keeps its dependency (it is listed), and
// its own row says what's wrong with it.
func TestAnotherPluginsFailureDoesNotFailAdd(t *testing.T) {
	const other = "opencode-other-auth"
	depsSandbox(t, map[string]string{other: other + "@1.0.0"}, `{"plugins":[{"spec":"`+other+`@1.0.0"}]}`)
	t.Setenv("MAGPIE_FAKE_BUN_BUSY", other)
	const spec = "opencode-goany-auth@1.0.0"
	if _, err := AddExact(ctxMinute(t), spec); err != nil {
		t.Fatalf("add failed over another plugin's package: %v", err)
	}
	d := depsOf(t)
	if d[other] != other+"@1.0.0" {
		t.Fatalf("a listed plugin's dependency was taken out: %v", d)
	}
	if d["opencode-goany-auth"] != spec {
		t.Fatalf("the plugin added isn't a dependency: %v", d)
	}
}

// goanyapi is CHENGJIAHEGOXIS's plugin: not on npm yet, so it is added
// from the community repo's dist branch.
const goanyapi = "@magpie-community/tools-goanyapi"

// goanyapiBusy is what Bun 1.4.4 said adding GoAnyAPI's dist tarball on
// the reporter's Windows (v0.1.4205's error, its lines as Bun prints
// them): "Unexpected:", not "EBUSY:", and GoAnyAPI itself installed.
const goanyapiBusy = "bun add v1.4.4 (38afee6c8)\nResolving dependencies\nResolved, downloaded and extracted [1]\n" +
	"Unexpected: failed copying files from cache to destination for package agency-agents\n\n" +
	"installed @magpie-community/tools-goanyapi@./dist/magpie-community-tools-goanyapi-0.1.1.tgz\n\n" +
	"Failed to install 2 packages\n[38.00ms] done\n"

// goanyapiSandbox is depsSandbox with npm having no GoAnyAPI and the dist
// branch its 0.1.1, and Bun failing over busy as the reporter's did.
func goanyapiSandbox(t *testing.T, deps map[string]string, list, busy string) {
	t.Helper()
	depsSandbox(t, deps, list)
	npmHasNothing(t)
	fakeDist(t, map[string][]string{goanyapi: {"0.1.1"}}, false)
	t.Setenv("MAGPIE_FAKE_BUN_NPM404", goanyapi)
	t.Setenv("MAGPIE_FAKE_BUN_BUSY", busy)
	t.Setenv("MAGPIE_FAKE_BUN_BUSY_SAYS", strings.ReplaceAll(goanyapiBusy, "agency-agents", busy))
}

// goanyapiUnnamed is what Bun 1.4.4 said adding GoAnyAPI's dist tarball
// on the reporter's Windows on v0.1.4210, its lines as the error showed
// them: GoAnyAPI installed, a package failed, and the line naming it was
// cut off by the error's last-lines (its header lines are Bun's own).
const goanyapiUnnamed = "bun add v1.4.4 (38afee6c8)\nResolving dependencies\nResolved, downloaded and extracted [1]\n" +
	"Saved lockfile\n\n" +
	"installed @magpie-community/tools-goanyapi@./dist/magpie-community-tools-goanyapi-0.1.1.tgz\n\n" +
	"1 package removed [26.00ms]\nFailed to install 1 package\n"

// The reporter's case on v0.1.4210: from the dist branch, GoAnyAPI went
// in at the version asked and Bun failed over a package in words magpie
// doesn't read. The add is done: Bun names a package of its own it
// couldn't install.
func TestUnnamedFailureDoesNotFailADistAdd(t *testing.T) {
	goanyapiSandbox(t, nil, `{"plugins":[]}`, goanyapi)
	t.Setenv("MAGPIE_FAKE_BUN_BUSY_SAYS", goanyapiUnnamed)
	if _, err := AddExact(ctxMinute(t), goanyapi+"@latest"); err != nil {
		t.Fatalf("add from dist failed with GoAnyAPI in: %v", err)
	}
	if d := depsOf(t); d[goanyapi] != "./dist/magpie-community-tools-goanyapi-0.1.1.tgz" {
		t.Fatalf("GoAnyAPI isn't a dependency at its dist tarball: %v", d)
	}
	if !slices.ContainsFunc(Load().Plugins, func(e Entry) bool { return Name(e.Spec) == goanyapi }) {
		t.Fatalf("GoAnyAPI not listed: %+v", Load().Plugins)
	}
}

// A failure that names the add's own package still fails it, and so does
// one that names none when the package isn't in at the version asked.
func TestOthersFailedNeedsTheAddsOwnVersion(t *testing.T) {
	depsSandbox(t, map[string]string{goanyapi: "./dist/magpie-community-tools-goanyapi-0.1.1.tgz", "opencode-other-auth": "opencode-other-auth@1.0.0"}, `{"plugins":[]}`)
	at := filepath.Join(Dir(), "node_modules", filepath.FromSlash(goanyapi))
	if err := os.MkdirAll(at, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(at, "package.json"), []byte(`{"name":"`+goanyapi+`","version":"0.1.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	unnamed := &bunRunError{errors.New("bun add"), goanyapiUnnamed}
	own := &bunRunError{errors.New("bun add"), "EBUSY: failed copying files from cache to destination for package " + goanyapi}
	other := &bunRunError{errors.New("bun add"), "EBUSY: failed copying files from cache to destination for package opencode-other-auth"}
	spec := goanyapi + "@latest"
	for _, c := range []struct {
		err  error
		want string
		ok   bool
	}{
		{unnamed, "0.1.0", true},
		{unnamed, "0.1.1", false}, // an older copy is not the add's
		{unnamed, "", false},      // no version to check it against
		{own, "0.1.0", false},
		{other, "0.1.0", true},
		{other, "0.1.1", false},
		{other, "", true},
	} {
		if got := othersFailed(c.err, spec, c.want); got != c.ok {
			t.Errorf("othersFailed(%q, want %q) = %v, want %v", c.err.(*bunRunError).out, c.want, got, c.ok)
		}
	}
}

// A failed bun add's error says which package Bun couldn't install: the
// line naming it came before the last lines, which alone were shown.
func TestBunAddErrorSaysWhatFailed(t *testing.T) {
	out := "bun add v1.4.4 (38afee6c8)\r\nResolving dependencies\r\nResolved, downloaded and extracted [1]\r\n" +
		"Unexpected: failed copying files from cache to destination for package opencode-held-auth\r\n" +
		goanyapiUnnamed[strings.Index(goanyapiUnnamed, "Saved"):]
	got := bunSaid(out, 6)
	for _, w := range []string{"for package opencode-held-auth", "installed " + goanyapi, "Failed to install 1 package"} {
		if !strings.Contains(got, w) {
			t.Errorf("bunSaid = %q, missing %q", got, w)
		}
	}
	if strings.Contains(got, "\n\n") || strings.Contains(got, "\r") || strings.Contains(got, "Resolving") {
		t.Errorf("bunSaid = %q: blank lines, CRs or lines before the failure", got)
	}
}

// A leftover Bun names in another of its ways is taken out too.
func TestLeftoverNamedOtherwiseIsTakenOut(t *testing.T) {
	goanyapiSandbox(t, map[string]string{agency: "github:msitarzewski/agency-agents"}, `{"plugins":[]}`, agency)
	t.Setenv("MAGPIE_FAKE_BUN_BUSY_SAYS", "bun add v1.4.4 (38afee6c8)\nerror: Failed to open node_modules folder for agency-agents in C:\\p\\node_modules\nFailed to install 1 package\n")
	if _, err := AddExact(ctxMinute(t), goanyapi+"@latest"); err != nil {
		t.Fatalf("add failed over a leftover: %v", err)
	}
	if _, in := depsOf(t)[agency]; in {
		t.Fatalf("the leftover is still a dependency: %v", depsOf(t))
	}
}

// The reporter's case on v0.1.4205: GoAnyAPI isn't on npm, and its add
// from the dist branch failed over the leftover, which only the npm add
// was cleared of. Every add has it: the dist branch's too.
func TestLeftoverDoesNotFailADistAdd(t *testing.T) {
	goanyapiSandbox(t, map[string]string{agency: "github:msitarzewski/agency-agents"}, `{"plugins":[]}`, agency)
	const spec = goanyapi + "@latest"
	if _, err := AddExact(ctxMinute(t), spec); err != nil {
		t.Fatalf("add from dist failed over a leftover no plugin needs: %v", err)
	}
	d := depsOf(t)
	if _, in := d[agency]; in {
		t.Fatalf("the leftover is still a dependency: %v", d)
	}
	if d[goanyapi] != "./dist/magpie-community-tools-goanyapi-0.1.1.tgz" {
		t.Fatalf("GoAnyAPI isn't a dependency at its dist tarball: %v", d)
	}
	if !slices.ContainsFunc(Load().Plugins, func(e Entry) bool { return Name(e.Spec) == goanyapi }) {
		t.Fatalf("GoAnyAPI not listed: %+v", Load().Plugins)
	}
}

// Another listed plugin's package Bun can't install fails no add from the
// dist branch either, once the add's own package went in ("installed
// …"): what Bun said there is read, not only the npm try's 404.
func TestAnotherPluginsFailureDoesNotFailADistAdd(t *testing.T) {
	const other = "opencode-other-auth"
	goanyapiSandbox(t, map[string]string{other: other + "@1.0.0"}, `{"plugins":[{"spec":"`+other+`@1.0.0"}]}`, other)
	if _, err := AddExact(ctxMinute(t), goanyapi+"@latest"); err != nil {
		t.Fatalf("add from dist failed over another plugin's package: %v", err)
	}
	d := depsOf(t)
	if d[other] != other+"@1.0.0" {
		t.Fatalf("a listed plugin's dependency was taken out: %v", d)
	}
	if d[goanyapi] == "" {
		t.Fatalf("GoAnyAPI isn't a dependency: %v", d)
	}
}

// Removing a plugin takes its dependency out though bun remove fails
// (which installs every other dependency again, and one of them can fail
// it): ignored, it stayed, a leftover.
func TestRemoveTakesDependencyOutWhenBunFails(t *testing.T) {
	const p = "opencode-gone-auth"
	depsSandbox(t, map[string]string{p: p + "@1.0.0", "opencode-kept-auth": "opencode-kept-auth@1.0.0"},
		`{"plugins":[{"spec":"`+p+`@1.0.0"},{"spec":"opencode-kept-auth@1.0.0"}]}`)
	t.Setenv("MAGPIE_FAKE_BUN_NOREMOVE", "1")
	if err := Remove(ctxMinute(t), p); err != nil {
		t.Fatal(err)
	}
	d := depsOf(t)
	if _, in := d[p]; in {
		t.Fatalf("still a dependency after Remove: %v", d)
	}
	if d["opencode-kept-auth"] == "" {
		t.Fatalf("another plugin's dependency went too: %v", d)
	}
	if _, err := os.Stat(filepath.Join(Dir(), "node_modules", p)); !os.IsNotExist(err) {
		t.Fatalf("its folder stays: %v", err)
	}
	if l := Leftovers(); len(l) != 0 {
		t.Fatalf("leftovers: %+v", l)
	}
}

// Leftovers lists what no plugin is installed as, and RemoveLeftover takes
// out only that.
func TestLeftoversListedAndRemoved(t *testing.T) {
	const p = "opencode-kept-auth"
	depsSandbox(t, map[string]string{agency: "github:msitarzewski/agency-agents", p: p + "@1.0.0"}, `{"plugins":[{"spec":"`+p+`@1.0.0","off":true}]}`)
	l := Leftovers()
	if len(l) != 1 || l[0].Name != agency || l[0].Spec != "github:msitarzewski/agency-agents" || l[0].Missing {
		t.Fatalf("leftovers = %+v", l)
	}
	if err := RemoveLeftover(ctxMinute(t), p); err == nil {
		t.Fatal("removed a plugin's dependency (one turned off) as a leftover")
	}
	// Bun can't copy it again either: the remove goes on by hand
	t.Setenv("MAGPIE_FAKE_BUN_BUSY", agency)
	t.Setenv("MAGPIE_FAKE_BUN_NOREMOVE", "1")
	if err := RemoveLeftover(ctxMinute(t), agency); err != nil {
		t.Fatal(err)
	}
	d := depsOf(t)
	if _, in := d[agency]; in || d[p] == "" {
		t.Fatalf("dependencies after = %v", d)
	}
	if l := Leftovers(); len(l) != 0 {
		t.Fatalf("leftovers after = %+v", l)
	}
}

// plugins.json there and unreadable: which dependencies are plugins is
// unknown, and none is a leftover (none taken out, none offered).
func TestNoLeftoversWhenListUnreadable(t *testing.T) {
	depsSandbox(t, map[string]string{"opencode-kept-auth": "opencode-kept-auth@1.0.0"}, `{"plugins":[`)
	if l := Leftovers(); len(l) != 0 {
		t.Fatalf("leftovers with plugins.json unreadable = %+v", l)
	}
	if err := RemoveLeftover(ctxMinute(t), "opencode-kept-auth"); err == nil {
		t.Fatal("removed a dependency with plugins.json unreadable")
	}
	if gone := dropBroken(&bunRunError{err: os.ErrInvalid, out: "EBUSY: failed copying files from cache to destination for package opencode-kept-auth"}); len(gone) != 0 {
		t.Fatalf("dropped %v with plugins.json unreadable", gone)
	}
}

// Bun's words for a package it couldn't install, as 1.4.2 prints them
// (colour codes and its <b>/<r> tags too).
func TestBunFailed(t *testing.T) {
	for out, want := range map[string][]string{
		"EBUSY: failed copying files from cache to destination for package agency-agents":                                                             {agency},
		"\x1b[31merror\x1b[0m: failed copying files from cache to destination for package <b>@scope/pkg<r>\n":                                         {"@scope/pkg"},
		"EACCES: Permission denied while installing opencode-x-auth\nEBUSY: failed copying files from cache to destination for package agency-agents": {"opencode-x-auth", agency},
		"error: GET https://registry.npmjs.org/x - 404":                                                                                               nil,
		goanyapiBusy: {agency},
		strings.ReplaceAll(goanyapiBusy, "\n", "\r\n"): {agency},
		goanyapiUnnamed: nil,
		// each other way Bun 1.4.4 names a package it couldn't install
		"error: Failed to open node_modules folder for \x1b[31m@scope/pkg\x1b[0m in C:\\x\\node_modules\r\n":    {"@scope/pkg"},
		"error: failed to install <b>agency-agents<r>: the downloaded package was not found in the cache":       {agency},
		"error: failed to link <b>opencode-x-auth<r>: EPERM":                                                    {"opencode-x-auth"},
		"error: failed to link into C:\\x\\node_modules\\.bin: EPERM":                                           nil,
		"ENAMETOOLONG: folder path for package <b>agency-agents<r> is too long":                                 {agency},
		"error: refusing to install dependency <b>evil<r> with unsafe folder path \"../x\"":                     {"evil"},
		"error: GET https://codeload.github.com/a/b - 404 downloading tarball <b>agency-agents@github:a/b#1<r>": {agency},
		"error: failed to download <b>@scope/pkg@1.0.0<r>":                                                      {"@scope/pkg"},
		"error: ENOENT extracting tarball from <b>opencode-x-auth<r>":                                           {"opencode-x-auth"},
		"error: Unexpected extracting tarball for \"@scope/pkg\": bad (at byte 1 of 2)":                         {"@scope/pkg"},
		"Failed to install 1 package": nil,
	} {
		if got := bunFailed(out); !slices.Equal(got, want) {
			t.Errorf("bunFailed(%q) = %v, want %v", out, got, want)
		}
	}
}
