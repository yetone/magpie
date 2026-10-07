package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// vertexNotAsked points Vertex AI, Google's token endpoint and IAM
// Credentials at a server on this computer that fails the test when it is
// asked anything: adding, changing, listing and showing a Vertex AI
// provider asks Google nothing.
func vertexNotAsked(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Google was asked %s %s", r.Method, r.URL)
		http.Error(w, "not here", http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(provider.VertexForTest(srv.URL, srv.URL+"/token", srv.URL+"/iam"))
}

// lineWith is the first line of out that has s in it, "" when none has.
func lineWith(out, s string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, s) {
			return l
		}
	}
	return ""
}

// magpie provider add google-vertex takes the project, location, credentials
// file and service account as pairs, set changes them (an empty one goes
// back to the default), and none of them is taken by a provider that isn't
// Vertex AI. A key is never taken: the error says what signs it instead.
func TestProviderAddVertexPairs(t *testing.T) {
	groupsHome(t)
	vertexNotAsked(t)
	// no project: the pair that gives it is named
	for _, args := range [][]string{{"provider", "add", "google-vertex"}, {"provider", "add", "google-vertex", "location=us"}} {
		err := providerCmd(args)
		if err == nil || !strings.Contains(err.Error(), "needs the id of your Google Cloud project") ||
			!strings.Contains(err.Error(), "magpie provider add google-vertex project=") {
			t.Fatalf("%v: %v", args[2:], err)
		}
	}
	if _, err := provider.Find("google-vertex"); err == nil {
		t.Fatal("added without a project")
	}

	out, err := printed(t, func() error {
		return providerCmd([]string{"provider", "add", "google-vertex", "project=my-project", "location=us",
			"impersonate=vertex@my-project.iam.gserviceaccount.com"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "added Google Vertex AI") {
		t.Fatalf("add said %q", out)
	}
	want := provider.Vertex{Project: "my-project", Location: "us", Impersonate: "vertex@my-project.iam.gserviceaccount.com"}
	if v := mustFind(t, "google-vertex").Vertex; v == nil || *v != want {
		t.Fatalf("added %+v, want %+v", v, want)
	}

	// another location and a service account key; impersonate= drops the
	// account, and the project stays
	key := filepath.Join(t.TempDir(), "vertex-sa.json")
	if _, err := printed(t, func() error {
		return providerCmd([]string{"provider", "set", "google-vertex", "location=europe-west4", "impersonate=", "credentials=" + key})
	}); err != nil {
		t.Fatal(err)
	}
	want = provider.Vertex{Project: "my-project", Location: "europe-west4", Credentials: key}
	if v := mustFind(t, "google-vertex").Vertex; v == nil || *v != want {
		t.Fatalf("set %+v, want %+v", v, want)
	}
	if _, err := printed(t, func() error {
		return providerCmd([]string{"provider", "set", "google-vertex", "location=", "credentials="})
	}); err != nil {
		t.Fatal(err)
	}
	want = provider.Vertex{Project: "my-project", Location: "global"}
	if v := mustFind(t, "google-vertex").Vertex; v == nil || *v != want {
		t.Fatalf("emptied %+v, want %+v", v, want)
	}

	// its address is its project's: one given is refused, not dropped, and
	// a region, as Google Cloud calls one, is its location
	if err := providerCmd([]string{"provider", "set", "google-vertex", "url=https://relay.example.com/v1"}); err == nil || !strings.Contains(err.Error(), "project= and location=") {
		t.Fatalf("url= on Vertex AI: %v", err)
	}
	if err := providerCmd([]string{"provider", "set", "google-vertex", "region=us-central1"}); err == nil || !strings.Contains(err.Error(), "location=") {
		t.Fatalf("region= on Vertex AI: %v", err)
	}
	if v := mustFind(t, "google-vertex").Vertex; v == nil || *v != want {
		t.Fatalf("refused pairs left %+v, want %+v", v, want)
	}

	// a file named from where the command runs is kept by its full path,
	// which the gateway, running elsewhere, can open
	work := t.TempDir()
	t.Chdir(work)
	if _, err := printed(t, func() error {
		return providerCmd([]string{"provider", "set", "google-vertex", "credentials=vertex-sa.json"})
	}); err != nil {
		t.Fatal(err)
	}
	got := mustFind(t, "google-vertex").Vertex.Credentials
	in, _ := os.Stat(filepath.Dir(got))
	cwd, _ := os.Stat(work)
	if !filepath.IsAbs(got) || filepath.Base(got) != "vertex-sa.json" || in == nil || cwd == nil || !os.SameFile(in, cwd) {
		t.Fatalf("credentials=vertex-sa.json in %s kept %q", work, got)
	}

	// a provider that isn't Vertex AI takes none of the four
	for _, pair := range []string{"project=my-project", "location=us", "credentials=" + key, "impersonate=vertex@my-project.iam.gserviceaccount.com"} {
		k, _, _ := strings.Cut(pair, "=")
		err := providerCmd([]string{"provider", "set", "a", pair})
		if err == nil || !strings.Contains(err.Error(), k+"=") || !strings.Contains(err.Error(), "Vertex AI") {
			t.Errorf("%s on A: %v", k, err)
		}
	}
	if err := providerCmd([]string{"provider", "add", "deepseek", "sk-test", "project=my-project"}); err == nil || !strings.Contains(err.Error(), "project=") {
		t.Errorf("project= on DeepSeek: %v", err)
	}
	if v := mustFind(t, "a").Vertex; v != nil {
		t.Fatalf("A has %+v", v)
	}
	if _, err := provider.Find("deepseek"); err == nil {
		t.Fatal("DeepSeek was added with a project")
	}

	// a key: neither as add's second word nor through provider key
	err = providerCmd([]string{"provider", "add", "google-vertex", "my-project"})
	if err == nil || !strings.Contains(err.Error(), "not an API key") || !strings.Contains(err.Error(), "project=") {
		t.Fatalf("add with a key: %v", err)
	}
	if _, err := provider.Find("google-vertex-2"); err == nil {
		t.Fatal("added a second Vertex AI with a key")
	}
	out, err = printed(t, func() error { return providerCmd([]string{"provider", "key", "google-vertex", "AIzaSy-test"}) })
	if err == nil || !strings.Contains(err.Error(), "Google credentials") ||
		!strings.Contains(err.Error(), "magpie provider set google-vertex credentials=") {
		t.Fatalf("provider key: %v", err)
	}
	if strings.Contains(out, "✓") || mustFind(t, "google-vertex").Key != "" {
		t.Fatalf("provider key said %q and kept %q", out, mustFind(t, "google-vertex").Key)
	}
}

// magpie providers and magpie provider <id> say a Vertex AI provider is
// signed with Google credentials, as which service account, and which
// project and location it asks, never that it has no key or needs none;
// switched off still says so. magpie presets says it is added with its
// project.
func TestProvidersShowVertexCredentials(t *testing.T) {
	groupsHome(t)
	vertexNotAsked(t)
	out, err := printed(t, presets)
	if err != nil {
		t.Fatal(err)
	}
	if row := lineWith(out, "google-vertex"); !strings.Contains(row, "magpie provider add google-vertex project=") || strings.Contains(row, "<key>") {
		t.Fatalf("presets: %q", row)
	}
	key := filepath.Join(t.TempDir(), "vertex-sa.json")
	for _, p := range []provider.Provider{
		{ID: "google-vertex", Name: "Google Vertex AI", Preset: provider.VertexPreset, Vertex: &provider.Vertex{Project: "my-project"}},
		{ID: "vertex-sa", Name: "Vertex SA", Preset: provider.VertexPreset, Vertex: &provider.Vertex{Project: "my-project",
			Location: "us-central1", Credentials: key, Impersonate: "vertex@my-project.iam.gserviceaccount.com"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}

	out, err = printed(t, providers)
	if err != nil {
		t.Fatal(err)
	}
	row := lineWith(out, "google-vertex")
	if !strings.Contains(row, "Google credentials") || strings.Contains(row, " as ") || strings.Contains(row, "no key") {
		t.Errorf("list, ADC: %q", row)
	}
	row = lineWith(out, "vertex-sa")
	if !strings.Contains(row, "Google credentials as vertex@my-project.iam.gserviceaccount.com") || strings.Contains(row, "no key") {
		t.Errorf("list, impersonating: %q", row)
	}

	out, err = printed(t, func() error { return providerCmd([]string{"provider", "google-vertex"}) })
	if err != nil {
		t.Fatal(err)
	}
	for label, want := range map[string]string{"project": "my-project", "location": "global", "signs with": "gcloud's Application Default Credentials"} {
		if !strings.Contains(lineWith(out, label), want) {
			t.Errorf("show, ADC: no %s %s in\n%s", label, want, out)
		}
	}
	if strings.Contains(out, "none needed") || strings.Contains(out, "not set") || lineWith(out, " as ") != "" {
		t.Errorf("show, ADC:\n%s", out)
	}
	out, err = printed(t, func() error { return providerCmd([]string{"provider", "vertex-sa"}) })
	if err != nil {
		t.Fatal(err)
	}
	for label, want := range map[string]string{"location": "us-central1", "signs with": key, " as ": "vertex@my-project.iam.gserviceaccount.com"} {
		if !strings.Contains(lineWith(out, label), want) {
			t.Errorf("show, impersonating: no %s %s in\n%s", label, want, out)
		}
	}
	if strings.Contains(out, "Application Default") || strings.Contains(out, "none needed") {
		t.Errorf("show, impersonating:\n%s", out)
	}

	if err := provider.SetOff("google-vertex", true); err != nil {
		t.Fatal(err)
	}
	out, _ = printed(t, providers)
	if row := lineWith(out, "google-vertex"); !strings.Contains(row, "switched off") || strings.Contains(row, "Google credentials") {
		t.Errorf("list, off: %q", row)
	}
}

// A Vertex AI provider with no project an address can be made of
// (providers.json edited by hand) asks nothing: magpie providers says it
// needs a project, as the app's row does, and magpie provider <id> says
// how to give it one, never to give it a key, which it takes none of.
func TestProvidersShowVertexWithoutProject(t *testing.T) {
	groupsHome(t)
	vertexNotAsked(t)
	if err := os.MkdirAll(filepath.Dir(provider.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(provider.Path(), []byte(`{"providers":[{"id":"vertex-bare","name":"Vertex Bare","preset":"google-vertex"},`+
		`{"id":"vertex-spaced","name":"Vertex Spaced","preset":"google-vertex","vertex":{"project":"My Project"}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := printed(t, providers)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"vertex-bare", "vertex-spaced"} {
		if row := lineWith(out, id); !strings.Contains(row, "○ needs a project") || strings.Contains(row, "Google credentials") {
			t.Errorf("list, %s: %q", id, row)
		}
	}
	for id, want := range map[string]string{"vertex-bare": "not set", "vertex-spaced": "my project"} {
		out, err := printed(t, func() error { return providerCmd([]string{"provider", id}) })
		if err != nil {
			t.Fatal(err)
		}
		if row := lineWith(out, "project "); !strings.Contains(row, want) || !strings.Contains(row, "magpie provider set "+id+" project=") {
			t.Errorf("show, %s: project %q in\n%s", id, row, out)
		}
		if strings.Contains(out, "provider key") || strings.Contains(out, "not set  magpie provider key") {
			t.Errorf("show, %s, asks for a key:\n%s", id, out)
		}
	}
}

// magpie provider test asks Vertex AI with a token minted from the
// provider's Google credentials, with no key asked for on the way: the
// smallest generateContent, at its project, to the first of its models.
func TestProviderTestVertex(t *testing.T) {
	groupsHome(t)
	var mu sync.Mutex
	var asked []string // each request's path and Authorization
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/token":
			io.WriteString(w, `{"access_token":"ya29.vertex-test","expires_in":3599,"scope":"https://www.googleapis.com/auth/cloud-platform","token_type":"Bearer"}`)
		case strings.HasSuffix(r.URL.Path, ":generateContent"):
			io.WriteString(w, `{"candidates": [{"content": {"role": "model","parts": [{"text": "Hi"}]},"finishReason": "MAX_TOKENS"}],"usageMetadata": {"promptTokenCount": 1,"candidatesTokenCount": 1,"totalTokenCount": 2,"trafficType": "ON_DEMAND"},"modelVersion": "gemini-3.8-flash","createTime": "2026-10-06T19:24:49.613681Z","responseId": "AUvFarG6Ja6Z4_UP_LrtmQ8"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(provider.VertexForTest(srv.URL, srv.URL+"/token", srv.URL+"/iam"))
	// as `gcloud auth application-default login` writes it
	creds := filepath.Join(t.TempDir(), "application_default_credentials.json")
	if err := os.WriteFile(creds, []byte(`{"type":"authorized_user","client_id":"test.apps.googleusercontent.com","client_secret":"test-secret","refresh_token":"1//test-refresh"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := printed(t, func() error {
		return providerCmd([]string{"provider", "add", "google-vertex", "project=my-project", "credentials=" + creds})
	}); err != nil {
		t.Fatal(err)
	}
	out, err := printed(t, func() error { return providerCmd([]string{"provider", "test", "google-vertex"}) })
	if err != nil || !strings.Contains(out, "✓") || !strings.Contains(out, "gemini-3.8-flash") {
		t.Fatalf("test said %q (%v)", out, err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"/token ", "/v1/projects/my-project/locations/global/publishers/google/models/gemini-3.8-flash:generateContent Bearer ya29.vertex-test"}
	if strings.Join(asked, "\n") != strings.Join(want, "\n") {
		t.Fatalf("asked\n%s\nwant\n%s", strings.Join(asked, "\n"), strings.Join(want, "\n"))
	}
}
