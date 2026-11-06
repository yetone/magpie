package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oldImport brings the skill in at the agent's entry at as the library
// did before dccfefdf7: linked to where it really was, Versions/<v> and all.
func oldImport(t *testing.T, name, at string) string {
	t.Helper()
	if _, err := ImportSkill(name); err != nil {
		t.Fatal(err)
	}
	real := realDir(at)
	if err := os.Remove(skillDir(name)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, skillDir(name)); err != nil {
		t.Fatal(err)
	}
	if _, err := change(func(l *Library) error { l.skill(name).Source.Dir = real; return nil }); err != nil {
		t.Fatal(err)
	}
	return real
}

func agentLink(t *testing.T, h, name, to string) string {
	t.Helper()
	shared := filepath.Join(h, ".agents/skills")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	at := filepath.Join(shared, name)
	if err := os.Symlink(to, at); err != nil {
		t.Skip("no symlinks here:", err)
	}
	return at
}

// tjhhhh on Discord: ego lite brought in before dccfefdf7 links into
// Versions/0.5.1.13; the update to 0.6.0.12 deletes that folder. Reading
// the library links it through Versions/Current, where the skill is now,
// and the agents have the new one through the library.
func TestOldImportFollowsDeletedVersion(t *testing.T) {
	h := sandbox(t)
	versions := egoLite(t, h, "0.5.1.13")
	at := agentLink(t, h, "ego-browser", filepath.Join(h, ".local/share/ego/ego-skills"))
	old := oldImport(t, "ego-browser", at)
	if !strings.Contains(old, "0.5.1.13") {
		t.Fatalf("the old import links to %s", old)
	}
	egoLite(t, h, "0.6.0.12")
	if err := os.RemoveAll(filepath.Join(versions, "0.5.1.13")); err != nil {
		t.Fatal(err)
	}
	if _, ok := readMeta(skillDir("ego-browser")); ok {
		t.Fatal("the old link still reads: the fixture didn't break it")
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Skills) != 1 || v.Skills[0].Missing {
		t.Fatalf("skills %+v", v.Skills)
	}
	if m, ok := readMeta(skillDir("ego-browser")); !ok || m.Description != "ego 0.6.0.12" {
		t.Errorf("the library's ego-browser is %q, want 0.6.0.12's", m.Description)
	}
	want := filepath.Join(realDir(versions), "Current/Resources/ego-skills/ego-browser")
	if to, _ := os.Readlink(skillDir("ego-browser")); filepath.Clean(to) != want {
		t.Errorf("links to %s, want %s, which the next update moves on too", to, want)
	}
	l, _ := load()
	if d := l.skill("ego-browser").Source.Dir; d != want {
		t.Errorf("source %s, want %s", d, want)
	}
	if m, ok := readMeta(at); !ok || m.Description != "ego 0.6.0.12" {
		t.Errorf("the agents' ego-browser is %q, want 0.6.0.12's", m.Description)
	}
	// and it then follows the next update by itself
	egoLite(t, h, "0.7.0.1")
	if err := os.RemoveAll(filepath.Join(versions, "0.6.0.12")); err != nil {
		t.Fatal(err)
	}
	if m, ok := readMeta(skillDir("ego-browser")); !ok || m.Description != "ego 0.7.0.1" {
		t.Errorf("after the next update it is %q", m.Description)
	}
}

// An update that leaves the old version behind: the library follows
// Versions/Current, now the new one, on a sync too.
func TestOldImportFollowsVersionsCurrent(t *testing.T) {
	h := sandbox(t)
	versions := egoLite(t, h, "0.5.1.13")
	at := agentLink(t, h, "ego-browser", filepath.Join(versions, "0.5.1.13/Resources/ego-skills/ego-browser"))
	oldImport(t, "ego-browser", at)
	egoLite(t, h, "0.6.0.12")
	if m, _ := readMeta(skillDir("ego-browser")); m.Description != "ego 0.5.1.13" {
		t.Fatalf("before a sync the library's is %q", m.Description)
	}
	if _, err := Sync(); err != nil {
		t.Fatal(err)
	}
	if m, ok := readMeta(skillDir("ego-browser")); !ok || m.Description != "ego 0.6.0.12" {
		t.Errorf("the library's ego-browser is %q, want 0.6.0.12's", m.Description)
	}
	// the old version is the app's to delete, not the library's
	if _, err := os.Stat(filepath.Join(versions, "0.5.1.13/Resources/ego-skills/ego-browser/SKILL.md")); err != nil {
		t.Errorf("the old version's folder: %v", err)
	}
}

// An app with no Current link: the highest version beside the gone one
// that has the same skill, by number (1.10 after 1.9).
func TestOldImportFollowsHighestVersion(t *testing.T) {
	h := sandbox(t)
	vs := filepath.Join(h, ".tool/versions")
	skill(t, filepath.Join(vs, "1.2.0/skills/review"), "review", "1.2.0")
	at := agentLink(t, h, "review", filepath.Join(vs, "1.2.0/skills/review"))
	oldImport(t, "review", at)
	skill(t, filepath.Join(vs, "1.9.0/skills/review"), "review", "1.9.0")
	skill(t, filepath.Join(vs, "1.10.0/skills/review"), "review", "1.10.0")
	skill(t, filepath.Join(vs, "2.0.0/other"), "other", "2.0.0") // no review here
	if err := os.RemoveAll(filepath.Join(vs, "1.2.0")); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(nil); err != nil {
		t.Fatal(err)
	}
	if m, ok := readMeta(skillDir("review")); !ok || m.Description != "1.10.0" {
		t.Errorf("the library's review is %q, want 1.10.0's", m.Description)
	}
}

// What isn't a version moved on from is left as it is: a folder of the
// library's own, and a link to a folder the user deleted, which shows as
// missing rather than being pointed at something else.
func TestMovedSkillsLeaveTheRest(t *testing.T) {
	h := sandbox(t)
	src := filepath.Join(h, "code/notes")
	skill(t, src, "notes", "Mine")
	agentLink(t, h, "notes", src)
	if _, err := ImportSkill("notes"); err != nil {
		t.Fatal(err)
	}
	skill(t, filepath.Join(h, "code/notes-2"), "notes", "Another")
	own := filepath.Join(h, ".claude/skills/own")
	skill(t, own, "own", "Moved in")
	if _, err := ImportSkill("own"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(src); err != nil {
		t.Fatal(err)
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	if to, err := os.Readlink(skillDir("notes")); err != nil || realBelow(to) != realBelow(src) {
		t.Errorf("notes links to %q (%v), want %s as it was", to, err, src)
	}
	for _, s := range v.Skills {
		if s.Name == "notes" && !s.Missing {
			t.Errorf("notes, deleted by the user, isn't shown missing")
		}
	}
	if fi, err := os.Lstat(skillDir("own")); err != nil || !fi.IsDir() {
		t.Errorf("the library's own folder: %v %v", fi, err)
	}
}

func TestVersionLess(t *testing.T) {
	for _, c := range [][2]string{{"1.9.0", "1.10.0"}, {"0.5.1.13", "0.6.0.12"}, {"v1.2", "v1.2.1"}, {"2.0.0-beta", "2.0.1"}} {
		if !versionLess(c[0], c[1]) || versionLess(c[1], c[0]) {
			t.Errorf("%s < %s", c[0], c[1])
		}
	}
}

// tjhhhh on Discord (11-06): ego-browser brought in since dccfefdf7 links
// to ego's own ~/.local/share/ego/ego-skills. ego's update to 0.6.2.7
// deleted Versions/0.6.2.5 and left active_version_dir (and Current)
// naming it, so the whole chain led nowhere. The library links to
// 0.6.2.7's folder without writing ego's links, and goes back to ego's
// link once the user (or ego) points active_version_dir at 0.6.2.7.
func TestSteadyLinkLeftOnDeletedVersion(t *testing.T) {
	h := sandbox(t)
	versions := egoLite(t, h, "0.6.2.5")
	steady := filepath.Join(h, ".local/share/ego/ego-skills")
	agentLink(t, h, "ego-browser", steady)
	if _, err := ImportSkill("ego-browser"); err != nil {
		t.Fatal(err)
	}
	if to, _ := os.Readlink(skillDir("ego-browser")); filepath.Clean(to) != steady {
		t.Fatalf("brought in linked to %s, want ego's %s", to, steady)
	}
	// the update: 0.6.2.7 in, 0.6.2.5 out, ego's links left as they were
	skill(t, filepath.Join(versions, "0.6.2.7/Resources/ego-skills/ego-browser"), "ego-browser", "ego 0.6.2.7")
	if err := os.RemoveAll(filepath.Join(versions, "0.6.2.5")); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(h, ".local/share/ego/active_version_dir")
	egoWas, _ := os.Readlink(active)
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Skills) != 1 || v.Skills[0].Missing {
		t.Fatalf("skills %+v", v.Skills)
	}
	if m, ok := readMeta(skillDir("ego-browser")); !ok || m.Description != "ego 0.6.2.7" {
		to, _ := os.Readlink(skillDir("ego-browser"))
		t.Errorf("the library's ego-browser is %q (links to %s), want 0.6.2.7's", m.Description, to)
	}
	if now, _ := os.Readlink(active); now != egoWas {
		t.Errorf("ego's active_version_dir was written: %s, was %s", now, egoWas)
	}
	l, _ := load()
	if src := l.skill("ego-browser").Source; src.Via != steady {
		t.Errorf("source %+v, want ego's link %s kept as Via", src, steady)
	}
	// ego's link put right: the library links through it again
	os.Remove(active)
	if err := os.Symlink(filepath.Join(versions, "0.6.2.7"), active); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(); err != nil {
		t.Fatal(err)
	}
	if to, _ := os.Readlink(skillDir("ego-browser")); filepath.Clean(to) != steady {
		t.Errorf("after ego's link is mended the library links to %s, want %s", to, steady)
	}
	l, _ = load()
	if src := l.skill("ego-browser").Source; src.Dir != steady || src.Via != "" {
		t.Errorf("source %+v, want Dir %s and no Via", src, steady)
	}
}

// With no other version on disk the dangling skill stays linked as it was
// and is shown missing.
func TestSteadyLinkLeftOnDeletedVersionNoOther(t *testing.T) {
	h := sandbox(t)
	versions := egoLite(t, h, "0.6.2.5")
	steady := filepath.Join(h, ".local/share/ego/ego-skills")
	agentLink(t, h, "ego-browser", steady)
	if _, err := ImportSkill("ego-browser"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(versions, "0.6.2.5")); err != nil {
		t.Fatal(err)
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Skills) != 1 || !v.Skills[0].Missing {
		t.Errorf("skills %+v, want ego-browser shown missing", v.Skills)
	}
	if to, _ := os.Readlink(skillDir("ego-browser")); filepath.Clean(to) != steady {
		t.Errorf("links to %s, want %s as it was", to, steady)
	}
}
