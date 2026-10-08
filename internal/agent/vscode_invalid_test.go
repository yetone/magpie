package agent

import (
	"path/filepath"
	"testing"
)

func TestVSCodeConnectPreservesInvalidConfig(t *testing.T) {
	for _, file := range []string{"settings.json", "chatLanguageModels.json"} {
		t.Run(file, func(t *testing.T) {
			a := vscodeAt(filepath.Join(syncHome(t), "vscode", "User"))
			writeFile(t, a.Path, `{"chat.defaultModel":"auto"}`)
			models := filepath.Join(a.Dir, "chatLanguageModels.json")
			writeFile(t, models, `[{"name":"mine","vendor":"ollama"}]`)
			path, bad := a.Path, `{"chat.defaultModel":"auto" "editor.fontSize":14}`
			if file == "chatLanguageModels.json" {
				path, bad = models, `[{"name":"mine","vendor":"ollama"} {"name":"other"}]`
			}
			writeFile(t, path, bad)
			if err := a.Connect(); err == nil {
				t.Error("connecting VS Code with malformed config succeeded")
			}
			if got := readFile(path); got != bad {
				t.Errorf("connecting VS Code changed malformed %s: %q", file, got)
			}
		})
	}
}
