package plugin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// An agent plugin whose agent needs a runtime (a poller, files watched)
// ships a Bun module as its main beside its moejs agent file. It loads
// and adds its agent, but the GitHub list read a package with a main as
// a provider and showed it under Providers (miaocell, plugins#737). A
// package that declares magpie.agent is an agent plugin, main or not;
// the file looked for in the repository is still the one Bun loads, its
// main. The package.json, module and agent file are
// miaocell/magpie-agent-deva's, as they are; it isn't on npm.
func TestTaggedAgentWithMain(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		asked = append(asked, q.URL.Path)
		mu.Unlock()
		name, ok := strings.CutPrefix(q.URL.Path, "/raw/")
		f, err := os.ReadFile(filepath.Join("testdata/github/raw", filepath.FromSlash(name)))
		if !ok || err != nil {
			http.Error(w, `"Not Found"`, 404)
			return
		}
		w.Write(f)
	}))
	t.Cleanup(srv.Close)
	api, raw, reg := githubAPI, githubRaw, npmRegistry
	githubAPI, githubRaw, npmRegistry = srv.URL, srv.URL+"/raw", srv.URL+"/npm"
	t.Cleanup(func() { githubAPI, githubRaw, npmRegistry = api, raw, reg })

	const repo = "miaocell/magpie-agent-deva"
	got, keep, known, err := readTagged(context.Background(), Tagged{Spec: "github:" + repo, Repo: repo}, "main")
	if !keep || !known || err != nil || got.Kind != "agent" || got.Package != "magpie-agent-deva" || got.Spec != "github:"+repo {
		t.Fatalf("an agent with a main: kind %q, package %q, spec %q, kept %v, known %v, %v", got.Kind, got.Package, got.Spec, keep, known, err)
	}
	mu.Lock()
	a := slices.Clone(asked)
	mu.Unlock()
	if !slices.Contains(a, "/raw/"+repo+"/main/index.mjs") {
		t.Fatalf("its main wasn't looked for: asked %v", a)
	}

	// an agent alone, no main, as before: its agent file is the one
	// looked for
	got, keep, _, err = readTagged(context.Background(), Tagged{Spec: "github:someone/agent-aider", Repo: "someone/agent-aider"}, "main")
	if !keep || err != nil || got.Kind != "agent" {
		t.Fatalf("an agent alone: %+v, kept %v, %v", got, keep, err)
	}
}
