package sessions

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Codex 0.166 resumes a thread only on the provider it was saved with:
// `codex resume <id>` of a session CC Switch's "custom" made, with Codex
// on magpie now, stopped at modelProviderMismatch ("This chat uses custom,
// but your current provider is magpie", mrten_95615 on Discord). Opening
// it from Sessions names its own provider while config.toml has it, and
// moves the thread to the provider Codex is on when config.toml has it no
// longer, which nothing else gets Codex to resume.
func TestCodexResumeOnItsProvider(t *testing.T) {
	_, codex := manageSetup(t)
	main := filepath.Join(codex, "sessions", "2026", "09", "20", "rollout-2026-09-20T15-48-28-"+mgCX+".jsonl")
	b, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	orig := bytes.Replace(b, []byte(`"model_provider":"openai"`), []byte(`"model_provider":"custom"`), 1)
	old := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	os.WriteFile(main, orig, 0o644)
	os.Chtimes(main, old, old)
	db := codexStateDB(t, codex, mgCX, "custom")
	cfg := filepath.Join(codex, "config.toml")
	magpie := "model = \"gpt-5.5\"\nmodel_provider = \"magpie\"\n\n[model_providers.magpie]\nname = \"magpie\"\nbase_url = \"http://127.0.0.1:1/v1\"\n"
	custom := "\n[model_providers.custom]\nname = \"custom\"\nbase_url = \"http://127.0.0.1:2/v1\"\n"

	// custom still in config.toml: resumed on it, nothing moved
	os.WriteFile(cfg, []byte(magpie+custom), 0o644)
	s, ok := Find("codex", mgCX)
	if !ok || !strings.HasSuffix(s.Resume, "codex resume "+mgCX) {
		t.Fatalf("listed %v, resume %q", ok, s.Resume)
	}
	got := ResumeNow(s)
	if want := strings.TrimSuffix(s.Resume, "codex resume "+mgCX) + "codex resume -c model_provider=custom " + mgCX; got != want {
		t.Fatalf("resume %q, want %q", got, want)
	}
	if b, _ := os.ReadFile(main); !bytes.Equal(b, orig) {
		t.Fatal("a thread whose provider is there was changed")
	}

	// Codex on custom itself: as listed
	os.WriteFile(cfg, []byte(strings.Replace(magpie, `model_provider = "magpie"`, `model_provider = "custom"`, 1)+custom), 0o644)
	if got := ResumeNow(s); got != s.Resume {
		t.Fatalf("on its own provider %q, want %q", got, s.Resume)
	}

	// custom gone: the thread moves to magpie and resumes there
	os.WriteFile(cfg, []byte(magpie), 0o644)
	if got := ResumeNow(s); got != s.Resume {
		t.Fatalf("resume %q, want %q", got, s.Resume)
	}
	if b, _ := os.ReadFile(main); !bytes.Contains(b, []byte(`"model_provider":"magpie"`)) || bytes.Contains(b, []byte(`"model_provider":"custom"`)) {
		t.Fatal("the thread's rollout wasn't moved to magpie")
	}
	if p, _, _ := threadProvider(t, db, mgCX); p != "magpie" {
		t.Fatalf("thread row %q, want magpie", p)
	}
	if p, _, _ := threadProvider(t, db, "other"); p != "custom" {
		t.Errorf("another thread became %q", p)
	}

	// the row is what Codex reads first: a row on custom over a file
	// naming magpie still takes the override
	os.WriteFile(cfg, []byte(magpie+custom), 0o644)
	if _, err := moveCodexThread(db, mgCX, "custom"); err != nil {
		t.Fatal(err)
	}
	if got := ResumeNow(s); !strings.Contains(got, "-c model_provider=custom ") {
		t.Fatalf("row on custom: resume %q", got)
	}

	// a built-in Codex knows without a table
	moveCodexThread(db, mgCX, "openai")
	if got := ResumeNow(s); !strings.Contains(got, "-c model_provider=openai ") {
		t.Fatalf("row on openai: resume %q", got)
	}

	// another agent's line is as listed
	if cc := (Session{Agent: "claude", ID: "x", Resume: "claude --resume x", Path: main}); ResumeNow(cc) != cc.Resume {
		t.Fatal("a Claude session's line changed")
	}
}
