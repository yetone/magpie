package sessions

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/appdir"
)

func TestCodexFingerprintsPrivateAndPersistent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const prompt = "hello"
	first := CodexPromptDigest(prompt)
	sum := sha256.Sum256([]byte(prompt))
	if first == "" || first == hex.EncodeToString(sum[:]) || first == CodexTitleDigest(prompt) {
		t.Fatal("fingerprint was unkeyed or not separated from title evidence")
	}
	path := filepath.Join(appdir.Config(), "codex-title-key")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("private key permissions: %o", info.Mode().Perm())
	}
	// Simulate restart, then a second installation with the same short prompt.
	codexFingerprintKey.Lock()
	codexFingerprintKey.key = nil
	codexFingerprintKey.Unlock()
	if CodexPromptDigest(prompt) != first {
		t.Fatal("restart changed evidence")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if second := CodexPromptDigest(prompt); second == "" || second == first {
		t.Fatal("different installations shared prompt fingerprints")
	}
}

func TestCodexFingerprintsConcurrentFirstUse(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var wg sync.WaitGroup
	results := make([]string, 32)
	for i := range results {
		wg.Go(func() { results[i] = CodexPromptDigest("first request") })
	}
	wg.Wait()
	for _, result := range results {
		if result == "" || result != results[0] {
			t.Fatal("concurrent requests used different private keys")
		}
	}
}

func TestCodexFingerprintsFailClosed(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(appdir.Config(), "codex-title-key")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid key"), 0600); err != nil {
		t.Fatal(err)
	}
	if CodexPromptDigest("hello") != "" || CodexTitleDigest("hello") != "" {
		t.Fatal("invalid private key produced public evidence")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if CodexPromptDigest("hello") != "" {
		t.Fatal("unreadable key produced evidence")
	}
}
