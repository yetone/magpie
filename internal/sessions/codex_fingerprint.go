package sessions

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"

	"github.com/yetone/magpie/internal/appdir"
)

var codexFingerprintKey struct {
	sync.Mutex
	path string
	key  []byte
}

// CodexPromptDigest keeps prompt evidence comparable across restarts without
// publishing a guessable hash. The key stays in Magpie's local config directory.
func CodexPromptDigest(prompt string) string { return codexFingerprint("prompt", prompt) }

func codexFingerprint(kind, text string) string {
	if text == "" {
		return ""
	}
	key := codexTitleKey()
	if len(key) == 0 {
		return "" // unavailable private key: never fall back to an unkeyed hash
	}
	h := hmac.New(sha256.New, key)
	h.Write([]byte(kind + "\x00" + text))
	return "hmac-sha256:" + hex.EncodeToString(h.Sum(nil))
}

func codexTitleKey() []byte {
	path := filepath.Join(appdir.Config(), "codex-title-key")
	codexFingerprintKey.Lock()
	defer codexFingerprintKey.Unlock()
	if codexFingerprintKey.path == path && len(codexFingerprintKey.key) == 32 {
		return codexFingerprintKey.key
	}
	key, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil
		}
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil
		}
		// An exclusive create lets concurrent Magpie processes agree on one key.
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if os.IsExist(err) {
			key, err = os.ReadFile(path)
		} else if err == nil {
			_, err = file.Write(key)
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
		}
		if err != nil {
			return nil
		}
	} else if err != nil {
		return nil
	}
	if len(key) != 32 {
		return nil
	}
	codexFingerprintKey.path, codexFingerprintKey.key = path, key
	return key
}
