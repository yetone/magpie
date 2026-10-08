package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `magpie qwen a/m` writes its provider entries and the gateway's key,
// `magpie qwen default` takes magpie back out and restores the model the
// user had, their own provider entry and settings kept.
func TestAgentQwenDefault(t *testing.T) {
	groupsHome(t)
	path := filepath.Join(os.Getenv("HOME"), ".qwen", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	own := `{
  "env": {
    "IDEALAB_API_KEY": "team-key"
  },
  "modelProviders": {
    "openai": [
      {
        "baseUrl": "https://idealab.example.com/v1",
        "envKey": "IDEALAB_API_KEY",
        "id": "glm-5",
        "name": "[IdeaLab] glm-5"
      }
    ]
  },
  "model": {
    "baseUrl": "https://idealab.example.com/v1",
    "name": "glm-5"
  },
  "outputLanguage": "zh-CN"
}
`
	if err := os.WriteFile(path, []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	read := func() string {
		b, _ := os.ReadFile(path)
		return string(b)
	}
	if err := run([]string{"qwen", "a/m"}); err != nil {
		t.Fatal(err)
	}
	s := read()
	for _, want := range []string{`"id": "a/m"`, `"name": "magpie/a/m"`, `"MAGPIE_QWEN_API_KEY"`, `"selectedType": "openai"`, `"baseUrl": "https://idealab.example.com/v1"`, `"outputLanguage": "zh-CN"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("not wired (%s missing):\n%s", want, s)
		}
	}
	if err := run([]string{"qwen", "default"}); err != nil {
		t.Fatal(err)
	}
	if s = read(); s != own {
		t.Fatalf("default didn't put back what was there:\n%s", s)
	}
}
