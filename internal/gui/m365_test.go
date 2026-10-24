package gui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
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

func TestM365OffRemovesItsOriginAndCertificateTrust(t *testing.T) {
	sandboxHome(t)
	s := settings.Load()
	s.M365 = true
	s.CORSOrigins = []string{"https://app.example.com", gateway.M365Origin}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}

	oldRemove := removeM365Certificate
	removed := false
	removeM365Certificate = func(context.Context) (gateway.M365CertificateStatus, error) {
		removed = true
		return gateway.M365CertificateStatus{}, nil
	}
	t.Cleanup(func() { removeM365Certificate = oldRemove })
	oldStop := stopM365Gateway
	stopped := false
	stopM365Gateway = func(context.Context) error {
		stopped = true
		return nil
	}
	t.Cleanup(func() { stopM365Gateway = oldStop })

	oldServed := served.Load()
	served.Store(gateway.New())
	t.Cleanup(func() { served.Store(oldServed) })
	oldRestart := restartM365Gateway
	restartM365Gateway = func() gatewayFixJSON { return gatewayFixJSON{OK: true} }
	t.Cleanup(func() { restartM365Gateway = oldRestart })

	mux := http.NewServeMux()
	m365Routes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/settings/m365", strings.NewReader(`{"on":false}`)))
	got := settings.Load()
	if w.Code != http.StatusOK || got.M365 || !removed || !stopped || !slices.Equal(got.CORSOrigins, []string{"https://app.example.com"}) {
		t.Fatalf("off: status=%d settings=%+v removed=%v stopped=%v body=%s", w.Code, got, removed, stopped, w.Body.String())
	}
}

func TestM365OffReportsListenerStopFailureButKeepsSecurityBoundaryOff(t *testing.T) {
	sandboxHome(t)
	s := settings.Load()
	s.M365 = true
	s.CORSOrigins = []string{gateway.M365Origin}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}

	oldRemove := removeM365Certificate
	removeM365Certificate = func(context.Context) (gateway.M365CertificateStatus, error) {
		return gateway.M365CertificateStatus{}, nil
	}
	t.Cleanup(func() { removeM365Certificate = oldRemove })
	oldStop := stopM365Gateway
	stopM365Gateway = func(context.Context) error { return errors.New("shutdown timed out") }
	t.Cleanup(func() { stopM365Gateway = oldStop })
	oldServed := served.Load()
	served.Store(nil)
	t.Cleanup(func() { served.Store(oldServed) })

	mux := http.NewServeMux()
	m365Routes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/settings/m365", strings.NewReader(`{"on":false}`)))
	got := settings.Load()
	if w.Code != http.StatusOK || got.M365 || slices.Contains(got.CORSOrigins, gateway.M365Origin) || !strings.Contains(w.Body.String(), `"stopWarning":"shutdown timed out"`) {
		t.Fatalf("off after stop failure: status=%d settings=%+v body=%s", w.Code, got, w.Body.String())
	}
}

func TestM365OffStaysOffWhenTrustRemovalFailsAndNoGatewayIsServed(t *testing.T) {
	sandboxHome(t)
	s := settings.Load()
	s.M365 = true
	s.CORSOrigins = []string{"https://app.example.com", gateway.M365Origin}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}

	oldRemove := removeM365Certificate
	removed := false
	removeM365Certificate = func(context.Context) (gateway.M365CertificateStatus, error) {
		removed = true
		return gateway.M365CertificateStatus{}, errors.New("the trust prompt was canceled")
	}
	t.Cleanup(func() { removeM365Certificate = oldRemove })
	oldStop := stopM365Gateway
	stopped := false
	stopM365Gateway = func(context.Context) error {
		stopped = true
		return nil
	}
	t.Cleanup(func() { stopM365Gateway = oldStop })
	oldServed := served.Load()
	served.Store(nil)
	t.Cleanup(func() { served.Store(oldServed) })
	oldRestart := restartM365Gateway
	restarted := false
	restartM365Gateway = func() gatewayFixJSON {
		restarted = true
		return gatewayFixJSON{OK: true}
	}
	t.Cleanup(func() { restartM365Gateway = oldRestart })

	mux := http.NewServeMux()
	m365Routes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/settings/m365", strings.NewReader(`{"on":false}`)))
	got := settings.Load()
	if w.Code != http.StatusOK || got.M365 || !removed || !stopped || restarted || !slices.Equal(got.CORSOrigins, []string{"https://app.example.com"}) || !strings.Contains(w.Body.String(), `"trustWarning":"the trust prompt was canceled"`) {
		t.Fatalf("off after trust failure: status=%d settings=%+v removed=%v stopped=%v restarted=%v body=%s", w.Code, got, removed, stopped, restarted, w.Body.String())
	}
}

func TestM365AutomaticCertificateTrustIsMacOSOnly(t *testing.T) {
	sandboxHome(t)
	oldAutomatic := m365AutomaticTrust
	m365AutomaticTrust = false
	t.Cleanup(func() { m365AutomaticTrust = oldAutomatic })

	if settingsState().M365CertificateActions {
		t.Fatal("non-macOS settings exposed automatic certificate trust")
	}
	mux := http.NewServeMux()
	m365Routes(mux)
	for _, action := range []string{"install", "remove"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/settings/m365/certificate/"+action, strings.NewReader(`{}`)))
		if w.Code == http.StatusOK || !strings.Contains(w.Body.String(), "only on macOS") {
			t.Fatalf("%s automatic trust: status=%d body=%s", action, w.Code, w.Body.String())
		}
	}
}

func TestM365OffOnNonMacOSAlwaysSaysHowToRemoveManualTrust(t *testing.T) {
	sandboxHome(t)
	s := settings.Load()
	s.M365 = true
	s.CORSOrigins = []string{gateway.M365Origin}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}

	oldAutomatic := m365AutomaticTrust
	m365AutomaticTrust = false
	t.Cleanup(func() { m365AutomaticTrust = oldAutomatic })
	oldStop := stopM365Gateway
	stopM365Gateway = func(context.Context) error { return nil }
	t.Cleanup(func() { stopM365Gateway = oldStop })
	oldServed := served.Load()
	served.Store(nil)
	t.Cleanup(func() { served.Store(oldServed) })
	mux := http.NewServeMux()
	m365Routes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/settings/m365", strings.NewReader(`{"on":false}`)))
	got := settings.Load()
	if w.Code != http.StatusOK || got.M365 || slices.Contains(got.CORSOrigins, gateway.M365Origin) || !strings.Contains(w.Body.String(), `"trustWarning"`) || !strings.Contains(w.Body.String(), "remove it from the current user's trust store by hand") {
		t.Fatalf("non-macOS off did not require manual cleanup: status=%d settings=%+v body=%s", w.Code, got, w.Body.String())
	}
}

func TestCORSSettingCannotDriftFromM365Switch(t *testing.T) {
	sandboxHome(t)
	s := settings.Load()
	s.M365 = true
	s.CORSOrigins = []string{gateway.M365Origin}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}

	srv := Handler(nil, nil)
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/settings/cors", strings.NewReader(body))
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	if w := post(`{"origins":[]}`); w.Code != http.StatusOK || !slices.Contains(settings.Load().CORSOrigins, gateway.M365Origin) {
		t.Fatalf("on removed origin: %d %s %v", w.Code, w.Body.String(), settings.Load().CORSOrigins)
	}
	s = settings.Load()
	s.M365 = false
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if w := post(`{"origins":["https://pivot.claude.ai","https://app.example.com"]}`); w.Code != http.StatusOK || !slices.Equal(settings.Load().CORSOrigins, []string{"https://app.example.com"}) {
		t.Fatalf("off retained origin: %d %s %v", w.Code, w.Body.String(), settings.Load().CORSOrigins)
	}
}

func TestM365StartFailureRollsBackOriginAndSetting(t *testing.T) {
	sandboxHome(t)
	s := settings.Load()
	s.CORSOrigins = []string{"https://app.example.com"}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}

	oldRemove := removeM365Certificate
	removeM365Certificate = func(context.Context) (gateway.M365CertificateStatus, error) {
		return gateway.M365CertificateStatus{}, nil
	}
	t.Cleanup(func() { removeM365Certificate = oldRemove })
	oldServed := served.Load()
	served.Store(gateway.New())
	t.Cleanup(func() { served.Store(oldServed) })
	oldRestart := restartM365Gateway
	restartM365Gateway = func() gatewayFixJSON {
		m365.SetError(errors.New("listen for Microsoft 365 at https://127.0.0.1:8787: address already in use"))
		return gatewayFixJSON{OK: true}
	}
	t.Cleanup(func() { restartM365Gateway = oldRestart })
	t.Cleanup(m365.ClearError)

	mux := http.NewServeMux()
	m365Routes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/settings/m365", strings.NewReader(`{"on":true}`)))
	got := settings.Load()
	if w.Code == http.StatusOK || got.M365 || slices.Contains(got.CORSOrigins, gateway.M365Origin) || !strings.Contains(w.Body.String(), "address already in use") {
		t.Fatalf("failure did not roll back: status=%d settings=%+v body=%s", w.Code, got, w.Body.String())
	}
}

func TestM365GatewayRestartFailureRollsBackOriginAndSetting(t *testing.T) {
	sandboxHome(t)
	s := settings.Load()
	s.CORSOrigins = []string{"https://app.example.com"}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}

	oldRemove := removeM365Certificate
	removed := false
	removeM365Certificate = func(context.Context) (gateway.M365CertificateStatus, error) {
		removed = true
		return gateway.M365CertificateStatus{}, nil
	}
	t.Cleanup(func() { removeM365Certificate = oldRemove })
	oldStop := stopM365Gateway
	stopM365Gateway = func(context.Context) error { return nil }
	t.Cleanup(func() { stopM365Gateway = oldStop })
	oldServed := served.Load()
	served.Store(gateway.New())
	t.Cleanup(func() { served.Store(oldServed) })
	oldRestart := restartM365Gateway
	restartM365Gateway = func() gatewayFixJSON {
		return gatewayFixJSON{Reason: "taken", Error: "address already in use"}
	}
	t.Cleanup(func() { restartM365Gateway = oldRestart })

	mux := http.NewServeMux()
	m365Routes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/settings/m365", strings.NewReader(`{"on":true}`)))
	got := settings.Load()
	if w.Code == http.StatusOK || got.M365 || !removed || slices.Contains(got.CORSOrigins, gateway.M365Origin) || !slices.Equal(got.CORSOrigins, []string{"https://app.example.com"}) || !strings.Contains(w.Body.String(), "address already in use") {
		t.Fatalf("restart failure did not roll back: status=%d settings=%+v removed=%v body=%s", w.Code, got, removed, w.Body.String())
	}
}
