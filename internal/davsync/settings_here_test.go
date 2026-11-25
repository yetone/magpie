package davsync

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// picksOf is each provider's picked models here, id=a,b sorted.
func picksOf(t *testing.T) []string {
	t.Helper()
	ps, err := provider.Stored()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range ps {
		out = append(out, p.ID+"="+strings.Join(p.Models, ",")+"/"+p.Key)
	}
	slices.Sort(out)
	return out
}

// pick sets provider id's picked models here, as its editor does.
func pick(t *testing.T, id string, models ...string) {
	t.Helper()
	ps, err := provider.Stored()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.ID == id {
			p.Models = models
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("no %s here", id)
}

// With ProviderSettingsHere a computer's providers sync and their model
// picks, among their settings, stay its own (StudyingPython on X, 0.1.4787
// on S3: synced providers, each computer keeping its own model
// selection): a key changed on one comes
// in on the other with that one's picks left as they are, a pick made on
// either goes nowhere, and a provider new on one comes in with the
// picks it was made with. Off on the other computer, that one's picks
// sync as ever, among the computers that let them.
func TestModelPicksStayOnEachComputer(t *testing.T) {
	f, srv := newFakeS3(t)
	ctx := context.Background()
	a, b := newComputer(t), newComputer(t)
	sync := func(c computer) {
		t.Helper()
		c.use(t)
		if err := Now(ctx); err != nil {
			t.Fatal(err)
		}
	}

	// a syncs first, its picks as ever
	a.use(t)
	cfgA := f.config(srv)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "k1", Models: []string{"gpt-5", "claude-sonnet-4-5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Configure(cfgA); err != nil {
		t.Fatal(err)
	}
	sync(a)

	// b joins with its picks kept here, having the same provider with
	// other picks: it takes a's key, and keeps its own picks
	b.use(t)
	cfgB := f.config(srv)
	cfgB.ProviderSettingsHere = true
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "old", Models: []string{"deepseek-v3"}}); err != nil {
		t.Fatal(err)
	}
	if err := Configure(cfgB); err != nil {
		t.Fatal(err)
	}
	sync(b)
	if got, want := picksOf(t), []string{"relay=deepseek-v3/k1"}; !slices.Equal(got, want) {
		t.Fatalf("b after joining: %v, want %v", got, want)
	}
	if v := Status(); !v.ProviderSettingsHere {
		t.Fatal("b's status doesn't say its picks stay on it")
	}

	// a changes its picks and its key: b takes the key only
	a.use(t)
	pick(t, "relay", "gpt-5")
	p, _ := provider.Stored()
	p[0].Key = "k2"
	if err := provider.Save(p[0]); err != nil {
		t.Fatal(err)
	}
	sync(a)
	sync(b)
	if got, want := picksOf(t), []string{"relay=deepseek-v3/k2"}; !slices.Equal(got, want) {
		t.Fatalf("b after a's new key and picks: %v, want %v", got, want)
	}

	// b changes its picks: nothing goes up, a's stay
	pick(t, "relay", "deepseek-v3", "kimi-k2")
	sync(b)
	sync(a)
	if got, want := picksOf(t), []string{"relay=gpt-5/k2"}; !slices.Equal(got, want) {
		t.Fatalf("a after b's picks: %v, want %v", got, want)
	}

	// b changes a name and its picks at once: the name goes up, a's picks
	// stay on the server and on a
	b.use(t)
	p, _ = provider.Stored()
	p[0].Name, p[0].Models = "Relay B", []string{"qwen3"}
	if err := provider.Save(p[0]); err != nil {
		t.Fatal(err)
	}
	sync(b)
	sync(a)
	if got, want := picksOf(t), []string{"relay=gpt-5/k2"}; !slices.Equal(got, want) {
		t.Fatalf("a after b's rename: %v, want %v", got, want)
	}
	if p, _ := provider.Stored(); p[0].Name != "Relay B" {
		t.Fatalf("a after b's rename is named %q", p[0].Name)
	}

	// a provider new on a comes to b with the picks it was made with
	if err := provider.Save(provider.Provider{ID: "other", Name: "Other", Chat: "https://other.example/v1", Key: "o1", Models: []string{"glm-4.6"}}); err != nil {
		t.Fatal(err)
	}
	sync(a)
	sync(b)
	if got, want := picksOf(t), []string{"other=glm-4.6/o1", "relay=qwen3/k2"}; !slices.Equal(got, want) {
		t.Fatalf("b after a's new provider: %v, want %v", got, want)
	}

	// and one new on b goes to a with b's picks
	if err := provider.Save(provider.Provider{ID: "third", Name: "Third", Chat: "https://third.example/v1", Key: "t1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	sync(b)
	sync(a)
	if got, want := picksOf(t), []string{"other=glm-4.6/o1", "relay=gpt-5/k2", "third=m1/t1"}; !slices.Equal(got, want) {
		t.Fatalf("a after b's new provider: %v, want %v", got, want)
	}

	// turning it off on b is no change on either side: b's picks stay
	// until b picks again, and then go up as any change made here
	b.use(t)
	cfgB.ProviderSettingsHere = false
	if err := Configure(cfgB); err != nil {
		t.Fatal(err)
	}
	sync(b)
	if got, want := picksOf(t), []string{"other=glm-4.6/o1", "relay=qwen3/k2", "third=m1/t1"}; !slices.Equal(got, want) {
		t.Fatalf("b after turning it off: %v, want %v", got, want)
	}
	sync(a)
	if got, want := picksOf(t), []string{"other=glm-4.6/o1", "relay=gpt-5/k2", "third=m1/t1"}; !slices.Equal(got, want) {
		t.Fatalf("a after b turned it off: %v, want %v", got, want)
	}
	b.use(t)
	pick(t, "relay", "qwen3", "kimi-k2")
	sync(b)
	sync(a)
	if got, want := picksOf(t), []string{"other=glm-4.6/o1", "relay=qwen3,kimi-k2/k2", "third=m1/t1"}; !slices.Equal(got, want) {
		t.Fatalf("a after b's pick with it off: %v, want %v", got, want)
	}
}

// Turning ProviderSettingsHere on is no change to send: the settings
// here, now kept here, are not taken for an edit.
func TestProviderSettingsHereTurnedOnSendsNothing(t *testing.T) {
	f, srv := newFakeS3(t)
	ctx := context.Background()
	a := newComputer(t)
	a.use(t)
	cfg := f.config(srv)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "k1", Models: []string{"gpt-5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	puts := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.put
	}
	before := puts()
	cfg.ProviderSettingsHere = true
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	if after := puts(); after != before {
		t.Fatalf("turning ProviderSettingsHere on wrote %d bytes to the server", after-before)
	}
	if v := Status(); v.Error != "" || !v.ProviderSettingsHere {
		t.Fatalf("status after: %+v", v)
	}
}

// settingsOf is each provider's settings and key here, sorted by id: the
// key, its URL and secret header, and the settings the test turns.
func settingsOf(t *testing.T) []string {
	t.Helper()
	ps, err := provider.Stored()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range ps {
		out = append(out, fmt.Sprintf("%s key=%s chat=%s auth=%q team=%q proxy=%s rpm=%d off=%t routing=%s",
			p.ID, p.Key, p.Chat, p.Headers["Authorization"], p.Headers["X-Team"], p.Proxy, p.MaxRPM, p.Off, p.Routing))
	}
	slices.Sort(out)
	return out
}

// With ProviderSettingsHere only the list and the keys come in and go up
// (StudyingPython on X: "只希望同步供应商的key和列表，不希望同步供应商设置"):
// a key with its URL and its secret header syncs, the provider's settings
// (a header of its own, the proxy, a limit, on or off, routing) stay on
// each computer, and a provider taken away on one goes on the other.
func TestProviderSettingsStayOnEachComputer(t *testing.T) {
	f, srv := newFakeS3(t)
	ctx := context.Background()
	a, b := newComputer(t), newComputer(t)
	sync := func(c computer) {
		t.Helper()
		c.use(t)
		if err := Now(ctx); err != nil {
			t.Fatal(err)
		}
	}
	edit := func(c computer, id string, change func(*provider.Provider)) {
		t.Helper()
		c.use(t)
		p, err := provider.Find(id)
		if err != nil {
			t.Fatal(err)
		}
		change(p)
		if err := provider.Save(*p); err != nil {
			t.Fatal(err)
		}
	}

	a.use(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "k1",
		Headers: map[string]string{"Authorization": "Bearer s1", "X-Team": "a"}, Proxy: "http://a.proxy:8080", MaxRPM: 10, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "gone", Name: "Gone", Chat: "https://gone.example/v1", Key: "g1"}); err != nil {
		t.Fatal(err)
	}
	if err := Configure(f.config(srv)); err != nil {
		t.Fatal(err)
	}
	sync(a)

	b.use(t)
	cfgB := f.config(srv)
	cfgB.ProviderSettingsHere = true
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "old",
		Headers: map[string]string{"X-Team": "b"}, Proxy: "socks5://b.proxy:1080", MaxRPM: 60, Off: true}); err != nil {
		t.Fatal(err)
	}
	if err := Configure(cfgB); err != nil {
		t.Fatal(err)
	}
	sync(b)
	wantB := []string{
		`gone key=g1 chat=https://gone.example/v1 auth="" team="" proxy= rpm=0 off=false routing=`,
		`relay key=k1 chat=https://relay.example/v1 auth="Bearer s1" team="b" proxy=socks5://b.proxy:1080 rpm=60 off=true routing=`,
	}
	if got := settingsOf(t); !slices.Equal(got, wantB) {
		t.Fatalf("b after joining:\n%v\nwant\n%v", strings.Join(got, "\n"), strings.Join(wantB, "\n"))
	}

	// a moves the key to a new URL with a new secret header, and turns its
	// settings: b takes the key, the URL and the header only
	edit(a, "relay", func(p *provider.Provider) {
		p.Key, p.Chat = "k2", "https://relay2.example/v1"
		p.Headers = map[string]string{"Authorization": "Bearer s2", "X-Team": "a2"}
		p.Proxy, p.MaxRPM, p.Routing = "", 5, "rotate"
	})
	if err := provider.Delete("gone"); err != nil {
		t.Fatal(err)
	}
	sync(a)
	sync(b)
	wantB = []string{`relay key=k2 chat=https://relay2.example/v1 auth="Bearer s2" team="b" proxy=socks5://b.proxy:1080 rpm=60 off=true routing=`}
	if got := settingsOf(t); !slices.Equal(got, wantB) {
		t.Fatalf("b after a's new key and settings:\n%v\nwant\n%v", strings.Join(got, "\n"), strings.Join(wantB, "\n"))
	}

	// b turns its settings: none of them reaches a
	edit(b, "relay", func(p *provider.Provider) {
		p.Proxy, p.MaxRPM, p.Off = "", 1, false
		p.Headers["X-Team"] = "b2"
	})
	sync(b)
	sync(a)
	wantA := []string{`relay key=k2 chat=https://relay2.example/v1 auth="Bearer s2" team="a2" proxy= rpm=5 off=false routing=rotate`}
	if got := settingsOf(t); !slices.Equal(got, wantA) {
		t.Fatalf("a after b's settings:\n%v\nwant\n%v", strings.Join(got, "\n"), strings.Join(wantA, "\n"))
	}

	// b's new key does
	edit(b, "relay", func(p *provider.Provider) { p.Key = "k3" })
	sync(b)
	sync(a)
	wantA = []string{`relay key=k3 chat=https://relay2.example/v1 auth="Bearer s2" team="a2" proxy= rpm=5 off=false routing=rotate`}
	if got := settingsOf(t); !slices.Equal(got, wantA) {
		t.Fatalf("a after b's key:\n%v\nwant\n%v", strings.Join(got, "\n"), strings.Join(wantA, "\n"))
	}
}
