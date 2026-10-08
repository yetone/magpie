package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
)

func TestM365KeyIsNamedReusedAndCopiedOnlyExplicitly(t *testing.T) {
	sandboxHome(t)
	s := settings.Load()
	if err := ensureM365Key(&s); err != nil {
		t.Fatal(err)
	}
	if s.M365KeyID == "" {
		t.Fatal("created key has no id")
	}
	secret, err := access.Update("copy-key", access.Change{Key: s.M365KeyID})
	if err != nil {
		t.Fatal(err)
	}
	if who, ok := access.Authenticate(secret); !ok || who.KeyID != s.M365KeyID || who.KeyName != m365KeyName {
		t.Fatalf("key identity: %+v %v", who, ok)
	}
	firstID := s.M365KeyID
	if err := ensureM365Key(&s); err != nil || s.M365KeyID != firstID {
		t.Fatalf("reused key: %+v %v", s, err)
	}
	s.M365 = true
	s.CORSOrigins = []string{gateway.M365Origin}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	m365Routes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/settings/m365/key", strings.NewReader(`{}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("copy: %d %s", w.Code, w.Body)
	}
	var out map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out["secret"] != secret {
		t.Fatalf("copy response: %v %v", out, err)
	}
	state := settingsState()
	if state.M365KeyName != m365KeyName || state.M365KeyMasked == "" || strings.Contains(state.M365KeyMasked, secret) {
		t.Fatalf("state exposed or lost key: %+v", state)
	}
}
