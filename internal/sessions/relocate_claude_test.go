package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func relocationFixture(t *testing.T) (string, string, ClaudeRelocation) {
	t.Helper()
	root := t.TempDir()
	in := ClaudeRelocation{From: filepath.Join(root, "old_app"), To: filepath.Join(root, "new_app")}
	if err := os.Mkdir(in.To, 0o700); err != nil {
		t.Fatal(err)
	}
	name, _ := claudeProjectName(in.From)
	source := filepath.Join(root, "projects", name)
	row := map[string]any{
		"type": "user", "cwd": in.From, "sessionId": mgCC, "uuid": "unchanged",
		"message": map[string]any{"role": "user", "content": "Historical path: " + in.From},
	}
	b, _ := json.Marshal(row)
	for p, content := range map[string][]byte{
		mgCC + ".jsonl": b,
		filepath.Join(mgCC, "subagents", "agent-a.jsonl"): b,
		filepath.Join(mgCC, "tool-results", "result.txt"): []byte(in.From),
		filepath.Join("memory", "MEMORY.md"):              []byte(in.From),
	} {
		target := filepath.Join(source, p)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ageRelocation(t, source)
	return root, source, in
}

func ageRelocation(t *testing.T, root string) {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	if err := filepath.WalkDir(root, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, old, old)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeRelocationPreviewAndCommit(t *testing.T) {
	root, source, in := relocationFixture(t)
	before, _ := os.ReadFile(filepath.Join(source, mgCC+".jsonl"))
	out, err := relocateClaudeProject(root, in.To, in)
	if err != nil || out.Sessions != 1 || out.Files != 4 || out.Backup != "" || out.Token == "" {
		t.Fatalf("preview %+v: %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(root, "magpie-relocations")); !os.IsNotExist(err) {
		t.Fatal("preview wrote to disk")
	}
	in.Token = out.Token
	out, err = relocateClaudeProject(root, in.To, in)
	if err != nil {
		t.Fatal(err)
	}
	name, _ := claudeProjectName(in.To)
	dest := filepath.Join(root, "projects", name)
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatal("source remains discoverable")
	}
	backup, _ := os.ReadFile(filepath.Join(out.Backup, "original", mgCC+".jsonl"))
	if string(backup) != string(before) {
		t.Fatal("backup changed")
	}
	for _, rel := range []string{mgCC + ".jsonl", filepath.Join(mgCC, "subagents", "agent-a.jsonl")} {
		b, _ := os.ReadFile(filepath.Join(dest, rel))
		var row map[string]json.RawMessage
		if json.Unmarshal(b, &row) != nil {
			t.Fatal("invalid migrated transcript")
		}
		var cwd string
		json.Unmarshal(row["cwd"], &cwd)
		var message struct{ Content string }
		json.Unmarshal(row["message"], &message)
		if cwd != in.To || message.Content != "Historical path: "+in.From {
			t.Fatal("cwd not migrated or history rewritten")
		}
	}
	for _, rel := range []string{filepath.Join("memory", "MEMORY.md"), filepath.Join(mgCC, "tool-results", "result.txt")} {
		b, _ := os.ReadFile(filepath.Join(dest, rel))
		if string(b) != in.From {
			t.Fatal("artifact contents changed")
		}
	}
}

func TestClaudeRelocationPublishRollback(t *testing.T) {
	for name, conflict := range map[string]bool{"missing-stage": false, "target-conflict": true} {
		t.Run(name, func(t *testing.T) {
			root, source, _ := relocationFixture(t)
			backup := filepath.Join(root, "backup")
			os.Mkdir(backup, 0o700)
			dest := filepath.Join(root, "destination")
			if conflict {
				os.Mkdir(dest, 0o700)
				os.WriteFile(filepath.Join(dest, "keep"), []byte("untouched"), 0o600)
			}
			// No staged tree: publishing must fail even without a conflict.
			if err := publishClaudeRelocation(source, dest, backup); err == nil {
				t.Fatal("publish unexpectedly succeeded")
			}
			if _, err := os.Stat(filepath.Join(source, mgCC+".jsonl")); err != nil {
				t.Fatal("original not restored", err)
			}
			if conflict {
				b, _ := os.ReadFile(filepath.Join(dest, "keep"))
				if string(b) != "untouched" {
					t.Fatal("target overwritten")
				}
			}
		})
	}
}

func TestClaudeRelocationRefusesUnsafeInputs(t *testing.T) {
	for _, kind := range []string{"active", "conflict", "stale", "symlink", "malformed", "wrong-project", "same", "missing"} {
		t.Run(kind, func(t *testing.T) {
			root, source, in := relocationFixture(t)
			preview, err := relocateClaudeProject(root, in.To, in)
			if err != nil {
				t.Fatal(err)
			}
			in.Token = preview.Token
			p := filepath.Join(source, mgCC+".jsonl")
			switch kind {
			case "active":
				os.Chtimes(p, time.Now(), time.Now())
			case "conflict":
				name, _ := claudeProjectName(in.To)
				os.Mkdir(filepath.Join(root, "projects", name), 0o700)
			case "stale":
				os.WriteFile(filepath.Join(source, "extra"), []byte("new"), 0o600)
				ageRelocation(t, source)
			case "symlink":
				if err := os.Symlink(in.To, filepath.Join(source, "link")); err != nil {
					t.Skip(err)
				}
				ageRelocation(t, source)
			case "malformed":
				os.WriteFile(p, []byte("{"), 0o600)
				ageRelocation(t, source)
			case "wrong-project":
				os.WriteFile(p, []byte(`{"cwd":"/another/project"}`), 0o600)
				ageRelocation(t, source)
			case "same":
				in.To = in.From
			case "missing":
				in.To = filepath.Join(root, "missing")
			}
			if _, err := relocateClaudeProject(root, in.To, in); err == nil {
				t.Fatal("unsafe relocation accepted")
			}
			if _, err := os.Stat(p); err != nil {
				t.Fatal("original disappeared")
			}
		})
	}
}

func TestClaudeRelocationMetadata(t *testing.T) {
	b := []byte("{\"cwd\":\"/old/sub\",\"message\":{\"cwd\":\"/old\",\"content\":\"/old\"}}\n{\"cwd\":\"/external/worktree\"}\n")
	got, found, err := relocateClaudeJSONL(b, "/old", "/new", true)
	if err != nil || !found {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"/new/sub"`) || !strings.Contains(string(got), `"/external/worktree"`) ||
		!strings.Contains(string(got), `"message":{"cwd":"/old","content":"/old"}`) {
		t.Fatalf("unexpected rewrite %s", got)
	}
	index := []byte(`{"version":1,"originalPath":"/old","entries":[{"sessionId":"s","projectPath":"/old","fullPath":"/config/projects/-old/s.jsonl","summary":"Read /old"}]}`)
	got, err = relocateClaudeIndex(index, "/config/projects/-old", "/config/projects/-new", "/old", "/new")
	if err != nil || !strings.Contains(string(got), `"/config/projects/-new/s.jsonl"`) ||
		!strings.Contains(string(got), `"summary":"Read /old"`) {
		t.Fatalf("index %s: %v", got, err)
	}
}

func TestClaudeProjectName(t *testing.T) {
	got, err := claudeProjectName("/home/user/my_app")
	if err != nil || got != "-home-user-my-app" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := claudeProjectName("/" + strings.Repeat("a", 200)); err == nil {
		t.Fatal("unsupported hashed path accepted")
	}
	if _, _, err := relocationPaths(ClaudeRelocation{From: "/a_b", To: "/a-b", WSL: "Ubuntu"}); err == nil {
		t.Fatal("encoding collision accepted")
	}
}
