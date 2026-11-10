package appdir

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeMiniMaxStore writes MiniMax Code Desktop's electron-store file as
// 3.1.13 does, its config holding body.
func writeMiniMaxStore(t *testing.T, userData, file, body string) {
	t.Helper()
	if err := os.MkdirAll(userData, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userData, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The desktop app's Settings moves the data folder to <parent>/.minimax
// and keeps the parent in its store, in Electron's userData: Application
// Support/MiniMax on a Mac, %APPDATA%\MiniMax on Windows (YujunT46308 on
// X). The global build's store is read too; a store that sets none, isn't
// JSON, or holds no usable path leaves ~/.minimax.
func TestMiniMaxDesktopParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "D", "data")
	store := `{"user":{},"config":{"language":"zh","localRuntimeDataParentDir":` + quote(parent) + `,"localRuntimePreviousDataParentDirs":[]}}`
	for _, goos := range []string{"darwin", "windows"} {
		for _, s := range miniMaxDesktopStores {
			t.Run(goos+"/"+s[0], func(t *testing.T) {
				home := t.TempDir()
				base := filepath.Join(home, "Library", "Application Support")
				if goos == "windows" {
					base = filepath.Join(t.TempDir(), "Roaming")
					t.Setenv("APPDATA", base)
				}
				if got := miniMaxDesktopParent(home, goos); got != "" {
					t.Fatalf("no store: %q", got)
				}
				writeMiniMaxStore(t, filepath.Join(base, s[0]), s[1], store)
				if got := miniMaxDesktopParent(home, goos); got != parent {
					t.Fatalf("got %q, want %q", got, parent)
				}
			})
		}
	}
	t.Run("linux", func(t *testing.T) {
		home := t.TempDir()
		writeMiniMaxStore(t, filepath.Join(home, ".config", "MiniMax"), "minimax-agent-cn-config.json", store)
		if got := miniMaxDesktopParent(home, "linux"); got != "" {
			t.Fatalf("linux has no desktop app: %q", got)
		}
	})
	for name, body := range map[string]string{
		"unset":    `{"config":{"language":"zh"}}`,
		"empty":    `{"config":{"localRuntimeDataParentDir":"  "}}`,
		"number":   `{"config":{"localRuntimeDataParentDir":3}}`,
		"relative": `{"config":{"localRuntimeDataParentDir":"data"}}`,
		"broken":   `{"config":{"localRuntimeDataParentDir":` + quote(parent),
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			writeMiniMaxStore(t, filepath.Join(home, "Library", "Application Support", "MiniMax"), "minimax-agent-cn-config.json", body)
			if got := miniMaxDesktopParent(home, "darwin"); got != "" {
				t.Fatalf("got %q", got)
			}
		})
	}
}

// MINIMAX_DATA_DIR, then MAVIS_DATA_DIR, win over the desktop app's
// setting, as in the app; without either the moved folder is the one.
func TestMiniMaxDirIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("MINIMAX_DATA_DIR", "")
	t.Setenv("MAVIS_DATA_DIR", "")
	if got, want := MiniMaxDirIn(home), filepath.Join(home, ".minimax"); got != want {
		t.Fatalf("default: %q", got)
	}
	parent := filepath.Join(t.TempDir(), "moved")
	userData := filepath.Join(home, "Library", "Application Support", "MiniMax")
	if goos := runtime.GOOS; goos == "windows" {
		userData = filepath.Join(home, "AppData", "Roaming", "MiniMax")
	} else if goos != "darwin" {
		t.Skip("MiniMax Code Desktop ships for macOS and Windows only")
	}
	writeMiniMaxStore(t, userData, "minimax-agent-cn-config.json", `{"config":{"localRuntimeDataParentDir":`+quote(parent)+`}}`)
	if got, want := MiniMaxDirIn(home), filepath.Join(parent, ".minimax"); got != want {
		t.Fatalf("moved: %q, want %q", got, want)
	}
	mavis := filepath.Join(t.TempDir(), "mavis")
	t.Setenv("MAVIS_DATA_DIR", mavis)
	if got := MiniMaxDirIn(home); got != mavis {
		t.Fatalf("MAVIS_DATA_DIR: %q", got)
	}
	env := filepath.Join(t.TempDir(), "env")
	t.Setenv("MINIMAX_DATA_DIR", env)
	if got := MiniMaxDirIn(home); got != env {
		t.Fatalf("MINIMAX_DATA_DIR: %q", got)
	}
}

// quote is s as a JSON string, a Windows path's backslashes escaped.
func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
