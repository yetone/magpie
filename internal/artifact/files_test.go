package artifact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// files is {} or {"project": "<folder>"}, nothing else.
func TestParseFilesConfig(t *testing.T) {
	for _, bad := range []string{`[]`, `"x"`, `{"project":1}`, `{"project":"a\u0000b"}`, `{"project":"` + strings.Repeat("a", 4097) + `"}`} {
		if _, err := ParseCapabilities(json.RawMessage(`{"files":` + bad + `}`)); err == nil {
			t.Errorf("%s was taken", bad)
		}
	}
	for _, good := range []string{`{}`, `{"project":"/srv/app"}`, `{"project":"chan_01abc"}`} {
		if _, err := ParseCapabilities(json.RawMessage(`{"files":` + good + `}`)); err != nil {
			t.Errorf("%s: %v", good, err)
		}
	}
}

// The folder is the project's when that is an absolute path, else the
// folder the agent worked in; never none, a disk's root, the home folder
// or one holding it, nor a file.
func TestFilesDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	proj := filepath.Join(home, "proj")
	os.MkdirAll(proj, 0o755)
	other := t.TempDir()
	file := filepath.Join(other, "notes.txt")
	os.WriteFile(file, []byte("x"), 0o644)
	caps := func(raw string) Capabilities { return Capabilities{"files": json.RawMessage(raw)} }

	for _, c := range []struct {
		why  string
		caps Capabilities
		cwd  string
		want string
	}{
		{"no files", Capabilities{"db": json.RawMessage(`{}`)}, proj, ""},
		{"the agent's folder", caps(`{}`), proj, proj},
		{"Claude's project id", caps(`{"project":"chan_01abc"}`), proj, proj},
		{"a folder named", caps(`{"project":` + quote(other) + `}`), proj, other},
		{"a folder named, unclean", caps(`{"project":` + quote(proj+"/../proj/") + `}`), "", proj},
	} {
		got, err := FilesDir(c.caps, c.cwd)
		if err != nil || got != c.want {
			t.Errorf("%s: %q %v, want %q", c.why, got, err, c.want)
		}
	}
	link := filepath.Join(other, "home")
	if err := os.Symlink(home, link); err != nil {
		t.Skip("no symlinks here:", err)
	}
	root := "/"
	if filepath.VolumeName(home) != "" {
		root = filepath.VolumeName(home) + `\`
	}
	for _, c := range []struct {
		why  string
		caps Capabilities
		cwd  string
	}{
		{"no folder known", caps(`{}`), ""},
		{"a relative folder", caps(`{}`), "proj"},
		{"a disk's root", caps(`{"project":` + quote(root) + `}`), ""},
		{"the home folder", caps(`{}`), home},
		{"one holding home", caps(`{}`), filepath.Dir(home)},
		{"a file", caps(`{"project":` + quote(file) + `}`), ""},
		{"a folder that isn't there", caps(`{}`), filepath.Join(other, "gone")},
		{"home through a symlink", caps(`{"project":` + quote(link) + `}`), ""},
	} {
		if got, err := FilesDir(c.caps, c.cwd); err == nil {
			t.Errorf("%s: took %q", c.why, got)
		}
	}
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }

// A version keeps the folder it was published with. A publish with no
// folder of its own, as a page's publishing itself is, keeps the one
// before; one from an agent in another folder moves it; and a publish
// that can't name a folder is refused.
func TestFilesDirKeptWithTheVersion(t *testing.T) {
	sandbox(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	one, two := filepath.Join(home, "one"), filepath.Join(home, "two")
	os.MkdirAll(one, 0o755)
	os.MkdirAll(two, 0o755)
	files := Capabilities{"files": json.RawMessage(`{}`)}

	if _, err := PublishPage(Publish{Page: []byte("<p>1"), Capabilities: files}); err == nil {
		t.Fatal("published files with no folder")
	}
	a, err := PublishPage(Publish{Page: []byte("<p>1"), Capabilities: files, Cwd: one})
	if err != nil || a.Latest().FilesDir != one {
		t.Fatalf("%+v %v", a, err)
	}
	b, err := PublishPage(Publish{ID: a.ID, Page: []byte("<p>2"), Base: 1})
	if err != nil || b.Latest().FilesDir != one {
		t.Fatalf("the page's own publish: %+v %v", b.Latest(), err)
	}
	c, err := PublishPage(Publish{ID: a.ID, Page: []byte("<p>3"), Base: 2, Cwd: two})
	if err != nil || c.Latest().FilesDir != two {
		t.Fatalf("from another folder: %+v %v", c.Latest(), err)
	}
	d, err := PublishPage(Publish{ID: a.ID, Page: []byte("<p>4"), Base: 3, Capabilities: Capabilities{}})
	if err != nil || d.Latest().FilesDir != "" {
		t.Fatalf("cleared: %+v %v", d.Latest(), err)
	}
	if _, err := PublishPage(Publish{ID: a.ID, Page: []byte("<p>5"), Base: 4, Capabilities: files, Cwd: home}); err == nil {
		t.Fatal("published the home folder")
	}
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	if _, err := FilesDir(files, one); err == nil {
		t.Fatal("took a folder with no home folder to check it against")
	}
	again, err := Get(a.ID)
	if err != nil || again.Version(1).FilesDir != one || again.Version(3).FilesDir != two {
		t.Fatalf("read again: %v", err)
	}
}
