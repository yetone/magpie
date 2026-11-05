package gui

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// #1865: Settings › Images lists a ChatGPT account's one GPT Image, marked
// as the vendor's pick, and a gpt-image-2.5 saved when it was listed shows
// as that entry while the saved value is kept as it is.
func TestImageGenCodexOneEntry(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("CODEX_HOME", "")
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	auth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":      claims(map[string]any{"email": "me@example.com"}),
		"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
		"refresh_token": "r", "account_id": "acct-1"}})
	os.MkdirAll(filepath.Join(h, ".codex"), 0o755)
	if err := os.WriteFile(filepath.Join(h, ".codex", "auth.json"), auth, 0o600); err != nil {
		t.Fatal(err)
	}
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	st := settings.Load()
	st.ImageGen = "codex/gpt-image-2.5"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	s := settingsState()
	var codex []modelRef
	for _, m := range s.ImageGenModels {
		if m.Provider == "codex" {
			codex = append(codex, m)
		}
	}
	if len(codex) != 1 || codex[0].ID != "codex/gpt-image-2" || codex[0].Name != "GPT Image" || !codex[0].VendorPicks {
		t.Fatalf("codex image models %+v", codex)
	}
	if s.ImageGen != "codex/gpt-image-2.5" || s.ImageGenShown != "codex/gpt-image-2" || s.ImageGenMissing != "" {
		t.Fatalf("imageGen %q shown %q missing %q", s.ImageGen, s.ImageGenShown, s.ImageGenMissing)
	}
	st.ImageGen = "codex/gpt-image-2"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	if s := settingsState(); s.ImageGenShown != "" {
		t.Fatalf("a listed pick is shown as %q", s.ImageGenShown)
	}
	// its editor's Image models says why it lists one (ChangQiu on
	// Discord looked there for gpt-image-2.5)
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if j := providerInfo(*p, nil); !j.DrawPicks || len(j.DrawIDs) != 1 {
		t.Fatalf("editor's drawIds %v, drawPicks %v", j.DrawIDs, j.DrawPicks)
	} else if j.DrawNames[j.DrawIDs[0]] != "GPT Image" {
		// StarBits on X read the chip's gpt-image-2 as an old model
		t.Fatalf("editor shows %q by %v", j.DrawIDs[0], j.DrawNames)
	}
}
