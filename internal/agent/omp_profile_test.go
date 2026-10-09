package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ompProfileAt writes a profile's config.yml with the model of its default
// role, and answers the folder it is in.
func ompProfileAt(t *testing.T, home, name, model string) string {
	t.Helper()
	dir := filepath.Join(home, ".omp", "profiles", name, "agent")
	writeFile(t, filepath.Join(dir, "config.yml"), "modelRoles:\n  default: "+model+"\n")
	return dir
}

// Named omp profiles under ~/.omp/profiles are agents of their own: each
// has its own config.yml and models.yml, shares omp's model lists, and
// leaves the default row's files alone. The default row is still the one
// ompDir picks, and still answers to omp's alias.
func TestOmpProfiles(t *testing.T) {
	home := syncHome(t)
	def := filepath.Join(home, ".omp", "agent")
	writeFile(t, filepath.Join(def, "config.yml"), "modelRoles:\n  default: a/model-a\n")
	work := ompProfileAt(t, home, "work", "b/model-b")
	lab := ompProfileAt(t, home, "lab", "c/model-c")

	for _, id := range []string{"omp", "omp#work", "omp#lab"} {
		if _, err := Find(id); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	w, err := Find("omp#work")
	if err != nil {
		t.Fatal(err)
	}
	if w.Name != "omp · work" || w.Icon != "omp" || w.ListsFor() != "omp" {
		t.Fatalf("work: %+v", w)
	}
	if !w.Detected() || w.Dir != work {
		t.Fatalf("work dir %q, detected %v", w.Dir, w.Detected())
	}
	if f := w.Field("model"); f == nil || f.Get() != "b/model-b" {
		t.Fatalf("work model %v", f)
	}

	before, labBefore := readFile(filepath.Join(def, "config.yml")), readFile(filepath.Join(lab, "config.yml"))
	if err := w.Field("model").Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if readFile(filepath.Join(work, "config.yml")) == "modelRoles:\n  default: b/model-b\n" {
		t.Fatal("work's config wasn't written")
	}
	if readFile(filepath.Join(def, "config.yml")) != before {
		t.Fatalf("the default row's config moved:\n%s", readFile(filepath.Join(def, "config.yml")))
	}
	if readFile(filepath.Join(lab, "config.yml")) != labBefore {
		t.Fatalf("lab's config moved:\n%s", readFile(filepath.Join(lab, "config.yml")))
	}

	d, err := Find("omp")
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "omp" || d.Dir != def || d.ListsFor() != "omp" {
		t.Fatalf("default: %+v", d)
	}
	if f := d.Field("model"); f.Get() != "a/model-a" {
		t.Fatalf("default model %q", f.Get())
	}
	if a, err := Find("oh-my-pi"); err != nil || a.ID != "omp" {
		t.Fatalf("oh-my-pi: %v %v", a, err)
	}
}

// A profile name omp refuses ("Bad", "work.", a Windows reserved device
// name) is no profile, "default" is the default row, and a folder that
// isn't a directory is no profile either. One whose agent folder has no
// config.yml yet is still a row: the first Set makes the file, and only
// that profile's. "work." is not made as a folder: Windows drops the
// trailing dot, so MkdirAll("work.") would create "work" and list it.
func TestOmpProfilesNameRules(t *testing.T) {
	home := syncHome(t)
	root := filepath.Join(home, ".omp")
	for _, name := range []string{"Bad", "default"} {
		if err := os.MkdirAll(filepath.Join(root, "profiles", name, "agent"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// a file where a profile folder would be
	writeFile(t, filepath.Join(root, "profiles", "notes"), "not a profile\n")
	fresh := filepath.Join(root, "profiles", "fresh")
	if err := os.MkdirAll(fresh, 0o755); err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, a := range ompProfiles(home) {
		got[a.ID] = true
	}
	if len(got) != 1 || !got["omp#fresh"] {
		t.Fatalf("profiles %v", got)
	}
	f, err := Find("omp#fresh")
	if err != nil {
		t.Fatal(err)
	}
	if !f.Detected() {
		t.Fatal("a profile folder is not detected")
	}
	if err := f.Field("model").Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fresh, "agent", "config.yml")); err != nil {
		t.Fatalf("fresh config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "agent", "config.yml")); !os.IsNotExist(err) {
		t.Fatalf("the default row's config was made: %v", err)
	}
	for _, name := range []string{"work.", "aux", "nul", "com1", "lpt9.x", "con.work", "COM1"} {
		if ompProfileOK(name) {
			t.Fatalf("%q is a profile", name)
		}
	}
	if d := parseProbe("U", "home:/home/me\nprofile:omp:aux\nprofile:omp:con.work\nprofile:omp:work.\n"); d == nil || len(d.OmpProfiles) != 0 {
		t.Fatalf("probe kept a refused name: %+v", d)
	}
}

// OMP_PROFILE points the default row at profiles/<name>/agent, and that
// profile is not listed a second time; PI_CONFIG_DIR renames the root the
// profiles are read under.
func TestOmpProfilesDefaultRowAndConfigDir(t *testing.T) {
	home := syncHome(t)
	work := ompProfileAt(t, home, "work", "b/model-b")
	ompProfileAt(t, home, "lab", "c/model-c")

	t.Setenv("OMP_PROFILE", "work")
	d, err := Find("omp")
	if err != nil {
		t.Fatal(err)
	}
	if d.Dir != work {
		t.Fatalf("default Dir %q", d.Dir)
	}
	got := map[string]bool{}
	for _, a := range ompProfiles(home) {
		got[a.ID] = true
	}
	if got["omp#work"] {
		t.Fatal("the row's own profile is listed twice")
	}
	if !got["omp#lab"] {
		t.Fatalf("profiles %v", got)
	}

	// PI_CONFIG_DIR moves the root, profiles and all
	t.Setenv("OMP_PROFILE", "")
	os.Unsetenv("OMP_PROFILE")
	t.Setenv("PI_CONFIG_DIR", ".omp-work")
	renamed := filepath.Join(home, ".omp-work", "profiles", "home", "agent")
	writeFile(t, filepath.Join(renamed, "config.yml"), "modelRoles:\n  default: h/model-h\n")
	if _, err := Find("omp#home"); err != nil {
		t.Fatal(err)
	}
	if a, err := Find("omp"); err != nil || a.Dir != filepath.Join(home, ".omp-work", "agent") {
		t.Fatalf("default under PI_CONFIG_DIR: %v %v", a, err)
	}
}

// The probe prints a named omp profile as profile:omp:<name>, and only
// real directories under ~/.omp/profiles count. A profile row's place
// takes omp's version, which the probe keeps under the bare omp kind.
func TestParseProbeOmpProfiles(t *testing.T) {
	d := parseProbe("U", "home:/home/me\ndir:.omp\nver:omp omp/16.5.1\n"+
		"profile:omp:work\nprofile:omp:Bad\nprofile:omp:work.\nprofile:pi:x\nprofile:omp:work\n")
	if d == nil {
		t.Fatal("no distro")
	}
	if len(d.OmpProfiles) != 1 || d.OmpProfiles[0] != "work" {
		t.Fatalf("profiles %v", d.OmpProfiles)
	}
	if v := d.place("omp#work@wsl:U").version; v != "16.5.1" {
		t.Fatalf("profile place version %q", v)
	}
	if !strings.Contains(wslProbeScript, `for d in "$HOME"/.omp/profiles/*`) ||
		!strings.Contains(wslProbeScript, `echo "profile:omp:$(basename "$d")"`) {
		t.Fatalf("the probe doesn't list profiles: %s", wslProbeScript)
	}
}

// A distro's profile names come from its probe, not from a Windows
// ReadDir: a running distro lists omp#work@wsl:<distro> even though the
// fake root has no profile folder, and picking its model writes that
// profile's files alone.
func TestWSLOmpProfilesFromProbe(t *testing.T) {
	syncHome(t)
	root := t.TempDir()
	dhome := filepath.Join(root, "home", "me")
	defCfg := filepath.Join(dhome, ".omp", "agent", "config.yml")
	writeFile(t, defCfg, "modelRoles:\n  default: a/model-a\n")
	t.Cleanup(FakeWSL(
		map[string]string{"Ubuntu": "home:/home/me\ndir:.omp\nver:omp omp/18.4.8\nprofile:omp:work\nprofile:omp:Bad\n"},
		map[string]string{"Ubuntu": root}))

	if _, err := os.Stat(filepath.Join(dhome, ".omp", "profiles")); !os.IsNotExist(err) {
		t.Fatalf("the fake root has a profiles folder: %v", err)
	}
	var a *Agent
	for _, x := range wslAgents() {
		if x.ID == "omp#work@wsl:Ubuntu" {
			a = x
		}
	}
	if a == nil {
		t.Fatal("no omp#work@wsl:Ubuntu")
	}
	if a.Name != "omp · work · WSL Ubuntu" || !a.Detected() || a.ListsFor() != "omp" {
		t.Fatalf("%+v", a)
	}
	if a.Dir != filepath.Join(dhome, ".omp", "profiles", "work", "agent") {
		t.Fatalf("Dir %q", a.Dir)
	}
	before := readFile(defCfg)
	if err := a.Field("model").Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	workCfg := filepath.Join(dhome, ".omp", "profiles", "work", "agent", "config.yml")
	if !strings.Contains(readFile(workCfg), "magpie/relay/glm-4.6") {
		t.Fatalf("work config:\n%s", readFile(workCfg))
	}
	if readFile(defCfg) != before {
		t.Fatalf("the default distro omp moved:\n%s", readFile(defCfg))
	}
	// the profile's own models.yml, not the default row's
	if _, err := os.Stat(filepath.Join(dhome, ".omp", "agent", "models.yml")); !os.IsNotExist(err) {
		t.Fatalf("the default row's models.yml was written: %v", err)
	}
}

// A stopped distro is never started to list its profiles: the names the
// probe remembered are still there, the row shows as asleep with no file
// read, and nothing of the distro is opened.
func TestWSLOmpProfilesStopped(t *testing.T) {
	syncHome(t)
	root := t.TempDir()
	dhome := filepath.Join(root, "home", "me")
	t.Cleanup(FakeWSL(
		map[string]string{"Ubuntu": "home:/home/me\ndir:.omp\nver:omp omp/18.4.8\nprofile:omp:work\n"},
		map[string]string{"Ubuntu": root}))
	if len(wslAgents()) == 0 {
		t.Fatal("nothing listed while running")
	}
	StopFakeWSL("Ubuntu")
	// the profile folder is gone: a stopped distro's row reads no file
	if err := os.RemoveAll(filepath.Join(dhome, ".omp")); err != nil {
		t.Fatal(err)
	}
	var a *Agent
	for _, x := range wslAgents() {
		if x.ID == "omp#work@wsl:Ubuntu" {
			a = x
		}
	}
	if a == nil {
		t.Fatal("the profile row was dropped with the distro")
	}
	if a.Path != "" || a.Dir != "" {
		t.Fatalf("asleep row reads files: Path %q Dir %q", a.Path, a.Dir)
	}
	if !a.Detected() {
		t.Fatal("the remembered profile isn't detected")
	}
	if !strings.Contains(a.Notice(), "isn't running") {
		t.Fatalf("notice %q", a.Notice())
	}
}
