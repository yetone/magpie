package gui

import (
	"os"
	"path/filepath"
	"testing"
)

// Paseo is on the page once ~/.paseo is there, with magpie to put in its
// Claude model picker (@TheMist016 on X).
func TestStateListsPaseo(t *testing.T) {
	home := sandboxHome(t)
	t.Setenv("PASEO_HOME", "")
	find := func() *agentJSON {
		for _, a := range state().Agents {
			if a.ID == "paseo" {
				return &a
			}
		}
		return nil
	}
	if find() != nil {
		t.Fatal("listed with no ~/.paseo")
	}
	os.MkdirAll(filepath.Join(home, ".paseo"), 0o755)
	a := find()
	if a == nil {
		t.Fatal("Paseo not listed")
	}
	if a.Name != "Paseo" || len(a.Fields) != 1 || a.Fields[0].Key != "provider" || a.Fields[0].Options[0].Value != "magpie" {
		t.Fatalf("Paseo: %+v", a)
	}
}
