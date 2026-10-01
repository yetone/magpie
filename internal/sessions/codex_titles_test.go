package sessions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCodexTitlesRenameAndIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session_index.jsonl")
	old := []byte("{\"id\":\"main\",\"thread_name\":\"查询长沙天气\"}\n{\"id\":\"other\",\"thread_name\":\"另一个聊天\"}\n")
	if err := os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	got := codexTitlesAt(path, []string{"main", "missing"})
	if got["main"] != "查询长沙天气" || len(got) != 1 {
		t.Fatalf("names: %#v", got)
	}
	if err := os.WriteFile(path, append(old, []byte("{\"id\":\"main\",\"thread_name\":\"  长沙天气更新  \\n \"}\n{broken")...), 0600); err != nil {
		t.Fatal(err)
	}
	got = codexTitlesAt(path, []string{"main"})
	if got["main"] != "长沙天气更新" {
		t.Fatalf("rename: %#v", got)
	}
	got["main"] = "caller mutation"
	if codexTitlesAt(path, []string{"main"})["main"] != "长沙天气更新" {
		t.Fatal("returned internal cache")
	}
	if got := codexTitlesAt(filepath.Join(t.TempDir(), "missing"), []string{"main"}); len(got) != 0 {
		t.Fatalf("cross installation name leak: %#v", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := codexTitlesAt(path, []string{"main"}); len(got) != 0 {
		t.Fatalf("deleted index used stale names: %#v", got)
	}
}
