package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/artifact"
)

// Work › Artifacts lists what agents published, newest first with each
// version's own address, and Delete takes one away with every version; one
// deleted already is gone as asked.
func TestArtifactsAPI(t *testing.T) {
	dir := t.TempDir()
	was := artifact.Dir
	artifact.Dir = func() string { return dir }
	t.Cleanup(func() { artifact.Dir = was })
	a, err := artifact.PublishPage(artifact.Publish{Page: []byte("<title>Report</title>one"), Agent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := artifact.PublishPage(artifact.Publish{Page: []byte("<title>Plan</title>x")})
	// the report's update makes it the newest
	if _, err := artifact.PublishPage(artifact.Publish{ID: a.ID, Page: []byte("two"), Label: "fixed totals"}); err != nil {
		t.Fatal(err)
	}

	srv := webPageHandler(t, webHost{func() {}}, nil)
	list := func() []artifactJSON {
		rw := httptest.NewRecorder()
		srv.ServeHTTP(rw, httptest.NewRequest("GET", "/api/artifacts", nil))
		var out struct{ Artifacts []artifactJSON }
		if rw.Code != http.StatusOK || json.Unmarshal(rw.Body.Bytes(), &out) != nil {
			t.Fatalf("/api/artifacts: %d %s", rw.Code, rw.Body)
		}
		return out.Artifacts
	}
	got := list()
	if len(got) != 2 {
		t.Fatalf("listed %+v", got)
	}
	// newest first (two published in one millisecond go by id)
	if got[0].Updated.Before(got[1].Updated) {
		t.Fatalf("not newest first: %+v", got)
	}
	r := got[0]
	if r.ID != a.ID {
		r = got[1]
	}
	if r.ID != a.ID {
		t.Fatalf("no report in %+v", got)
	}
	if r.Title != "Report" || !strings.HasSuffix(r.URL, "/a/"+a.ID+"/") || len(r.Versions) != 2 {
		t.Fatalf("the report %+v", r)
	}
	if v := r.Versions[0]; v.N != 2 || v.Label != "fixed totals" || !strings.HasSuffix(v.URL, "/a/"+a.ID+"@2/") {
		t.Fatalf("newest version %+v", v)
	}
	if v := r.Versions[1]; v.N != 1 || v.Agent != "codex" || v.AgentName != "Codex" || !strings.HasSuffix(v.URL, "/a/"+a.ID+"@1/") {
		t.Fatalf("first version %+v", v)
	}

	del := func(id string) int {
		rw := httptest.NewRecorder()
		srv.ServeHTTP(rw, httptest.NewRequest("POST", "/api/artifacts/delete", strings.NewReader(`{"id":"`+id+`"}`)))
		return rw.Code
	}
	if c := del(a.ID); c != http.StatusOK {
		t.Fatalf("delete: %d", c)
	}
	if got := list(); len(got) != 1 || got[0].ID != b.ID {
		t.Fatalf("after delete %+v", got)
	}
	if _, err := artifact.Get(a.ID); err == nil {
		t.Fatal("the deleted artifact is still there")
	}
	if c := del(a.ID); c != http.StatusOK {
		t.Fatalf("deleting it again: %d", c)
	}
}
