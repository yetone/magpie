//go:build windows

package library

import (
	"os"
	"path/filepath"
	"testing"
)

// #7226 on Windows, where the library's link into ~/.agents/skills is a
// junction: its folder removed, the skill is gone, and taking the junction
// away leaves the user's other folders as they are.
func TestJunctionToARemovedFolderIsGone(t *testing.T) {
	h := sandbox(t)
	shared := filepath.Join(h, ".agents", "skills")
	skill(t, filepath.Join(shared, "pdf"), "pdf", "Read PDFs")
	os.MkdirAll(filepath.Dir(skillDir("pdf")), 0o755)
	if err := junction(filepath.Join(shared, "pdf"), skillDir("pdf")); err != nil {
		t.Fatal(err)
	}
	if sourceGone("pdf") {
		t.Fatal("a junction to a folder that is there is gone")
	}
	if err := os.RemoveAll(filepath.Join(shared, "pdf")); err != nil {
		t.Fatal(err)
	}
	if !sourceGone("pdf") {
		t.Error("a junction to a removed folder isn't gone")
	}
	if _, err := os.Lstat(skillDir("pdf")); err != nil {
		t.Errorf("the junction was taken away: %v", err)
	}
}
