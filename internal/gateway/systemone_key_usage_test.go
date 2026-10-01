package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

func TestSystemOneUsageNamesProviderKey(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			fresh(t)
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer fixture-systemone-secret" {
					t.Error("System One did not use its provider key")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				io.WriteString(w, `{"model":"jev","usage":{"input_tokens":120,"output_tokens":2}}`)
			}))
			defer up.Close()
			if err := provider.Save(provider.Provider{ID: "jev", Name: "Jev", Key: "fixture-systemone-secret", KeyName: "Decisions", Decide: up.URL + "/v1"}); err != nil {
				t.Fatal(err)
			}
			code, _ := sendTo(New(), "/v1/systemone", `{"model":"jev/jev-latest","state":{"message":"hi"},"questions":{}}`)
			if code != status {
				t.Fatal(code)
			}
			r := lastUsage(t)
			if r.ProviderKeyID != provider.KeyID("fixture-systemone-secret") || r.ProviderKeyName != "Decisions" || r.Input != 120 || r.Output != 2 || r.Status != status {
				t.Fatalf("System One provider attribution: %+v", r)
			}
			encoded, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			if fields["providerKeyId"] != r.ProviderKeyID || fields["providerKeyName"] != "Decisions" || fields["keyId"] != nil || fields["keyName"] != nil {
				t.Fatalf("ambiguous provider-key JSON fields: %s", encoded)
			}
			if status == http.StatusOK {
				p, err := provider.Find("jev")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := New().systemOne(context.Background(), *p, "jev-latest", []byte(`{"questions":{}}`)); err != nil {
					t.Fatal(err)
				}
				if own := lastUsage(t); own.ProviderKeyID != r.ProviderKeyID || own.ProviderKeyName != "Decisions" {
					t.Fatalf("router's System One attribution: %+v", own)
				}
			}
			log, err := os.ReadFile(usage.Path())
			if err != nil || strings.Contains(string(log), "fixture-systemone-secret") {
				t.Fatal("raw credential in usage log", err)
			}
		})
	}
}

func TestSystemOneWithoutProviderKeyStaysUnattributed(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"usage":{"input_tokens":10,"output_tokens":1}}`)
	}))
	defer up.Close()
	p := provider.Provider{ID: "local-jev", Name: "Local Jev", KeyName: "unused", Decide: up.URL + "/v1"}
	if _, err := New().systemOne(context.Background(), p, "jev", []byte(`{"questions":{}}`)); err != nil {
		t.Fatal(err)
	}
	if r := lastUsage(t); r.ProviderKeyID != "" || r.ProviderKeyName != "" {
		t.Fatalf("keyless System One call acquired a provider identity: %+v", r)
	}
}
