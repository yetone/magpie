package provider

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

func TestAccountOrderValidation(t *testing.T) {
	for _, order := range [][]string{nil, {"a"}, {"a", "a"}, {"a", "other"}, {"a", "b", "c"}} {
		if validateAccountOrder([]string{"a", "b"}, order) == nil {
			t.Fatalf("accepted %v", order)
		}
	}
	if err := validateAccountOrder([]string{"a", "b"}, []string{"b", "a"}); err != nil {
		t.Fatal(err)
	}
}

func TestAccountOrderKeysAllRoutingModes(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, mode := range []string{"", Ordered, Rotate, LeastUsed} {
		p := Provider{ID: "arrange-test", Name: "Arrange", Chat: "https://example.invalid/v1", Key: "primary", KeyName: "Personal", KeyProtocol: Chat, Routing: mode,
			Keys: []KeyAccount{{Name: "Off", Key: "disabled", Off: true, Protocol: Chat}, {Name: "Extra", Key: "extra", Protocol: Anthropic}, {Name: "Last", Key: "last"}}}
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
		order := []string{keyID("extra"), keyID("last"), keyID("disabled"), keyID("primary")}
		if err := SetAccountOrder(p.ID, order); err != nil {
			t.Fatal(err)
		}
		got, err := Find(p.ID)
		if err != nil {
			t.Fatal(err)
		}
		var displayed []string
		for _, k := range got.KeyList() {
			displayed = append(displayed, k.ID)
		}
		if !reflect.DeepEqual(displayed, order) || got.Routing != mode {
			t.Fatalf("displayed=%v, mode=%q", displayed, got.Routing)
		}
		want := []KeyAccount{{Name: "Extra", Key: "extra", Protocol: Anthropic}, {Name: "Last", Key: "last"}, p.first()}
		if !reflect.DeepEqual(got.KeysOn(), want) || !got.Keys[1].Off {
			t.Fatalf("routing/credentials changed: %+v", got.KeysOn())
		}
		if got.KeyList()[0].ID != keyID(got.KeysOn()[0].Key) || !got.KeyList()[0].Active {
			t.Fatal("First does not match routing")
		}
		if load().Providers[0].Key != "extra" {
			t.Fatal("priority not persisted")
		}
		before, _ := os.ReadFile(Path())
		for _, invalid := range [][]string{{order[0], order[0], order[2], order[3]}, {order[2], order[0], order[1], order[3]}, order[:3]} {
			if err := SetAccountOrder(p.ID, invalid); err == nil {
				t.Fatalf("accepted %v", invalid)
			}
			after, _ := os.ReadFile(Path())
			if !bytes.Equal(before, after) {
				t.Fatal("invalid order changed the store")
			}
		}
		if err := UseKey(p.ID, keyID("primary")); err != nil {
			t.Fatal(err)
		}
		got, _ = Find(p.ID)
		if got.KeyList()[0].ID != keyID("primary") || got.KeysOn()[0].Key != "primary" {
			t.Fatal("Make first did not move routing and display together")
		}
	}
}

func loginRoutingUsers(p *Provider) []string {
	out := []string{p.Account.User}
	for _, q := range p.AlsoOn() {
		out = append(out, q.Account.User)
	}
	return out
}

func TestAccountOrderSideLoginsAllRoutingModes(t *testing.T) {
	googleSandbox(t, &fakeGoogle{})
	users := []string{"a@example.test", "b@example.test", "c@example.test", "off@example.test"}
	for _, user := range users {
		auth := googleAuth{AccessToken: "fixture", RefreshToken: "fixture-" + user, Expiry: time.Now().Add(time.Hour).UnixMilli()}
		if err := addGoogleLogin("antigravity", user, "Pro", auth); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetLoginOn("antigravity", users[3], false); err != nil {
		t.Fatal(err)
	}
	auths := map[string]string{}
	for _, l := range readLogins() {
		auths[l.User] = string(l.Auth)
	}
	order := []string{users[2], users[1], users[0], users[3]}
	for _, mode := range []string{"", Ordered, Rotate, LeastUsed} {
		if err := SetRouting("antigravity", mode); err != nil {
			t.Fatal(err)
		}
		if err := SetAccountOrder("antigravity", order); err != nil {
			t.Fatal(err)
		}
		p, err := Find("antigravity")
		if err != nil {
			t.Fatal(err)
		}
		shown, first := loginUsers(Logins("antigravity"))
		if !reflect.DeepEqual(shown, order) || first != order[0] || p.Routing != mode || !reflect.DeepEqual(loginRoutingUsers(p), order[:3]) {
			t.Fatalf("display=%v first=%s routing=%v mode=%s", shown, first, loginRoutingUsers(p), mode)
		}
		for _, l := range readLogins() {
			if string(l.Auth) != auths[l.User] {
				t.Fatal("changed credentials")
			}
			if l.User == users[3] && l.On {
				t.Fatal("enabled an off account")
			}
		}
		before, _ := os.ReadFile(loginsPath())
		if err := SetAccountOrder("antigravity", []string{users[3], users[2], users[1], users[0]}); err == nil {
			t.Fatal("silently enabled an off account")
		}
		after, _ := os.ReadFile(loginsPath())
		if !bytes.Equal(before, after) {
			t.Fatal("rejected drag changed logins")
		}
		if err := SwitchLogin("antigravity", users[1]); err != nil {
			t.Fatal(err)
		}
		p, _ = Find("antigravity")
		shown, first = loginUsers(Logins("antigravity"))
		if shown[0] != users[1] || first != users[1] || !reflect.DeepEqual(loginRoutingUsers(p), []string{users[1], users[2], users[0]}) {
			t.Fatal("Make first did not update both orders")
		}
	}
}

func TestAccountOrderCodexSurvivesRememberAndSwitch(t *testing.T) {
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "second@example.test", "r-second")
	rememberLogins(true)
	codexSignIn(t, home, "third@example.test", "r-third")
	rememberLogins(true)
	for _, user := range []string{"me@example.com", "second@example.test"} {
		if err := SetLoginOn("codex", user, true); err != nil {
			t.Fatal(err)
		}
	}
	order := []string{"second@example.test", "third@example.test", "me@example.com"}
	if err := SetAccountOrder("codex", order); err != nil {
		t.Fatal(err)
	}
	rememberLogins(true) // must retain ranks when it replaces the credential snapshot
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loginRoutingUsers(p), order) {
		t.Fatalf("routing %v", loginRoutingUsers(p))
	}
	shown, active := loginUsers(Logins("codex"))
	if !reflect.DeepEqual(shown, order) || active != order[0] {
		t.Fatalf("display %v active=%s", shown, active)
	}
	if err := SwitchLogin("codex", "me@example.com"); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("codex")
	if !reflect.DeepEqual(loginRoutingUsers(p), []string{"me@example.com", order[0], order[1]}) {
		t.Fatal("Make first lost the remaining order")
	}
	// Make a subsequent switch fail in this isolated home. Only ordering metadata
	// is rolled back, not a stale snapshot of anyone's refresh tokens.
	before := readLogins()
	if err := os.Remove(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, ".codex", "auth.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := arrangeLogins("codex", []string{order[1], order[0], "me@example.com"}); err == nil {
		t.Fatal("expected failed switch")
	}
	after := readLogins()
	for i := range before {
		if before[i].User != after[i].User || before[i].Order != after[i].Order || !bytes.Equal(before[i].Auth, after[i].Auth) {
			t.Fatal("failed switch did not restore order safely")
		}
	}
}

func TestAccountOrderRankSurvivesDedupe(t *testing.T) {
	old := savedLogin{Agent: "codex", User: "same@example.test", Order: 3, Seen: time.Unix(1, 0)}
	fresh := savedLogin{Agent: "codex", User: old.User, Seen: time.Unix(2, 0)}
	for _, ls := range [][]savedLogin{{old, fresh}, {fresh, old}} {
		got := dedupeLogins(ls)
		if len(got) != 1 || got[0].Order != 3 || !got[0].Seen.Equal(fresh.Seen) {
			t.Fatalf("lost order or newest credentials: %+v", got)
		}
	}
}
