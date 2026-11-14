package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// failRestoreOnce makes the write that carries marker fail once: where the
// config file stood, a folder takes its place, so the write that would put
// the originals back cannot. It gives the config file back afterwards.
func failRestoreOnce(t *testing.T, path, marker string, fn func() error) (injected bool, err error) {
	t.Helper()
	real, rerr := filepath.EvalSymlinks(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	held := path + ".held"
	err = edit.Filtered(func(p string, data []byte) []byte {
		if p == real && bytes.Contains(data, []byte(marker)) && !injected {
			injected = true
			if rerr := os.Rename(path, held); rerr != nil {
				t.Logf("rename aside: %v", rerr)
			} else if merr := os.Mkdir(path, 0o700); merr != nil {
				t.Logf("mkdir: %v", merr)
			}
		}
		return data
	}, fn)
	if injected {
		if rerr := os.Remove(path); rerr != nil {
			t.Fatalf("undoing the injected folder: %v", rerr)
		}
		if rerr := os.Rename(held, path); rerr != nil {
			t.Fatalf("putting the config file back: %v", rerr)
		}
	}
	return injected, err
}

func stashHolds(t *testing.T, want ...string) {
	t.Helper()
	b, err := json.Marshal(stashShared())
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range want {
		if !strings.Contains(string(b), w) {
			t.Fatalf("the stash lost %q: %s", w, b)
		}
	}
}

// #4258: a Disconnect whose restore write fails leaves the stashed
// originals stashed for the next try — they are taken out only once the
// write that replaces them has gone through. A key another change wrote
// meanwhile is not the retry's to lose either.
func TestDisconnectFailureKeepsStashedOriginals(t *testing.T) {
	t.Run("claude", func(t *testing.T) {
		home := syncHome(t)
		path := filepath.Join(home, ".claude", "settings.json")
		writeFile(t, path, `{"model":"native-original","env":{"ANTHROPIC_BASE_URL":"https://original.example","ANTHROPIC_AUTH_TOKEN":"original-token"}}`)
		a := claude(home)
		if err := a.Connect(); err != nil {
			t.Fatal(err)
		}
		wired, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		injected, err := failRestoreOnce(t, path, "https://original.example", a.Disconnect)
		if !injected {
			t.Fatal("no write carried the original endpoint back")
		}
		if err == nil {
			t.Fatal("the restore write failed but Disconnect said it went through")
		}
		stashHolds(t, "https://original.example", "original-token", "native-original")

		// the write that failed is tried again on a wired config
		if err := os.WriteFile(path, wired, 0o644); err != nil {
			t.Fatal(err)
		}
		stash(map[string]string{"concurrent.probe": "keep"})
		if err := a.Disconnect(); err != nil {
			t.Fatalf("the retry: %v", err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range []string{"https://original.example", "original-token", "native-original"} {
			if !strings.Contains(string(body), w) {
				t.Fatalf("the retry lost %q:\n%s", w, body)
			}
		}
		if stashShared()["concurrent.probe"] != "keep" {
			t.Fatalf("the concurrent key went with the retry: %q", stashShared()["concurrent.probe"])
		}
	})

	t.Run("codex", func(t *testing.T) {
		home, _ := codexHome(t, "", "model = \"native-original\"\nmodel_provider = \"native-provider\"\n")
		path := filepath.Join(home, ".codex", "config.toml")
		cx := codex(home)
		if err := cx.Connect(); err != nil {
			t.Fatal(err)
		}
		wired, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		injected, err := failRestoreOnce(t, path, "native-provider", cx.Disconnect)
		if !injected {
			t.Fatal("no write carried the original provider back")
		}
		if err == nil {
			t.Fatal("the restore write failed but Disconnect said it went through")
		}
		stashHolds(t, "native-original", "native-provider")

		if err := os.WriteFile(path, wired, 0o644); err != nil {
			t.Fatal(err)
		}
		stash(map[string]string{"concurrent.probe": "keep"})
		if err := cx.Disconnect(); err != nil {
			t.Fatalf("the retry: %v", err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range []string{"native-provider", "native-original"} {
			if !strings.Contains(string(body), w) {
				t.Fatalf("the retry lost %q:\n%s", w, body)
			}
		}
		if stashShared()["concurrent.probe"] != "keep" {
			t.Fatalf("the concurrent key went with the retry: %q", stashShared()["concurrent.probe"])
		}
	})
}
