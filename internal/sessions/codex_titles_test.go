package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexTitlesRenameAndIsolation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
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

func TestCodexTitlesIncrementalApplications(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	path := filepath.Join(dir, "session_index.jsonl")
	at := time.Now().UTC().Truncate(time.Second)
	line := func(name string) string {
		return fmt.Sprintf("{\"id\":\"main\",\"thread_name\":%q,\"updated_at\":%q}\n", name, at.Format(time.RFC3339Nano))
	}
	if err := os.WriteFile(path, []byte(line("Generated")), 0600); err != nil {
		t.Fatal(err)
	}
	if got := CodexTitleRecipients([]string{CodexTitleDigest("Generated"), CodexTitleDigest("Renamed")})["main"]; len(got) != 1 || got[0].Digest != CodexTitleDigest("Generated") {
		t.Fatalf("applications: %v", got)
	}
	// Prove that the append path doesn't reread earlier bytes. Names have no
	// timestamp dependency, and adding a partial line must not publish it.
	firstOffset := codexTitles.offset
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	partial := strings.TrimSuffix(line("Renamed"), "\n")
	f.WriteString(partial)
	f.Close()
	if got := CodexTitles([]string{"main"})["main"]; got != "Generated" || codexTitles.offset != firstOffset {
		t.Fatalf("partial row published: %s offset %d", got, codexTitles.offset)
	}
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString("\n")
	f.Close()
	if got := CodexTitles([]string{"main"})["main"]; got != "Renamed" {
		t.Fatalf("completed row: %s", got)
	}
	apps := CodexTitleRecipients([]string{CodexTitleDigest("Generated"), CodexTitleDigest("Renamed")})["main"]
	if len(apps) != 2 || apps[0].Digest != CodexTitleDigest("Generated") {
		t.Fatal("rename discarded original application")
	}
	apps[0].Digest = "mutated"
	if CodexTitleRecipients([]string{CodexTitleDigest("Generated"), CodexTitleDigest("Renamed")})["main"][0].Digest == "mutated" {
		t.Fatal("exposed cache slice")
	}
	// Atomic replacement and truncation invalidate old name/application data.
	replacement := path + ".new"
	os.WriteFile(replacement, []byte(line("Replaced")), 0600)
	os.Rename(replacement, path)
	if got := CodexTitles([]string{"main"})["main"]; got != "Replaced" || len(CodexTitleRecipients([]string{CodexTitleDigest("Replaced")})["main"]) != 1 {
		t.Fatal("replacement kept stale evidence")
	}
	os.WriteFile(path, []byte("{\"id\":\"main\",\"thread_name\":\"Short\"}\n"), 0600)
	if got := CodexTitles([]string{"main"})["main"]; got != "Short" || len(CodexTitleRecipients([]string{CodexTitleDigest("Generated"), CodexTitleDigest("Renamed")})) != 0 {
		t.Fatal("truncation kept stale evidence")
	}
}

func BenchmarkCodexTitleIndex(b *testing.B) {
	b.Setenv("XDG_CONFIG_HOME", b.TempDir())
	dir := b.TempDir()
	path := filepath.Join(dir, "session_index.jsonl")
	var data strings.Builder
	at := time.Now().UTC().Format(time.RFC3339Nano)
	for i := 0; i < 10000; i++ {
		fmt.Fprintf(&data, "{\"id\":\"chat-%d\",\"thread_name\":\"Title %d\",\"updated_at\":%q}\n", i, i, at)
	}
	os.WriteFile(path, []byte(data.String()), 0600)
	ids := []string{"chat-1", "chat-500", "chat-9999"}
	codexTitlesAt(path, ids)
	b.Run("Cold10k", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			codexTitles.path = ""
			codexTitlesAt(path, ids)
		}
	})
	b.Run("Cached10k", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			codexTitlesAt(path, ids)
		}
	})
	b.Run("Append10k", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			f.WriteString("{\"id\":\"chat-1\",\"thread_name\":\"Update\"}\n")
			f.Close()
			codexTitlesAt(path, ids)
		}
	})
}

func TestCodexTitleEvidenceBound(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	var data strings.Builder
	at := time.Now().UTC().Add(-time.Second)
	for i := 0; i <= titleApplicationLimit; i++ {
		fmt.Fprintf(&data, "{\"id\":\"main\",\"thread_name\":\"Title\",\"updated_at\":%q}\n", at.Add(time.Duration(i)).Format(time.RFC3339Nano))
	}
	if err := os.WriteFile(codexTitleIndexPath(), []byte(data.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if got := CodexTitleRecipients([]string{CodexTitleDigest("Title")}); len(got) != 0 {
		t.Fatal("inference retained partial evidence after hitting its bound")
	}
	if got := CodexTitles([]string{"main"})["main"]; got != "Title" {
		t.Fatal("evidence bound broke normal title display")
	}
}
