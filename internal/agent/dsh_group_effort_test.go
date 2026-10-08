package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// ARNO on Discord: routing group 在接入 harness 的时候，没有设置
// reasoning_efforts. A group of deepseek-v4.1-flash from a provider that
// lists its levels and from one whose list says it thinks but gives no
// levels (a Volcengine endpoint's) was written into dsh's route without
// reasoningEfforts, so dsh offered no thinking for it while it did for the
// member alone: the member with no levels to pick from took the others'
// away, though the gateway sends it whatever effort is asked, up to high.
func TestDshGroupHasItsMembersLevels(t *testing.T) {
	home, _, web := dshRouteHome(t)
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n[]\n"), 0o644)
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	if err := os.WriteFile(catalog.CachePath(), []byte(`{
	  "deepseek": {"models": {"deepseek-v4.1-flash": {"id":"deepseek-v4.1-flash","name":"DeepSeek V4.1 Flash","reasoning":true,
	    "reasoning_options":[{"type":"toggle"},{"type":"effort","values":["low","high","max"]}],"limit":{"context":1000000}}}},
	  "volcengine": {"models": {"deepseek-v4.1-flash": {"id":"deepseek-v4.1-flash","name":"DeepSeek V4.1 Flash","reasoning":true,
	    "limit":{"context":1000000}}}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for _, p := range []provider.Provider{
		{ID: "ds", Name: "DS", Key: "k", Chat: "http://127.0.0.1:1/v1", Catalog: "deepseek", Models: []string{"deepseek-v4.1-flash"}},
		{ID: "volc", Name: "Volc", Key: "k", Chat: "http://127.0.0.1:1/v1", Catalog: "volcengine", Models: []string{"deepseek-v4.1-flash"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "flash", Name: "Flash", Members: []string{"volc/deepseek-v4.1-flash", "ds/deepseek-v4.1-flash"}}); err != nil {
		t.Fatal(err)
	}
	a := dsh(home)
	if err := a.Field("model").Set("magpie/group/flash"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(web)
	s := string(b)
	at := strings.Index(s, "- id: group/flash\n")
	if at < 0 {
		t.Fatalf("no group in the route:\n%s", s)
	}
	entry := s[at:]
	if end := strings.Index(entry[1:], "- id: "); end >= 0 {
		entry = entry[:end+1]
	}
	if !strings.Contains(entry, "reasoningEfforts:\n") || !strings.Contains(entry, "low: low") || !strings.Contains(entry, "max: max") {
		t.Fatalf("the group's entry has no reasoningEfforts:\n%s", entry)
	}
	var offered []string
	for _, o := range a.Field("effort").Options(map[string]string{"model": "magpie/group/flash"}) {
		offered = append(offered, o.Value)
	}
	if !reflect.DeepEqual(offered, []string{"off", "low", "high", "max"}) {
		t.Fatalf("thinking offered for the group: %v", offered)
	}
}
