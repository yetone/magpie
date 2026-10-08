package provider

import (
	"os"
	"path/filepath"
	"testing"
)

// A provider whose id was put in providers.json by hand, not one magpie
// would make ("b.ai"), is saved as it is and renamed to one that is
// right; a new provider still can't be given such an id (01huadalang on
// Discord: 我不管改成什么都显示不能用 b.ai).
func TestStoredOddIDSavedAndRenamed(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := `{"providers":[{"id":"b.ai","name":"b.ai","responses":"https://api.b.ai/v1","key":"sk-bai"}]}`
	if err := os.WriteFile(Path(), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := Find("b.ai")
	if err != nil {
		t.Fatal(err)
	}
	p.Name = "bai"
	if err := Save(*p); err != nil {
		t.Fatalf("saving the stored b.ai: %v", err)
	}
	if err := Rename("b.ai", "bai"); err != nil {
		t.Fatalf("renaming b.ai: %v", err)
	}
	q, err := Find("bai")
	if err != nil || q.ID != "bai" || q.Name != "bai" || q.Key != "sk-bai" || q.Responses != "https://api.b.ai/v1" {
		t.Fatalf("after the rename: %+v, %v", q, err)
	}
	if err := Save(Provider{ID: "c.ai", Name: "c", Chat: "https://c.example/v1", Key: "sk-c"}); err == nil {
		t.Fatal("a new provider was saved under the id c.ai")
	}
}
