package plugin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// installedAt fakes the package installed at version v.
func installedAt(t *testing.T, pkg, v string) {
	t.Helper()
	dir := Target(pkg)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]string{"name": pkg, "version": v})
	if err := os.WriteFile(filepath.Join(dir, "package.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The community's plugins update by themselves; anyone else's, or one
// pinned to a version, waits for the reader, and waits no more once
// updated; one switched off is left alone.
func TestCheckUpdates(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	const (
		ours   = "@magpie-community/opencode-zed-auth"
		pinned = "@magpie-community/opencode-kiro-auth"
		theirs = "opencode-gemini-auth"
		off    = "@magpie-community/opencode-qoder-auth"
		fresh  = "@magpie-community/opencode-grok-auth"
	)
	if err := save(List{Plugins: []Entry{{Spec: ours + "@latest"}, {Spec: pinned + "@0.1.2"}, {Spec: theirs + "@latest"}, {Spec: off + "@latest", Off: true}, {Spec: fresh}}}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{ours, pinned, theirs, off} {
		installedAt(t, p, "0.1.2")
	}
	installedAt(t, fresh, "0.2.0")
	latest := map[string]string{ours: "0.1.4", pinned: "0.1.4", theirs: "1.0.0", off: "0.1.4", fresh: "0.2.0"}
	var installed []string
	was, wasInstall := latestOf, installNewest
	t.Cleanup(func() { latestOf, installNewest = was, wasInstall })
	latestOf = func(context.Context, []string) map[string]string { return latest }
	installNewest = func(_ context.Context, pkg, _ string) error {
		installed = append(installed, pkg)
		installedAt(t, pkg, latest[pkg])
		return nil
	}

	u, err := CheckUpdates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(installed, ",") != ours {
		t.Fatalf("installed %v, want only %s", installed, ours)
	}
	if Installed(ours+"@latest") != "0.1.4" || len(u.Updated) != 1 || u.Updated[0].From != "0.1.2" || u.Updated[0].To != "0.1.4" {
		t.Fatalf("Updated = %+v", u.Updated)
	}
	p := PendingUpdates()
	var waiting []string
	for _, w := range p.Waiting {
		waiting = append(waiting, w.Package+"@"+w.Latest)
	}
	if strings.Join(waiting, ",") != pinned+"@0.1.4,"+theirs+"@1.0.0" {
		t.Fatalf("Waiting = %v", waiting)
	}
	if x, ok := LastUpdated(ours, time.Now().Add(-time.Hour)); !ok || x.To != "0.1.4" {
		t.Fatalf("LastUpdated = %+v, %v", x, ok)
	}
	if _, ok := LastUpdated(ours, time.Now().Add(time.Hour)); ok {
		t.Fatal("an update older than asked for was told")
	}

	// the reader updates theirs: it waits no more, before magpie looks again
	installedAt(t, theirs, "1.0.0")
	if p := PendingUpdates(); len(p.Waiting) != 1 || p.Waiting[0].Package != pinned {
		t.Fatalf("after updating %s, Waiting = %+v", theirs, p.Waiting)
	}
	// looking again updates nothing more, and keeps what it updated
	installed = nil
	if u, err := CheckUpdates(context.Background()); err != nil || len(installed) != 0 || len(u.Updated) != 1 {
		t.Fatalf("looking again: installed %v, %+v, %v", installed, u, err)
	}
}

func TestPinned(t *testing.T) {
	for spec, want := range map[string]bool{
		"@magpie-community/opencode-zed-auth":          false,
		"@magpie-community/opencode-zed-auth@latest":   false,
		"@magpie-community/opencode-zed-auth@0.1.2":    true,
		"@magpie-community/opencode-zed-auth@^0.1.0":   true,
		"opencode-gemini-auth@next":                    true,
		"opencode-gemini-auth":                         false,
		filepath.Join(os.TempDir(), "plugin/index.js"): false,
	} {
		if Pinned(spec) != want {
			t.Errorf("Pinned(%q) = %v, want %v", spec, !want, want)
		}
	}
}

// A reply streaming through a plugin when the plugins are loaded again
// (one updated) goes on to its end, while the next request goes to the
// host started anew.
func TestRestartFinishesTheReply(t *testing.T) {
	sandbox(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		if r.URL.Query().Get("slow") != "" {
			<-release
		}
		io.WriteString(w, "data: last\n\n")
	}))
	defer srv.Close()
	t.Setenv("FAKE_BASE", srv.URL+"/v1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := APIKey(ctx, "fakeco", 0, nil, "k1", NewAccount); err != nil {
		t.Fatal(err)
	}
	fetch := func(q string) (*http.Response, error) {
		return Fetch(ctx, FetchRequest{Provider: "fakeco", Model: "fake-1", NPM: "@ai-sdk/openai-compatible",
			URL: srv.URL + "/v1/chat/completions" + q, Method: "POST", Body: []byte(`{}`)})
	}
	res, err := fetch("?slow=1")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	before := generation.Load()
	Restart()
	// the host the reply streams through is still answering it
	time.Sleep(3 * time.Second)
	next, err := fetch("")
	if err != nil {
		t.Fatalf("a request after the restart: %v", err)
	}
	b, _ := io.ReadAll(next.Body)
	next.Body.Close()
	if generation.Load() == before || !strings.Contains(string(b), "last") {
		t.Fatalf("after the restart: a new host %v, %q", generation.Load() != before, b)
	}
	close(release)
	b, err = io.ReadAll(res.Body)
	if err != nil || !strings.Contains(string(b), "data: last") {
		t.Fatalf("the reply streaming through the restart: %q, %v", b, err)
	}
}
