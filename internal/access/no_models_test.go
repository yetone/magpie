package access

import (
	"slices"
	"testing"
	"time"
)

// ifitscolorful on X: an MCP gateway's key must not be able to spend the
// subscriptions. "-" alone holds a key to no model (NoModels), as NoMCP
// holds one to no MCP server; beside a model it is dropped. Such a key
// allows no model, is ModelsOff and not MCPOff, and every key it issues
// is held to no model too, whatever it asks for. A limit can't count it,
// and the provider editor leaves the key out.
func TestNoModelsKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	clockAt(t, time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC))
	if got, err := CleanModels([]string{" - "}); err != nil || !slices.Equal(got, []string{NoModels}) {
		t.Fatal("none:", got, err)
	}
	if got, err := CleanModels([]string{NoModels, "openai/gpt-5"}); err != nil || !slices.Equal(got, []string{"openai/gpt-5"}) {
		t.Fatal("none beside a model:", got, err)
	}

	id, secret := addKey(t, "MCP gateway")
	if _, err := Update("models-key", Change{Key: id, Models: []string{NoModels}}); err != nil {
		t.Fatal(err)
	}
	who, ok := Authenticate(secret)
	if !ok || !who.ModelsOff() || who.MCPOff() || !who.Restricted() {
		t.Fatalf("%+v %v", who, ok)
	}
	for _, m := range []string{"openai/gpt-5", "-", "group/smart", "x"} {
		if who.Allows(m) {
			t.Errorf("a key held to no model may use %q", m)
		}
	}
	full, fsecret := addKey(t, "Full")
	if fw, _ := Authenticate(fsecret); fw.ModelsOff() || fw.MCPOff() {
		t.Fatalf("a new key: %+v", fw)
	}
	if _, err := Update("mcp-key", Change{Key: full, MCP: []string{NoMCP}}); err != nil {
		t.Fatal(err)
	}
	if fw, _ := Authenticate(fsecret); fw.ModelsOff() || !fw.MCPOff() {
		t.Fatalf("a key for models only: %+v", fw)
	}

	// the keys it issues
	if _, err := Update("issuer-key", Change{Key: id, Issues: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := IssueKey(Issue{TTL: time.Hour, Issuer: id, Models: []string{"openai/gpt-5"}}); err == nil {
		t.Fatal("a key held to no model issued one that may use a model")
	}
	child, err := IssueKey(Issue{TTL: time.Hour, Issuer: id})
	if err != nil {
		t.Fatal(err)
	}
	if cw, ok := Authenticate(child.Key); !ok || !cw.ModelsOff() || cw.Allows("openai/gpt-5") {
		t.Fatalf("issued under a key held to no model: %+v %v", cw, ok)
	}
	// widening the child afterwards doesn't widen it
	if _, err := Update("models-key", Change{Key: child.ID, Models: []string{"openai/*"}}); err != nil {
		t.Fatal(err)
	}
	if cw, _ := Authenticate(child.Key); !cw.ModelsOff() || cw.Allows("openai/gpt-5") {
		t.Fatalf("a child widened past its issuer: %+v", cw)
	}

	// a key held to no model issued no-model under a full issuer
	if _, err := Update("issuer-key", Change{Key: full, Issues: true}); err != nil {
		t.Fatal(err)
	}
	none, err := IssueKey(Issue{TTL: time.Hour, Issuer: full, Models: []string{NoModels}})
	if err != nil {
		t.Fatal(err)
	}
	if cw, _ := Authenticate(none.Key); !cw.ModelsOff() || !cw.MCPOff() {
		t.Fatalf("a no-model key issued under a models-only one: %+v", cw)
	}

	if _, err := (&Limit{Period: "day", Tokens: 10, Models: []string{NoModels}}).Valid(); err == nil {
		t.Error("a limit counts no model")
	}

	keys, err := List()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if k.ID == id && modelKey(k) {
			t.Error("the provider editor lists a key for MCP servers only")
		}
		if k.ID == full && !modelKey(k) {
			t.Error("the provider editor leaves out a key for models only")
		}
	}
}
