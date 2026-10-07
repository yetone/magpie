package tui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Google Vertex AI is added from the TUI with its Google Cloud project, as
// magpie provider add google-vertex project=… adds it, and no key is asked
// for: its row says the Google credentials that sign it, and e and w, which
// would ask a key or an address it has no use for, say the command that
// changes what it has instead.
func TestTUIAddsVertexWithItsProject(t *testing.T) {
	home(t)
	// adding and listing it asks Google nothing
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Google was asked %s %s", r.Method, r.URL)
		http.Error(w, "not here", http.StatusTeapot)
	}))
	defer srv.Close()
	defer provider.VertexForTest(srv.URL, srv.URL+"/token", srv.URL+"/iam")()

	m := press(t, model{w: 160, h: 40}, "2", "a")
	m = typeIn(m, "google-vertex")
	m = press(t, m, "enter")
	if m.mode != modeAsk || m.ask.input.Placeholder != "your Google Cloud project's id" {
		t.Fatalf("picking Google Vertex AI asked %q (mode %v), not its project", m.ask.input.Placeholder, m.mode)
	}
	if v := m.View(); !strings.Contains(v, "gcloud's Application Default Credentials") || !strings.Contains(v, "magpie provider set google-vertex location=") {
		t.Fatalf("the project's line says nothing of what signs it:\n%s", v)
	}
	// nothing typed: said to be needed, nothing added
	m = press(t, m, "enter")
	wantFlash(t, m, false, "needs the id of your Google Cloud project")
	if _, err := provider.Find("google-vertex"); err == nil {
		t.Fatal("added without a project")
	}

	m = press(t, m, "a")
	m = typeIn(m, "google-vertex")
	m = press(t, m, "enter")
	m = typeIn(m, "my-project")
	m = press(t, m, "enter")
	wantFlash(t, m, true, "added Google Vertex AI · ")
	p, err := provider.Find("google-vertex")
	if err != nil {
		t.Fatal(err)
	}
	if p.Vertex == nil || *p.Vertex != (provider.Vertex{Project: "my-project", Location: "global"}) || p.Key != "" {
		t.Fatalf("saved %+v, key %q", p.Vertex, p.Key)
	}

	// its row, one asked as a service account and one switched off
	for _, q := range []provider.Provider{
		{ID: "vertex-sa", Name: "Vertex SA", Preset: provider.VertexPreset,
			Vertex: &provider.Vertex{Project: "my-project", Impersonate: "vertex@my-project.iam.gserviceaccount.com"}},
		{ID: "vertex-off", Name: "Vertex Off", Preset: provider.VertexPreset, Vertex: &provider.Vertex{Project: "my-project"}, Off: true},
	} {
		if err := provider.Save(q); err != nil {
			t.Fatal(err)
		}
	}
	m.reloadProviders()
	view := m.View()
	row := func(id string) string {
		for _, l := range strings.Split(view, "\n") {
			if strings.Contains(l, " "+id+" ") {
				return l
			}
		}
		t.Fatalf("no row for %s:\n%s", id, view)
		return ""
	}
	if r := row("google-vertex"); !strings.Contains(r, "● Google credentials") || strings.Contains(r, " as ") || strings.Contains(r, "no key") {
		t.Errorf("its row: %q", r)
	}
	if r := row("vertex-sa"); !strings.Contains(r, "● Google credentials as vertex@my-project.iam.gserviceaccount.com") {
		t.Errorf("a service account's row: %q", r)
	}
	if r := row("vertex-off"); !strings.Contains(r, "○ switched off") || strings.Contains(r, "Google credentials") {
		t.Errorf("a switched-off one's row: %q", r)
	}

	for i, q := range m.provs {
		if q.ID == "google-vertex" {
			m.prow = i
		}
	}
	m = press(t, m, "e")
	if m.mode == modeAsk {
		t.Fatalf("e asked %q of Vertex AI", m.ask.input.Placeholder)
	}
	wantFlash(t, m, false, "magpie provider set google-vertex credentials=")
	m = press(t, m, "w")
	if m.mode == modeAsk {
		t.Fatalf("w asked %q of Vertex AI", m.ask.input.Placeholder)
	}
	wantFlash(t, m, false, "magpie provider set google-vertex project=")
}

// A Vertex AI provider with no project (providers.json edited by hand)
// says in its row that it needs one, as the app's does, not that Google
// credentials sign it: nothing is asked, and no agent has its models.
func TestTUIVertexWithoutProject(t *testing.T) {
	home(t)
	if err := os.MkdirAll(filepath.Dir(provider.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(provider.Path(), []byte(`{"providers":[{"id":"vertex-bare","name":"Vertex Bare","preset":"google-vertex"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := press(t, model{w: 160, h: 40}, "2")
	m.reloadProviders()
	for _, l := range strings.Split(m.View(), "\n") {
		if strings.Contains(l, " vertex-bare ") {
			if !strings.Contains(l, "○ needs a project") || strings.Contains(l, "Google credentials") {
				t.Errorf("its row: %q", l)
			}
			return
		}
	}
	t.Fatalf("no row for vertex-bare:\n%s", m.View())
}
