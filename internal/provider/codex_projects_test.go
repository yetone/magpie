package provider

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// codexTestAuth is an auth.json signed in to ChatGPT user u in account a.
func codexTestAuth(u, a string) []byte {
	b, _ := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token": fakeJWT(map[string]any{
				"email":                       u + "@example.com",
				"https://api.openai.com/auth": map[string]any{"chatgpt_user_id": u, "chatgpt_account_id": a},
			}),
			"account_id": a,
		},
	})
	return b
}

// codexAppSim is the Codex app's renderer state as its GlobalState keeps
// it (ChatGPT.app 26.1103, early-bootstrap-133's selectRendererOwner): the
// file read at launch, an owner switched in memory, the file written.
type codexAppSim struct{ state map[string]any }

// codexAccountScoped is the app's own list (nh0) of account-scoped
// globals that a switch parks and loads; the sidebar's are among them.
var codexAccountScoped = append(slices.Clone(codexProjectKeys), "remote-projects", "selected-remote-host-id", "queued-follow-ups")

func codexAppLaunch(t *testing.T, path string) *codexAppSim {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := &codexAppSim{}
	if err := json.Unmarshal(b, &s.state); err != nil {
		t.Fatal(err)
	}
	s.selectOwner(nil) // its constructor, before the account is known
	return s
}

func (s *codexAppSim) selectOwner(r *string) {
	rs, _ := s.state[codexRendererKey].(map[string]any)
	if rs == nil {
		return
	}
	cur, _ := rs["owner"].(string)
	if r != nil && rs["owner"] != nil && cur == *r {
		return
	}
	inactive := map[string]any{}
	for k, v := range rs["inactive"].(map[string]any) {
		inactive[k] = v
	}
	globals := map[string]any{}
	for _, k := range codexAccountScoped {
		if v, ok := s.state[k]; ok {
			globals[k] = v
		}
	}
	if rs["owner"] != nil {
		inactive[cur] = map[string]any{"atoms": map[string]any{}, "globals": globals}
	}
	var o map[string]any
	if r != nil {
		o, _ = inactive[*r].(map[string]any)
		delete(inactive, *r)
	}
	for _, k := range codexAccountScoped {
		g, _ := o["globals"].(map[string]any)
		if v, ok := g[k]; o != nil && ok {
			s.state[k] = v
		} else {
			delete(s.state, k)
		}
	}
	var owner any
	if r != nil {
		owner = *r
	}
	s.state[codexRendererKey] = map[string]any{"owner": owner, "inactive": inactive}
}

func (s *codexAppSim) signIn(auth []byte) {
	h := codexRendererOwner(auth)
	if h == "" {
		s.selectOwner(nil)
		return
	}
	s.selectOwner(&h)
}

func (s *codexAppSim) projects() []string {
	lp, _ := s.state["local-projects"].(map[string]any)
	return slices.Sorted(func(yield func(string) bool) {
		for k := range lp {
			if !yield(k) {
				return
			}
		}
	})
}

func (s *codexAppSim) save(t *testing.T, path string) {
	t.Helper()
	b, _ := json.Marshal(s.state)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// codexStateFixture is a state file as the app leaves it signed in to a,
// with a's projects at its top level: a folder of the user's own, and a
// mirror of a's ChatGPT project, and b parked with no projects (its
// snapshot from an earlier switch, as in #5702).
func codexStateFixture(t *testing.T, home string, a []byte, b []byte) string {
	t.Helper()
	ida := codexIdentityOf(a)
	backing := map[string]any{"accountId": ida.account, "userId": ida.user, "projectId": "g-p-1"}
	state := map[string]any{
		"electron-main-window-bounds": map[string]any{"x": 10, "y": 20},
		"local-projects": map[string]any{
			"p-site": map[string]any{"id": "p-site", "name": "site", "rootPaths": []string{"/Users/me/site"},
				"retiredChatGptBacking": backing, "createdAt": 1, "updatedAt": 2},
			"g-p-1": map[string]any{"id": "g-p-1", "name": "a's ChatGPT project", "rootPaths": []string{},
				"chatGptBacking": backing, "createdAt": 1, "updatedAt": 2},
		},
		"project-order":              []string{"p-site", "g-p-1"},
		"thread-project-assignments": map[string]any{"t1": "p-site", "t2": "g-p-1"},
		"remote-projects":            []any{map[string]any{"id": "r1"}},
		codexRendererKey: map[string]any{
			"owner": codexRendererOwner(a),
			"inactive": map[string]any{
				codexRendererOwner(b): map[string]any{"atoms": map[string]any{"x": 1}, "globals": map[string]any{"pinned-thread-ids": []string{"t9"}}},
			},
		},
	}
	path := codexGlobalStatePath(home)
	bs, _ := json.Marshal(state)
	if err := os.WriteFile(path, bs, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Michael25651150 on X: "切换账号 codex 添加的项目丢了 发生过好几次了". The
// Codex app keeps projects per ChatGPT identity, so magpie's switch to
// another account showed that account's empty list. With the app closed,
// magpie gives the accounts with none the projects: the app, launched and
// switched as magpie switches it, shows them on every account.
func TestCodexProjectsFollowASwitch(t *testing.T) {
	home := t.TempDir()
	rec := filepath.Join(t.TempDir(), "codex-projects.json")
	a, b, c := codexTestAuth("user-a", "acct-a"), codexTestAuth("user-b", "acct-b"), codexTestAuth("user-c", "acct-c")
	path := codexStateFixture(t, home, a, b)

	n, err := shareCodexProjects(home, [][]byte{a, b, c}, rec)
	if err != nil || n != 2 {
		t.Fatalf("carried to %d (%v), want b and c", n, err)
	}
	for _, to := range [][]byte{b, c} {
		app := codexAppLaunch(t, path)
		app.signIn(a)
		if got := app.projects(); !slices.Equal(got, []string{"g-p-1", "p-site"}) {
			t.Fatalf("a's own projects: %v", got)
		}
		app.signIn(to) // magpie's switch: auth.json, the app-server's restart
		if got := app.projects(); !slices.Equal(got, []string{"p-site"}) {
			t.Fatalf("after the switch the app shows %v, want the user's p-site", got)
		}
		if got := app.state["project-order"]; !slices.Equal(anyStrings(got), []string{"p-site"}) {
			t.Fatalf("project-order %v: a's ChatGPT project went along", got)
		}
		if got := app.state["thread-project-assignments"].(map[string]any); len(got) != 1 || got["t1"] != "p-site" {
			t.Fatalf("thread-project-assignments %v", got)
		}
		p := app.state["local-projects"].(map[string]any)["p-site"].(map[string]any)
		if _, ok := p["retiredChatGptBacking"]; ok {
			t.Fatal("a's backing went to another account: the app refuses it (\"belongs to a different ChatGPT account\")")
		}
		if _, ok := app.state["remote-projects"]; ok {
			t.Fatal("a's remote projects went along")
		}
	}
	// b's own things stay
	app := codexAppLaunch(t, path)
	app.signIn(b)
	if got := anyStrings(app.state["pinned-thread-ids"]); !slices.Equal(got, []string{"t9"}) {
		t.Fatalf("b's pins %v", got)
	}
	// what magpie doesn't know is kept, and the file as it was beside it
	var top map[string]any
	raw, _ := os.ReadFile(path)
	_ = json.Unmarshal(raw, &top)
	if top["electron-main-window-bounds"] == nil {
		t.Fatal("an unrelated key was lost")
	}
	if _, err := os.Stat(path + ".magpie-before-projects"); err != nil {
		t.Fatalf("no backup: %v", err)
	}
	// once more: nothing to do
	if n, err := shareCodexProjects(home, [][]byte{a, b, c}, rec); n != 0 || err != nil {
		t.Fatalf("carried again to %d (%v)", n, err)
	}
}

// add is the user adding a folder as a project in the app's sidebar.
func (s *codexAppSim) add(id, root string) {
	lp, _ := s.state["local-projects"].(map[string]any)
	if lp == nil {
		lp = map[string]any{}
	}
	lp[id] = map[string]any{"id": id, "name": filepath.Base(root), "rootPaths": []any{root}, "createdAt": 3, "updatedAt": 3}
	s.state["local-projects"] = lp
	po, _ := s.state["project-order"].([]any)
	s.state["project-order"] = append(po, id)
}

// remove is the user removing a project from the sidebar.
func (s *codexAppSim) remove(id string) {
	lp, _ := s.state["local-projects"].(map[string]any)
	delete(lp, id)
	var po []any
	for _, e := range anyStrings(s.state["project-order"]) {
		if e != id {
			po = append(po, e)
		}
	}
	s.state["project-order"] = po
}

func shareOnce(t *testing.T, home, rec string, want int, auths ...[]byte) {
	t.Helper()
	if n, err := shareCodexProjects(home, auths, rec); n != want || err != nil {
		t.Fatalf("shared with %d (%v), want %d", n, err, want)
	}
}

func shows(t *testing.T, path string, who []byte, want ...string) *codexAppSim {
	t.Helper()
	app := codexAppLaunch(t, path)
	app.signIn(who)
	if got := app.projects(); !slices.Equal(got, want) {
		t.Fatalf("%s's account shows %v, want %v", codexIdentityOf(who).user, got, want)
	}
	return app
}

// Michael25651150 on X: projects are kept however accounts are switched.
// Every account shows every account's projects, even one that has a list
// of its own, and a folder added on two accounts is one project, its
// threads with it.
func TestCodexProjectsShared(t *testing.T) {
	home := t.TempDir()
	rec := filepath.Join(t.TempDir(), "codex-projects.json")
	a, b, c := codexTestAuth("user-a", "acct-a"), codexTestAuth("user-b", "acct-b"), codexTestAuth("user-c", "acct-c")
	path := codexStateFixture(t, home, a, b)
	app := codexAppLaunch(t, path)
	app.signIn(a)
	app.signIn(b) // a switch with the app open: b's own list
	app.add("p-b", "/Users/me/b")
	app.add("p-dup", "/Users/me/site/") // a's p-site, added again on b
	app.state["thread-project-assignments"] = map[string]any{"t5": "p-b"}
	app.save(t, path) // the app quit

	shareOnce(t, home, rec, 2, a, b, c) // a and c; b has them all
	app = shows(t, path, a, "g-p-1", "p-b", "p-site")
	if got := app.state["thread-project-assignments"].(map[string]any); got["t5"] != "p-b" || got["t1"] != "p-site" {
		t.Fatalf("a's thread-project-assignments %v", got)
	}
	shows(t, path, b, "p-b", "p-dup")
	app = shows(t, path, c, "p-b", "p-dup")
	if got := app.state["thread-project-assignments"].(map[string]any); got["t1"] != "p-dup" || got["t5"] != "p-b" || len(got) != 2 {
		t.Fatalf("c's thread-project-assignments %v: a's thread in site, under c's id for it", got)
	}
	// once more: nothing to do, nothing written
	before, _ := os.ReadFile(path)
	shareOnce(t, home, rec, 0, a, b, c)
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("the file was written with nothing to share")
	}
}

// A project the user removes on one account is removed from every
// account, not given back from another, and comes back only when the
// user adds it again.
func TestCodexProjectsRemovalRemembered(t *testing.T) {
	home := t.TempDir()
	rec := filepath.Join(t.TempDir(), "codex-projects.json")
	a, b, c := codexTestAuth("user-a", "acct-a"), codexTestAuth("user-b", "acct-b"), codexTestAuth("user-c", "acct-c")
	path := codexStateFixture(t, home, a, b)
	shareOnce(t, home, rec, 2, a, b, c)

	app := codexAppLaunch(t, path)
	app.signIn(a)
	app.add("p-new", "/Users/me/new") // added on a
	app.signIn(b)
	if got := app.projects(); !slices.Equal(got, []string{"p-site"}) {
		t.Fatalf("b shows %v", got)
	}
	app.remove("p-site") // removed on b
	app.save(t, path)

	shareOnce(t, home, rec, 3, a, b, c)
	for range 2 {
		app = shows(t, path, a, "g-p-1", "p-new")
		if got := anyStrings(app.state["project-order"]); slices.Contains(got, "p-site") {
			t.Fatalf("a's project-order %v", got)
		}
		if got := app.state["thread-project-assignments"].(map[string]any); got["t1"] != nil {
			t.Fatalf("a's thread-project-assignments %v", got)
		}
		shows(t, path, b, "p-new")
		shows(t, path, c, "p-new")
		shareOnce(t, home, rec, 0, a, b, c) // and it stays gone
	}

	// added again on c, under a new id: back on every account
	app = codexAppLaunch(t, path)
	app.signIn(c)
	app.add("p-site2", "/Users/me/site")
	app.save(t, path)
	shareOnce(t, home, rec, 2, a, b, c)
	shows(t, path, a, "g-p-1", "p-new", "p-site2")
	shows(t, path, b, "p-new", "p-site2")
}

// What was removed is unknown when magpie's record can't be read: the
// file isn't written then.
func TestCodexProjectsRecordUnreadable(t *testing.T) {
	home := t.TempDir()
	rec := filepath.Join(t.TempDir(), "codex-projects.json")
	if err := os.WriteFile(rec, []byte(`{"identities":`), 0o600); err != nil {
		t.Fatal(err)
	}
	a, b := codexTestAuth("user-a", "acct-a"), codexTestAuth("user-b", "acct-b")
	path := codexStateFixture(t, home, a, b)
	before, _ := os.ReadFile(path)
	if n, err := shareCodexProjects(home, [][]byte{a, b}, rec); n != 0 || err == nil {
		t.Fatalf("shared with %d (%v)", n, err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("written without knowing what was removed")
	}
}

// A file magpie can't read as the app's, or one from an app before
// per-identity projects, is never written.
func TestCodexProjectsUnknownFileLeft(t *testing.T) {
	a, b := codexTestAuth("user-a", "acct-a"), codexTestAuth("user-b", "acct-b")
	for name, body := range map[string]string{
		"old app":   `{"local-projects":{"p":{"id":"p","name":"p","rootPaths":[],"createdAt":1,"updatedAt":1}}}`,
		"broken":    `{"local-projects":`,
		"odd shape": `{"electron-account-renderer-state-v1":{"owner":1,"inactive":[]}}`,
	} {
		home := t.TempDir()
		rec := filepath.Join(t.TempDir(), "codex-projects.json")
		path := codexGlobalStatePath(home)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		n, _ := shareCodexProjects(home, [][]byte{a, b}, rec)
		if got, _ := os.ReadFile(path); n != 0 || string(got) != body {
			t.Fatalf("%s: carried to %d, file %s", name, n, got)
		}
		if _, err := os.Stat(path + ".magpie-before-projects"); err == nil {
			t.Fatalf("%s: a backup written", name)
		}
	}
	if n, err := shareCodexProjects(t.TempDir(), [][]byte{a}, filepath.Join(t.TempDir(), "rec.json")); n != 0 || err != nil {
		t.Fatalf("no file: %d %v", n, err)
	}
}

// The owner's hash is the app's: sha256 of JSON.stringify([userId,
// accountId]) (early-bootstrap-815's eaD).
func TestCodexRendererOwner(t *testing.T) {
	// node -e 'console.log(require("crypto").createHash("sha256").update(JSON.stringify(["user-a","acct-a"])).digest("hex"))'
	const want = "3a83b2a14d3700e107eeba3a6b5fb3a80f6b16c3c8de079ee80f8cf8b4bd15ea"
	if got := codexRendererOwner(codexTestAuth("user-a", "acct-a")); got != want {
		t.Fatalf("owner %q, want %q", got, want)
	}
	if codexRendererOwner([]byte(`{"auth_mode":"apikey"}`)) != "" {
		t.Fatal("an API key has an identity")
	}
}

// The look every few seconds carries only while the app is closed: what
// the open app holds it writes over the file.
func TestCodexProjectsStepWaitsForTheAppToClose(t *testing.T) {
	signIn(t)
	a := codexTestAuth("user-a", "acct-a")
	b := codexTestAuth("user-b", "acct-b")
	if err := os.WriteFile(codexAuthPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	path := codexStateFixture(t, codexHome(), a, b)
	oldOpen, oldEvery := CodexAppOpen, codexProjectsEvery
	t.Cleanup(func() {
		CodexAppOpen, codexProjectsEvery = oldOpen, oldEvery
		codexProjects.Lock()
		codexProjects.last, codexProjects.tried = "", time.Time{}
		codexProjects.Unlock()
	})
	codexProjectsEvery = 0
	open := true
	CodexAppOpen = func(context.Context) (bool, bool) { return open, true }
	before, _ := os.ReadFile(path)
	codexProjectsStep(context.Background())
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("written while the app is open")
	}
	open = false
	codexProjectsStep(context.Background())
	app := codexAppLaunch(t, path)
	app.signIn(b)
	if got := app.projects(); !slices.Equal(got, []string{"p-site"}) {
		t.Fatalf("b, signed in now, shows %v once the app closed", got)
	}
	if _, err := os.Stat(codexProjectsRecordPath()); err != nil {
		t.Fatalf("no record of what each account has: %v", err)
	}
}

func anyStrings(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, e := range l {
		s, _ := e.(string)
		out = append(out, s)
	}
	return out
}
