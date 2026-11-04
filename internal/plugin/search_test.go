package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A plugin's search hook (testdata/search): the host lists the APIs with an
// id magpie can use and an execute, leaves out the rest, and notes them for
// the provider package, which reads the note without starting the host and
// only for plugins that are on. A search is run with the user's key and
// address, its results read from a list or {results}, a throw with a
// status is a refused key, and one that never answers is ended.
func TestPluginSearch(t *testing.T) {
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/search")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	loaded, err := Plugins(ctx)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("%v %v", loaded, err)
	}
	var ids []string
	for _, d := range loaded[0].Searches {
		ids = append(ids, d.ID)
	}
	if strings.Join(ids, " ") != "fake own" {
		t.Fatalf("searches = %+v", loaded[0].Searches)
	}
	if d := loaded[0].Searches[0]; d.Name != "Fake Search" || d.KeysURL != "https://example.com/keys" || d.KeyOptional || d.NeedURL {
		t.Errorf("fake = %+v", d)
	}
	if d := loaded[0].Searches[1]; !d.NeedURL {
		t.Errorf("own = %+v", d)
	}

	ns, err := NotedSearches()
	if err != nil || len(ns) != 2 || ns[0].VendorID() != ShortName(Name(abs))+":fake" || ns[0].Spec != abs {
		t.Fatalf("noted = %+v, %v", ns, err)
	}

	pages, err := RunSearch(ctx, abs, "fake", "go", 6, "k1", "http://x")
	if err != nil || len(pages) != 2 || pages[0].Title != "T go" || pages[0].URL != "https://example.com/6" ||
		pages[0].Text != "key k1 url http://x" || pages[0].Age != "2026" {
		t.Errorf("pages = %+v, %v", pages, err)
	}
	_, err = RunSearch(ctx, abs, "fake", "go", 6, "spent", "")
	var r SearchRefused
	if !errors.As(err, &r) || r.Status != 429 || r.RetryAfter != "30" || !strings.Contains(r.Msg, "quota spent") {
		t.Errorf("a refused key: %#v", err)
	}
	if _, err := RunSearch(ctx, abs, "fake", "go", 6, "boom", ""); err == nil || !strings.Contains(err.Error(), "it broke") || errors.As(err, &r) {
		t.Errorf("a failure: %v", err)
	}
	if _, err := RunSearch(ctx, abs, "fake", "go", 6, "shape", ""); err == nil || !strings.Contains(err.Error(), "neither a list") {
		t.Errorf("a wrong shape: %v", err)
	}
	if _, err := RunSearch(ctx, abs, "nothing", "go", 6, "", ""); err == nil || !strings.Contains(err.Error(), "no search API nothing") {
		t.Errorf("an unknown API: %v", err)
	}
	short, stop := context.WithTimeout(ctx, 400*time.Millisecond)
	t0 := time.Now()
	_, err = RunSearch(short, abs, "fake", "go", 6, "hang", "")
	stop()
	// the host ends it at 400ms; Go's own backstop 5s later
	if err == nil || time.Since(t0) > 4*time.Second {
		t.Errorf("a search that never answers: %v after %s", err, time.Since(t0))
	}
	if _, err := RunSearch(ctx, abs, "fake", "go", 6, "k1", ""); err != nil {
		t.Errorf("the host after a search ran out of time: %v", err)
	}

	// off: not listed
	if err := SetOff(abs, true); err != nil {
		t.Fatal(err)
	}
	if ns, err := NotedSearches(); err != nil || len(ns) != 0 {
		t.Errorf("with the plugin off: %+v, %v", ns, err)
	}
	// a note that can't be read is an error, never "none, so remove"
	if err := os.WriteFile(searchesPath(), []byte("{half"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NotedSearches(); err == nil {
		t.Error("a corrupt note counted as no searches")
	}
}

// A search entry with auth (testdata/searchauth) signs in as the plugin's
// own provider: it is listed with that provider and no key, an id the
// plugin doesn't sign in to leaves the entry a key's, and one without auth
// is as before (a key, no getAuth). A search runs as the one account Go
// names, ctx.getAuth() giving that account alone, renewed first when due;
// the renewal's wait counts within the search's time; and an account that
// isn't signed in, or is another provider's, is an error, never the
// provider's first account in its place.
func TestPluginSearchSignsIn(t *testing.T) {
	sandbox(t)
	hold := filepath.Join(t.TempDir(), "hold")
	t.Setenv("SEARCHAUTH_HOLD", hold)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/searchauth")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	loaded, err := Plugins(ctx)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("%v %v", loaded, err)
	}
	got := map[string]SearchDef{}
	for _, d := range loaded[0].Searches {
		got[d.ID] = d
	}
	if got["seek"].Auth != "seekco" || got["stray"].Auth != "" || got["keyed"].Auth != "" || len(got) != 3 {
		t.Fatalf("searches = %+v", loaded[0].Searches)
	}
	if ns, err := NotedSearches(); err != nil || len(ns) != 3 || ns[0].Auth != "seekco" {
		t.Fatalf("noted = %+v, %v", ns, err)
	}

	later := time.Now().Add(time.Hour).UnixMilli()
	soon := time.Now().Add(time.Minute).UnixMilli()
	account := func(who, access, refresh string, expires int64) string {
		t.Helper()
		k, err := Import(ctx, "seekco", map[string]any{"type": "oauth", "access": access, "refresh": refresh, "expires": expires, "accountId": who})
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	a := account("a@seek", "acc-a", "r-a", later)
	b := account("b@seek", "acc-b", "r-b", later)
	due := account("due@seek", "acc-due", "r-due", soon)
	slow := account("slow@seek", "acc-slow", "r-slow", soon)
	if a == b {
		t.Fatalf("two accounts at one key %q", a)
	}

	title := func(account string) string {
		t.Helper()
		pages, err := RunSearchAs(ctx, abs, "seek", "go", 5, account)
		if err != nil || len(pages) != 1 {
			t.Fatalf("as %s: %+v, %v", account, pages, err)
		}
		if pages[0].Text != "key= fields=access,accountId,expires,refresh,type" {
			t.Errorf("as %s the hook saw %q", account, pages[0].Text)
		}
		return pages[0].Title
	}
	if s := title(b); s != "b@seek acc-b" {
		t.Errorf("as the second account: %q", s)
	}
	if s := title(a); s != "a@seek acc-a" {
		t.Errorf("as the first account: %q", s)
	}
	// a token the hook saves (client.auth.set) goes to the account it
	// searched as, never the provider's first (inScope)
	if _, err := RunSearchAs(ctx, abs, "seek", "rotate", 5, b); err != nil {
		t.Fatal(err)
	}
	if got := Auths("seekco"); got[b]["access"] != "rotated-b@seek" || got[a]["access"] != "acc-a" {
		t.Errorf("a token saved while searching as b: a %v, b %v", got[a]["access"], got[b]["access"])
	}
	// due: renewed before the search, which sees the new sign-in
	if s := title(due); s != "due@seek renewed-r-due" {
		t.Errorf("a sign-in due for renewal searched as %q", s)
	}

	for _, bad := range []string{"seekco#nobody", "elsewhere", "elsewhere#x"} {
		if _, err := RunSearchAs(ctx, abs, "seek", "go", 5, bad); err == nil || !strings.Contains(err.Error(), "not signed in") {
			t.Errorf("as %s: %v", bad, err)
		}
	}
	if _, err := RunSearchAs(ctx, abs, "seek", "go", 5, ""); err == nil {
		t.Error("a search that signs in ran with no account")
	}

	// the renewal (3s) counts within the search's time (1s)
	short, stop := context.WithTimeout(ctx, time.Second)
	t0 := time.Now()
	_, err = RunSearchAs(short, abs, "seek", "go", 5, slow)
	stop()
	if err == nil || time.Since(t0) > 2500*time.Millisecond {
		t.Errorf("a renewal past the search's time: %v after %s", err, time.Since(t0))
	}

	// a refused search is a SearchRefused, as a key's
	spent := account("spent@seek", "spent", "r-spent", later)
	var r SearchRefused
	if _, err := RunSearchAs(ctx, abs, "seek", "go", 5, spent); !errors.As(err, &r) || r.Status != 429 || r.RetryAfter != "60" {
		t.Errorf("a refused search: %#v", err)
	}

	// signed out while its renewal runs: the search doesn't go out as it,
	// and the renewal doesn't sign it in again (renew's own guard, cd83c3147)
	gone := account("gone@seek", "acc-gone", "r-hold", soon)
	ran := make(chan error, 1)
	go func() {
		_, err := RunSearchAs(ctx, abs, "seek", "go", 5, gone)
		ran <- err
	}()
	for {
		if _, err := os.Stat(hold + ".in"); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("the renewal never began")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := SignOut(ctx, "seekco", gone); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hold, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-ran; err == nil || !strings.Contains(err.Error(), "not signed in") {
		t.Errorf("a search as an account signed out mid-renewal: %v", err)
	}
	if _, ok := Auths("seekco")[gone]; ok {
		t.Error("the renewal signed the account in again")
	}

	// entries without auth: a key, as before, and no getAuth
	for _, id := range []string{"keyed", "stray"} {
		pages, err := RunSearch(ctx, abs, id, "go", 5, "k9", "")
		if err != nil || len(pages) != 1 || pages[0].Title != "k9 undefined" {
			t.Errorf("%s: %+v, %v", id, pages, err)
		}
	}
}
