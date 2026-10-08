package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

func TestCodexModelListWithoutChatGPT(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			fresh(t)
			twoProviders(t, &fake{t: t}, &fake{t: t})
			chatgpt(t, func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(status)
				io.WriteString(writer, `{"error":{"message":"ChatGPT sign-in required"}}`)
			})
			for _, group := range []provider.Group{
				{Name: "Both", Members: []string{"plan/m1", "spare/m2"}},
				{Name: "Solo", Members: []string{"plan/m1"}},
			} {
				if err := provider.SaveGroup(group); err != nil {
					t.Fatal(err)
				}
			}
			cache := catalog.CodexModelsCache()
			if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cache, []byte(`{"models":[{"slug":"gpt-5.5","base_instructions":"cached ChatGPT model"}]}`), 0o600); err != nil {
				t.Fatal(err)
			}
			keys, secrets := newCaller(t, "Catalog client")
			handler := lanGuard(New().Handler())
			for _, test := range []struct {
				name     string
				models   []string
				accounts []string
				allowed  []string
				denied   []string
			}{
				{name: "unrestricted", allowed: []string{"plan/m1", "spare/m2", "group/both", "group/solo"}},
				{name: "model restriction", models: []string{"plan/*"}, allowed: []string{"plan/m1", "group/solo"}, denied: []string{"spare/m2", "group/both"}},
				{name: "named group", models: []string{"group/both"}, allowed: []string{"group/both"}, denied: []string{"plan/m1", "spare/m2", "group/solo"}},
				{name: "account restriction", accounts: []string{"plan/" + provider.KeyID("k")}, allowed: []string{"spare/m2", "group/both"}, denied: []string{"plan/m1", "group/solo"}},
				{name: "no allowed models", models: []string{"plan/*"}, accounts: []string{"plan/" + provider.KeyID("k")}, denied: []string{"plan/m1", "spare/m2", "group/both", "group/solo"}},
			} {
				t.Run(test.name, func(t *testing.T) {
					if len(test.accounts) > 0 {
						if err := provider.SetAccountModels("plan", provider.KeyID("k"), []string{"other"}); err != nil {
							t.Fatal(err)
						}
					}
					if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: test.models}); err != nil {
						t.Fatal(err)
					}
					if _, err := access.Update("accounts-key", access.Change{Key: keys[0].ID, Accounts: test.accounts}); err != nil {
						t.Fatal(err)
					}
					request := httptest.NewRequest(http.MethodGet, CodexPath+"/models", nil)
					request.Header.Set("Authorization", "Bearer "+secrets[0])
					recorder := httptest.NewRecorder()
					handler.ServeHTTP(recorder, request)
					var list struct{ Models []struct{ Slug string } }
					if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
						t.Fatal(err)
					}
					listed := map[string]bool{}
					for _, model := range list.Models {
						listed[model.Slug] = true
					}
					for _, model := range test.allowed {
						if !listed[model] {
							t.Errorf("authorized model %q unavailable after ChatGPT rejected authentication: %s", model, recorder.Body.String())
						}
					}
					for _, model := range append(test.denied, "gpt-5.5") {
						if listed[model] {
							t.Errorf("caller can read unauthorized model %q", model)
						}
					}
				})
			}
			for _, secret := range []string{"", access.Prefix + "invalid"} {
				request := httptest.NewRequest(http.MethodGet, CodexPath+"/models", nil)
				request.Header.Set("Authorization", "Bearer "+secret)
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, request)
				if strings.Contains(recorder.Body.String(), "spare/m2") {
					t.Fatal("unauthenticated caller can read the model catalog")
				}
			}
		})
	}
}
