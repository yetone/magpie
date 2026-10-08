//go:build !windows

package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// The Restart button runs `codex app-server daemon restart` with the codex
// magpie finds, for the Codex home it switches; a stand-in codex writes
// down how it was run, since the real one would restart the user's daemon.
func TestCodexDaemonRestartRoute(t *testing.T) {
	h := sandboxHome(t)
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	t.Setenv("CODEX_HOME", "/somewhere/else")
	rec := filepath.Join(bin, "ran")
	script := "#!/bin/sh\necho \"$@|$CODEX_HOME\" > '" + rec + "'\n[ \"$FAIL\" = 1 ] && { echo 'Error: no daemon' >&2; exit 1; }\nexit 0\n"
	testenv.Program(t, filepath.Join(bin, "codex"), script)
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(action string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/codex/daemon/"+action, strings.NewReader("{}")))
		return w
	}
	w := post("restart")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var st providersJSON
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || st.CodexDaemon != "" {
		t.Fatalf("state %v %q", err, st.CodexDaemon)
	}
	b, _ := os.ReadFile(rec)
	if got, want := strings.TrimSpace(string(b)), "app-server daemon restart|"+filepath.Join(h, ".codex"); got != want {
		t.Fatalf("ran %q, want %q", got, want)
	}
	t.Setenv("FAIL", "1")
	if w := post("restart"); w.Code != 400 || !strings.Contains(w.Body.String(), "no daemon") {
		t.Fatalf("failing codex: %d %s", w.Code, w.Body)
	}
	if w := post("dismiss"); w.Code != 200 {
		t.Fatalf("dismiss: %d %s", w.Code, w.Body)
	}
	if w := post("nope"); w.Code != 404 {
		t.Fatalf("unknown action: %d", w.Code)
	}
}
