package davsync

import (
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/provider"
)

// Whether a Remote magpie that is this magpie is used anyway is this
// computer's own (provider.Mirror keeps it): on the other computer the
// same provider points at a magpie that is not that one (hanslin1666 on
// Discord). Changing it uploads nothing.
func TestKeepSelfIsNoChange(t *testing.T) {
	p := provider.Provider{ID: "rem", Name: "Rem", Preset: provider.RemoteMagpiePreset}
	b := backup.Bundle{Providers: []provider.Provider{p}}
	p.KeepSelf = true
	kept := backup.Bundle{Providers: []provider.Provider{p}}
	if hashes(b)["providers"] != hashes(kept)["providers"] {
		t.Fatal("Use it anyway changed the providers hash")
	}
	if !kept.Providers[0].KeepSelf {
		t.Fatal("hashing changed the bundle")
	}
}
