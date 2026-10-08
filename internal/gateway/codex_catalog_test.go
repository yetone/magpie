package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// codexModelInfo is what codex-rs's ModelInfo can't do without
// (protocol/src/openai_models.rs): a field missing here fails the whole
// list there, "failed to decode models response".
type codexModelInfo struct {
	Slug             *string           `json:"slug"`
	DisplayName      *string           `json:"display_name"`
	BaseInstructions *string           `json:"base_instructions"`
	Levels           *[]map[string]any `json:"supported_reasoning_levels"`
	ShellType        *string           `json:"shell_type"`
	Visibility       *string           `json:"visibility"`
	SupportedInAPI   *bool             `json:"supported_in_api"`
	Priority         *int              `json:"priority"`
	Verbosity        *bool             `json:"support_verbosity"`
	Truncation       *struct {
		Mode  string `json:"mode"`
		Limit int    `json:"limit"`
	} `json:"truncation_policy"`
	Tools *[]string `json:"experimental_supported_tools"`
}

// A Codex on another computer that names magpie its model_provider, with a
// gateway key (#1281), reads magpie's models from /v1/codex/models in
// Codex's own shape — through the server as it runs, lanGuard and all —
// and sees only the models its key may use; another computer without a key
// gets nothing, and /v1/models stays OpenAI's list.
func TestCodexCatalogForAGatewayKey(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	plan := &fake{t: t, ctype: "application/json", reply: `{}`}
	spare := &fake{t: t, ctype: "application/json", reply: `{}`}
	twoProviders(t, plan, spare)
	keys, secrets := newCaller(t, "Held", "Free")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"plan/*"}}); err != nil {
		t.Fatal(err)
	}
	h := lanGuard(New().Handler())
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = "192.168.1.9:5000" // another computer
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(host.Close)
	get := func(path, key string) (*http.Response, []byte) {
		req, _ := http.NewRequest("GET", host.URL+path, nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, b
	}
	// what Codex asks: model_catalog_url with ?client_version= after it
	slugs := func(key string) []string {
		t.Helper()
		res, b := get(CodexCatalogPath+"?client_version=0.161.0", key)
		if res.StatusCode != 200 {
			t.Fatalf("%d %s", res.StatusCode, b)
		}
		var list struct {
			Models []codexModelInfo `json:"models"`
		}
		if err := json.Unmarshal(b, &list); err != nil {
			t.Fatal(err, string(b))
		}
		var out []string
		for _, m := range list.Models {
			if m.Slug == nil || m.DisplayName == nil || m.BaseInstructions == nil || *m.BaseInstructions == "" ||
				m.Levels == nil || m.ShellType == nil || m.Visibility == nil || m.SupportedInAPI == nil ||
				m.Priority == nil || m.Verbosity == nil || m.Truncation == nil || m.Tools == nil {
				t.Fatalf("an entry Codex can't read: %s", b)
			}
			if slices.Contains(out, *m.Slug) {
				t.Fatalf("slug %s twice: Codex refuses the list", *m.Slug)
			}
			out = append(out, *m.Slug)
		}
		return out
	}
	if got := slugs(secrets[1]); !slices.Contains(got, "plan/m1") || !slices.Contains(got, "spare/m2") {
		t.Fatalf("free key: %v", got)
	}
	// a key held to some models is listed only those, as on /v1/models
	if got := slugs(secrets[0]); !slices.Contains(got, "plan/m1") || slices.Contains(got, "spare/m2") {
		t.Fatalf("held key: %v", got)
	}
	if res, b := get(CodexCatalogPath, ""); res.StatusCode != 401 {
		t.Fatalf("no key: %d %s", res.StatusCode, b)
	}
	if res, b := get(CodexCatalogPath, "sk-not-a-key"); res.StatusCode != 401 {
		t.Fatalf("a wrong key: %d %s", res.StatusCode, b)
	}
	// /v1/models is OpenAI's list as it was
	res, b := get("/v1/models", secrets[1])
	var l struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models any `json:"models"`
	}
	if json.Unmarshal(b, &l); res.StatusCode != 200 || l.Object != "list" || len(l.Data) == 0 || l.Models != nil {
		t.Fatalf("/v1/models: %d %s", res.StatusCode, b)
	}
}

// Codex reads at most 1 MiB of a model_catalog_url and refuses the whole
// list past it; every entry carries Codex's prompt (~8 KB), so a gateway
// with hundreds of models hands Codex as many as fit, and says how many it
// left out, rather than a list Codex loads none of.
func TestCodexCatalogFitsCodexsLimit(t *testing.T) {
	fresh(t)
	var ms []string
	for i := range 300 {
		ms = append(ms, fmt.Sprintf("model-%03d", i))
	}
	if err := provider.Save(provider.Provider{ID: "many", Name: "Many", Key: "k", Chat: "http://127.0.0.1:9/v1", Models: ms}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	New().Handler().ServeHTTP(w, httptest.NewRequest("GET", CodexCatalogPath, nil))
	if w.Code != 200 || w.Body.Len() > codexCatalogMost {
		t.Fatalf("%d, %d bytes", w.Code, w.Body.Len())
	}
	var list struct {
		Models []codexModelInfo `json:"models"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	out, _ := strconv.Atoi(w.Header().Get("X-Magpie-Left-Out"))
	t.Logf("%d listed in %d bytes, %d left out", len(list.Models), w.Body.Len(), out)
	if len(list.Models) < 100 || out == 0 || len(list.Models)+out != 300 {
		t.Fatalf("%d listed, %d left out", len(list.Models), out)
	}
	if first := *list.Models[0].Slug; !strings.HasPrefix(first, "many/model-000") {
		t.Fatalf("the first kept is %s", first)
	}
}
