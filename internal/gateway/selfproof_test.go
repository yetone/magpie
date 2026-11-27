package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// GET /?self=<nonce> proves this is the magpie asking: the answer is this
// magpie's proof for the nonce, which another magpie (another settings
// folder, another secret) can't give, and every process of this one gives
// alike, from the secret the first one made.
func TestInfoProvesItIsThisMagpie(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ask := func(nonce string) string {
		srv := httptest.NewServer(New().Handler())
		defer srv.Close()
		res, err := http.Get(srv.URL + "/?" + SelfParam + "=" + nonce)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var info struct {
			Name, Self string
		}
		json.NewDecoder(res.Body).Decode(&info)
		if info.Name != "magpie" {
			t.Fatalf("info: %+v", info)
		}
		return info.Self
	}
	proofs := make([]string, 3)
	for i := range proofs {
		// a process of its own: what it read is forgotten
		selfKey.Lock()
		selfKey.path = ""
		selfKey.Unlock()
		proofs[i] = ask("n1")
	}
	for _, p := range proofs {
		if !IsSelfProof("n1", p) {
			t.Fatalf("proofs disagree: %q", proofs)
		}
	}
	if IsSelfProof("n2", proofs[0]) || IsSelfProof("n1", "n1") || IsSelfProof("n1", "") {
		t.Error("a proof for another nonce, or an echo, taken")
	}
	if ask("") != "" {
		t.Error("a proof without a nonce")
	}
	// another magpie: its own settings folder
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	other := ask("n1")
	if other == "" || other == proofs[0] {
		t.Errorf("another magpie answered %q, this one %q", other, proofs[0])
	}
}
