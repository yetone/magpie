package gui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// POST /api/signin/export (heyiwei5 and Rk1ngggg on X): the accounts are
// written 0600 into Downloads, in a folder of their own for CLIProxyAPI,
// and the answer names the file and never holds a token.
func TestSigninExportWritesPrivateFiles(t *testing.T) {
	h := sandboxHome(t)
	t.Setenv("CODEX_HOME", filepath.Join(h, ".codex"))
	if err := os.MkdirAll(filepath.Join(h, "Downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := func(m map[string]any) string {
			b, _ := json.Marshal(m)
			return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id_token": claims(map[string]any{"email": "a@example.com",
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro", "chatgpt_account_id": "acct-a"}}),
			"access_token":  claims(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": "SECRET-refresh",
		})
	}))
	defer fake.Close()
	t.Cleanup(provider.CodexTokenURLForTest(fake.URL))
	seed := `{"tokens":{"id_token":"x","access_token":"y","refresh_token":"r-a","account_id":"acct-a"}}`
	if res, err := provider.ImportLogins(context.Background(), "codex", []string{seed}); err != nil || len(res) == 0 || res[0].Status != "added" {
		t.Fatalf("%+v %v", res, err)
	}
	mux := http.NewServeMux()
	providerRoutes(mux, folderOnly{})
	post := func(body any) (int, string, map[string]any) {
		b, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/signin/export", strings.NewReader(string(b))))
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, w.Body.String(), out
	}
	for _, format := range []string{"sub2api", "cliproxyapi"} {
		code, raw, out := post(map[string]any{"agent": "codex", "format": format})
		if code != 200 {
			t.Fatalf("%s: %d %s", format, code, raw)
		}
		if strings.Contains(raw, "SECRET") {
			t.Errorf("%s: a token is in the answer: %s", format, raw)
		}
		p, _ := out["path"].(string)
		if !strings.HasPrefix(p, "~") {
			t.Fatalf("%s: path %q", format, p)
		}
		file := filepath.Join(h, strings.TrimPrefix(p, "~"))
		if format == "cliproxyapi" && !strings.Contains(file, "magpie-cliproxyapi-codex-") {
			t.Errorf("not in a folder of its own: %s", file)
		}
		data, err := os.ReadFile(file)
		if err != nil || !strings.Contains(string(data), "SECRET-refresh") {
			t.Fatalf("%s: %v %s", format, err, data)
		}
		if fi, _ := os.Stat(file); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
			t.Errorf("%s is %v, not 0600", file, fi.Mode().Perm())
		}
	}
	if code, _, _ := post(map[string]any{"agent": "codex", "format": "sub2api", "users": []string{"nobody@example.com"}}); code == 200 {
		t.Error("an account that isn't there was exported")
	}
	if code, _, _ := post(map[string]any{"agent": "gemini", "format": "sub2api"}); code == 200 {
		t.Error("gemini was exported")
	}
}
