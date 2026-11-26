package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/artifact"
)

func artifactsSandbox(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	old := artifact.Dir
	artifact.Dir = func() string { return dir }
	t.Cleanup(func() { artifact.CloseDBs(); artifact.Dir = old })
}

// `magpie artifacts` does what Work › Artifacts does: lists them newest
// first with who made each, opens one (a version too), and deletes one
// with every version.
func TestArtifactsCommand(t *testing.T) {
	artifactsSandbox(t)
	var opened []string
	old := openArtifact
	openArtifact = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openArtifact = old })

	run := func(args ...string) (string, error) {
		var b bytes.Buffer
		err := artifactsTo(&b, args, time.Now())
		return b.String(), err
	}
	if out, err := run(); err != nil || !strings.Contains(out, "Nothing published yet") {
		t.Fatalf("empty list: %q %v", out, err)
	}
	a, err := artifact.PublishPage(artifact.Publish{Page: []byte("<title>Q3 report</title>"), Agent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := artifact.PublishPage(artifact.Publish{ID: a.ID, Page: []byte("<title>Q3 report</title>2"), Agent: "codex"}); err != nil {
		t.Fatal(err)
	}
	b, _ := artifact.PublishPage(artifact.Publish{Page: []byte("<title>Old one</title>"), Agent: "some-agent"})

	out, err := run()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Q3 report  Codex · just now · 2 versions") || !strings.Contains(out, "/a/"+a.ID+"/") ||
		!strings.Contains(out, "Old one  some-agent") {
		t.Fatalf("list:\n%s", out)
	}
	var rows []struct {
		ID       string `json:"id"`
		Versions []struct {
			N   int    `json:"n"`
			URL string `json:"url"`
		} `json:"versions"`
	}
	out, _ = run("--json")
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 2 {
		t.Fatalf("--json: %v\n%s", err, out)
	}
	if rows[0].ID != b.ID || len(rows[1].Versions) != 2 || rows[1].Versions[0].N != 2 || !strings.HasSuffix(rows[1].Versions[1].URL, "/a/"+a.ID+"@1/") {
		t.Fatalf("--json rows: %+v", rows)
	}

	if _, err := run("open", a.ID+"@1"); err != nil || len(opened) != 1 || !strings.HasSuffix(opened[0], "/a/"+a.ID+"@1/") {
		t.Fatalf("open @1: %v %v", err, opened)
	}
	if _, err := run("open", a.ID+"@3"); err == nil || len(opened) != 1 {
		t.Fatalf("a version it hasn't opened: %v %v", err, opened)
	}
	for _, bad := range [][]string{{"open"}, {"open", "nope"}, {"delete", a.ID + "@1"}, {"--help-me"}} {
		if _, err := run(bad...); err == nil {
			t.Errorf("%v was taken", bad)
		}
	}
	if _, err := artifact.Get(a.ID); err != nil {
		t.Fatalf("a refused delete deleted it: %v", err)
	}
	if out, err := run("delete", "http://127.0.0.1:3425/a/"+a.ID+"/"); err != nil || !strings.Contains(out, "2 versions") {
		t.Fatalf("delete by address: %q %v", out, err)
	}
	if _, err := artifact.Get(a.ID); err == nil {
		t.Fatal("still there after delete")
	}
	if _, err := run("delete", a.ID); err == nil {
		t.Fatal("deleted what was gone")
	}
}

// `magpie artifacts share` prints the link the list then shows, and
// unshare takes it down; neither takes a version.
func TestArtifactsShareCommand(t *testing.T) {
	artifactsSandbox(t)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/owner") {
			w.Write([]byte(`{"code":"c0de","url":"https://usemagpie.ai/a/abcdefghijklmnop#owner=c0de"}`))
			return
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":"abcdefghijklmnop","url":"https://usemagpie.ai/a/abcdefghijklmnop","token":"tok","version":1}`))
			return
		}
		w.Write([]byte(`{"deleted":true}`))
	}))
	t.Cleanup(site.Close)
	t.Setenv("MAGPIE_SHARE_SITE", site.URL)
	run := func(args ...string) (string, error) {
		var b bytes.Buffer
		err := artifactsTo(&b, args, time.Now())
		return b.String(), err
	}
	a, _ := artifact.PublishPage(artifact.Publish{Page: []byte("<title>Plan</title>")})
	const link = "https://usemagpie.ai/a/abcdefghijklmnop"
	if _, err := run("unshare", a.ID); err == nil {
		t.Fatal("unshared one never shared")
	}
	var opened []string
	was := openArtifact
	openArtifact = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openArtifact = was })
	if _, err := run("open-shared", a.ID); err == nil || !strings.Contains(err.Error(), "isn't shared") || len(opened) != 0 {
		t.Fatalf("open-shared, not shared: %v %v", err, opened)
	}
	if _, err := run("share", a.ID+"@1"); err == nil {
		t.Fatal("shared a version")
	}
	if out, err := run("share", a.ID); err != nil || !strings.Contains(out, link) {
		t.Fatalf("share: %q %v", out, err)
	}
	if out, err := run("open-shared", a.ID); err != nil || !strings.Contains(out, "as its owner") || len(opened) != 1 || opened[0] != link+"#owner=c0de" {
		t.Fatalf("open-shared: %q %v %v", out, err, opened)
	}
	if out, _ := run(); !strings.Contains(out, "shared: "+link) {
		t.Fatalf("list:\n%s", out)
	}
	if out, _ := run("--json"); !strings.Contains(out, `"shared": "`+link+`"`) {
		t.Fatalf("--json:\n%s", out)
	}
	if out, err := run("unshare", a.ID); err != nil || !strings.Contains(out, "isn't shared any more") {
		t.Fatalf("unshare: %q %v", out, err)
	}
	if out, _ := run(); strings.Contains(out, "shared:") {
		t.Fatalf("still listed as shared:\n%s", out)
	}
}
