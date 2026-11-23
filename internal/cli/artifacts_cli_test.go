package cli

import (
	"bytes"
	"encoding/json"
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
	t.Cleanup(func() { artifact.Dir = old })
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
