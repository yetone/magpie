package artifact

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sandbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := Dir
	Dir = func() string { return dir }
	t.Cleanup(func() { Dir = old })
	return dir
}

func read(t *testing.T, id string, n int, name string) string {
	t.Helper()
	p, _, err := File(id, n, name)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPublishKeepsEveryVersion(t *testing.T) {
	sandbox(t)
	a, err := PublishPage(Publish{Page: []byte("<!doctype html><title>Q3 &amp; Q4</title><p>one"), Title: "ignored",
		Files: map[string][]byte{"style.css": []byte("p{}"), "img/a.svg": []byte("<svg/>")}})
	if err != nil {
		t.Fatal(err)
	}
	if !ValidID(a.ID) || a.Title != "Q3 & Q4" || a.Latest().N != 1 {
		t.Fatalf("made %+v", a)
	}
	// an update that leaves a file out keeps it; nil takes one away
	b, err := PublishPage(Publish{ID: a.ID, Page: []byte("<!doctype html><p>two"), Base: 1,
		Files: map[string][]byte{"img/a.svg": nil, "data.json": []byte("[]")}})
	if err != nil {
		t.Fatal(err)
	}
	if b.ID != a.ID || b.Latest().N != 2 || b.Title != "Q3 & Q4" {
		t.Fatalf("updated %+v", b)
	}
	if got := strings.Join(b.Latest().Files, ","); got != "data.json,style.css" {
		t.Fatalf("version 2 holds %s", got)
	}
	if read(t, a.ID, 1, "") != "<!doctype html><title>Q3 &amp; Q4</title><p>one" || read(t, a.ID, 0, Page) != "<!doctype html><p>two" {
		t.Fatal("the pages aren't each version's own")
	}
	if read(t, a.ID, 2, "style.css") != "p{}" || read(t, a.ID, 1, "img/a.svg") != "<svg/>" {
		t.Fatal("a file wasn't kept")
	}
	if _, _, err := File(a.ID, 2, "img/a.svg"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a removed file is still served: %v", err)
	}
	// with no page, the one before is kept
	c, err := PublishPage(Publish{ID: a.ID, Files: map[string][]byte{"style.css": []byte("p{color:red}")}})
	if err != nil {
		t.Fatal(err)
	}
	if read(t, c.ID, 3, "") != "<!doctype html><p>two" || read(t, c.ID, 2, "style.css") != "p{}" || read(t, c.ID, 3, "style.css") != "p{color:red}" {
		t.Fatal("version 3 isn't version 2 with the new style")
	}
}

func TestPublishOverNewerVersionRefused(t *testing.T) {
	sandbox(t)
	a, _ := PublishPage(Publish{Page: []byte("one")})
	PublishPage(Publish{ID: a.ID, Page: []byte("two")})
	if _, err := PublishPage(Publish{ID: a.ID, Page: []byte("mine"), Base: 1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a publish built on version 1 went over version 2: %v", err)
	}
	if got := read(t, a.ID, 0, ""); got != "two" {
		t.Fatalf("the page is %q", got)
	}
}

func TestPathsStayInside(t *testing.T) {
	sandbox(t)
	for _, p := range []string{"../x", "/etc/passwd", `..\x`, "a/../../x", "C:/x", ".git/config", "a/.env", "index.html", "", "a\x00b"} {
		if _, err := PublishPage(Publish{Page: []byte("p"), Files: map[string][]byte{p: []byte("x")}}); err == nil {
			t.Errorf("published a file at %q", p)
		}
	}
	a, _ := PublishPage(Publish{Page: []byte("p"), Files: map[string][]byte{`sub\x.css`: []byte("x")}})
	if a == nil || a.Latest().Files[0] != "sub/x.css" {
		t.Fatalf("a backslash path wasn't kept as sub/x.css: %+v", a)
	}
	for _, name := range []string{"../meta.json", "../../" + a.ID + "/meta.json", "/" + a.ID} {
		if _, _, err := File(a.ID, 0, name); err == nil {
			t.Errorf("served %q", name)
		}
	}
	if _, _, err := File("../x", 0, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("an id that climbs out was read: %v", err)
	}
}

func TestListAndDelete(t *testing.T) {
	dir := sandbox(t)
	a, _ := PublishPage(Publish{Page: []byte("a"), Title: "First"})
	b, _ := PublishPage(Publish{Page: []byte("b"), Title: "Second"})
	PublishPage(Publish{ID: a.ID, Page: []byte("a2")})
	// a folder with no meta.json (a publish cut short) isn't listed
	os.MkdirAll(filepath.Join(dir, strings.Repeat("a", 26)), 0o700)
	l, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(l) != 2 || l[0].ID != a.ID || l[1].ID != b.ID || l[0].Title != "First" {
		t.Fatalf("listed %+v", l)
	}
	if err := Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, a.ID)); !os.IsNotExist(err) {
		t.Fatal("the artifact's folder is still there")
	}
	if _, err := Get(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got a deleted artifact: %v", err)
	}
	if err := Delete(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted it twice: %v", err)
	}
}

func TestLimits(t *testing.T) {
	sandbox(t)
	if _, err := PublishPage(Publish{Page: bytes.Repeat([]byte("x"), MaxPage+1)}); err == nil {
		t.Error("published a page over the limit")
	}
	if _, err := PublishPage(Publish{Page: []byte{0xff, 0xfe}}); err == nil {
		t.Error("published a page that isn't UTF-8")
	}
	many := map[string][]byte{}
	for i := 0; i <= MaxFiles; i++ {
		many[strings.Repeat("f", 1+i%5)+string(rune('a'+i%26))+"/"+string(rune('a'+i/26))+".txt"] = []byte("x")
	}
	if _, err := PublishPage(Publish{Page: []byte("p"), Files: many}); err == nil {
		t.Error("published more files than a version holds")
	}
	if _, err := PublishPage(Publish{Page: []byte("p"), Files: map[string][]byte{"a": []byte("1"), "a/b": []byte("2")}}); err == nil {
		t.Error("published a file and a folder of one name")
	}
	if _, err := PublishPage(Publish{ID: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("updated an artifact that isn't there: %v", err)
	}
}

func TestServedWrapsFragmentsOnly(t *testing.T) {
	whole := []byte("<!DOCTYPE html><html><body>x</body></html>")
	if !bytes.Equal(Served(whole), whole) {
		t.Error("a page with its doctype was changed")
	}
	if got := string(Served([]byte("<!-- made by --> <!doctype html><p>x"))); got != "<!-- made by --> <!doctype html><p>x" {
		t.Errorf("a doctype after a comment was missed: %q", got)
	}
	frag := string(Served([]byte("<h1>Hi</h1>")))
	for _, want := range []string{"<!doctype html>", `<meta charset="utf-8">`, `name="viewport"`, "<h1>Hi</h1>", "<title>"} {
		if !strings.Contains(frag, want) {
			t.Errorf("a fragment served without %s: %s", want, frag)
		}
	}
	if got := string(Served([]byte("<html><body>x"))); !strings.HasPrefix(got, "<!doctype html>\n<html>") {
		t.Errorf("a page with <html> but no doctype: %q", got)
	}
}

func TestTitles(t *testing.T) {
	sandbox(t)
	a, _ := PublishPage(Publish{Page: []byte("<p>no title"), Title: "  Sales\n report  "})
	if a.Title != "Sales report" {
		t.Errorf("title %q", a.Title)
	}
	b, _ := PublishPage(Publish{ID: a.ID, Page: []byte("<p>still none")})
	if b.Title != "Sales report" {
		t.Errorf("an update with no title lost the one before: %q", b.Title)
	}
	c, _ := PublishPage(Publish{Page: []byte("x")})
	if c.Title != "Untitled" {
		t.Errorf("title %q", c.Title)
	}
}
