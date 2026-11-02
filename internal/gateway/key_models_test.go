package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// A gateway key held to some models (#882) sees only those in the model
// lists, is refused any other before a provider is asked — by a bare
// name, through a group with one it may not use or as a fallback — while
// another key and magpie's own calls for it go on.
func TestGatewayKeyModelsHoldAKey(t *testing.T) {
	fresh(t)
	plan := &fake{t: t, ctype: "application/json", reply: `{"id":"from-plan","choices":[]}`}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	twoProviders(t, plan, spare)
	keys, secrets := newCaller(t, "Held", "Free")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{" plan/* ", "PLAN/*"}}); err != nil {
		t.Fatal(err)
	}
	if ks, _ := access.List(); !slices.Equal(ks[len(ks)-2].Models, []string{"plan/*"}) {
		t.Fatalf("stored %+v", ks[len(ks)-2])
	}
	for _, g := range []provider.Group{
		{Name: "Both", Members: []string{"plan/m1", "spare/m2"}, Routing: provider.Ordered},
		{Name: "Solo", Members: []string{"plan/m1"}, Routing: provider.Ordered},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	h := New().Handler()
	do := func(secret, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	listed := func(secret string) []string {
		var l struct{ Data []struct{ ID string } }
		w := do(secret, "GET", "/v1/models", "")
		if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
			t.Fatal(w.Body.String())
		}
		var ids []string
		for _, m := range l.Data {
			ids = append(ids, m.ID)
		}
		return ids
	}
	// Solo, of plan/m1 alone, is a group the key wasn't given (kautism)
	if ids := listed(secrets[0]); !slices.Contains(ids, "plan/m1") || slices.Contains(ids, "group/solo") || slices.Contains(ids, "spare/m2") || slices.Contains(ids, "group/both") {
		t.Fatalf("held key lists %v", ids)
	}
	if ids := listed(secrets[1]); !slices.Contains(ids, "spare/m2") || !slices.Contains(ids, "group/both") {
		t.Fatalf("free key lists %v", ids)
	}
	if w := do(secrets[0], "GET", "/v1beta/models", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "m1") || strings.Contains(w.Body.String(), "m2") {
		t.Fatalf("gemini list %d %s", w.Code, w.Body.String())
	}
	if w := do(secrets[0], "GET", "/v1/models/spare/m2", ""); w.Code == 200 {
		t.Fatal("held key found a model it may not use", w.Body.String())
	}

	chat := func(secret, model string) *httptest.ResponseRecorder {
		return do(secret, "POST", "/v1/chat/completions", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
	}
	for _, model := range []string{"spare/m2", "m2", "group/both", "group/solo"} {
		w := chat(secrets[0], model)
		var e struct {
			Error struct{ Message, Type string }
		}
		json.Unmarshal(w.Body.Bytes(), &e)
		if w.Code != 403 || e.Error.Type != "permission_error" || !strings.Contains(e.Error.Message, `"Held"`) || !strings.Contains(e.Error.Message, "plan/*") {
			t.Fatalf("%s: %d %s", model, w.Code, w.Body.String())
		}
		if rec := lastUsage(t); !rec.Rejected || rec.Status != 403 || rec.CallerKeyID != keys[0].ID {
			t.Fatalf("%s recorded as %+v", model, rec)
		}
	}
	if spare.calls != 0 || plan.calls != 0 {
		t.Fatal("a refused request reached a provider", plan.calls, spare.calls)
	}
	// said in Anthropic's shape on its API
	w := do(secrets[0], "POST", "/v1/messages", `{"model":"spare/m2","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	var a struct {
		Type  string
		Error struct{ Type string }
	}
	json.Unmarshal(w.Body.Bytes(), &a)
	if w.Code != 403 || a.Type != "error" || a.Error.Type != "permission_error" {
		t.Fatal("anthropic refusal", w.Code, w.Body.String())
	}
	for _, model := range []string{"plan/m1", "m1"} {
		if w := chat(secrets[0], model); w.Code != 200 || !strings.Contains(w.Body.String(), "from-plan") {
			t.Fatalf("%s: %d %s", model, w.Code, w.Body.String())
		}
	}
	if w := chat(secrets[1], "spare/m2"); w.Code != 200 || !strings.Contains(w.Body.String(), "from-spare") {
		t.Fatal("a free key was held", w.Code, w.Body.String())
	}
	// magpie's own call for the key (a search, a picture described) goes on
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"spare/m2","messages":[{"role":"user","content":"hi"}]}`))
	r = r.WithContext(magpieChose(r.Context()))
	r.Header.Set("Authorization", "Bearer "+secrets[0])
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Fatal("magpie's own call was held", rec.Code, rec.Body.String())
	}
	// plan out of quota: its fallback is a model the key may not use
	plan.code, plan.reply = 429, `{"error":{"message":"slow down"}}`
	spareCalls := spare.calls
	if w := chat(secrets[0], "plan/m1"); w.Code == 200 || spare.calls != spareCalls {
		t.Fatal("a held key fell back to a model it may not use", w.Code, w.Body.String())
	}
	if w := chat(secrets[1], "plan/m1"); w.Code != 200 || !strings.Contains(w.Body.String(), "from-spare") {
		t.Fatal("a free key's fallback", w.Code, w.Body.String())
	}
	// "all" takes it off
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID}); err != nil {
		t.Fatal(err)
	}
	plan.code, plan.reply = 0, `{"id":"from-plan","choices":[]}`
	if w := chat(secrets[0], "spare/m2"); w.Code != 200 {
		t.Fatal("a key let off is still held", w.Code, w.Body.String())
	}
}

// A provider's id inside the asked model can't pass for another
// provider's: OpenRouter's "anthropic/x" isn't Anthropic's.
func TestGatewayKeyModelsMatchTheServingProvider(t *testing.T) {
	who := access.Identity{KeyName: "k", Models: []string{"anthropic/*"}}
	if modelAllowed(who, provider.Provider{ID: "openrouter"}, "anthropic/claude") {
		t.Fatal("a model id passed for its provider")
	}
	if !modelAllowed(who, provider.Provider{ID: "anthropic"}, "claude") {
		t.Fatal("anthropic's own")
	}
	if !modelAllowed(access.Identity{}, provider.Provider{ID: "x"}, "y") {
		t.Fatal("a free key")
	}
	if membersAllowed(who, nil) || !membersAllowed(access.Identity{}, nil) {
		t.Fatal("an empty group")
	}
}

// A key held to a routing group it names, "group/<id>" (Magic_zero on
// Discord), is shown the group and may use it, every member of it going
// through it — the next one too when the first fails — but not the
// members asked by name, a group it doesn't name, or a fallback outside
// the group; "group/*" names every group, and a group the key doesn't
// name is not its for a group named inside it.
func TestGatewayKeyModelsNameAGroup(t *testing.T) {
	fresh(t)
	plan := &fake{t: t, ctype: "application/json", reply: `{"id":"from-plan","choices":[]}`}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	twoProviders(t, plan, spare)
	keys, secrets := newCaller(t, "Group", "Every group", "Inner")
	for i, ms := range [][]string{{"group/both"}, {"group/*"}, {"group/solo", "spare/m2"}} {
		if _, err := access.Update("models-key", access.Change{Key: keys[i].ID, Models: ms}); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []provider.Group{
		{Name: "Both", Members: []string{"plan/m1", "spare/m2"}, Routing: provider.Ordered},
		{Name: "Solo", Members: []string{"plan/m1"}, Routing: provider.Ordered},
		{Name: "Outer", Members: []string{"group/solo", "spare/m2"}, Routing: provider.Ordered},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	h := New().Handler()
	do := func(secret, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	listed := func(secret string) []string {
		var l struct{ Data []struct{ ID string } }
		w := do(secret, "GET", "/v1/models", "")
		if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
			t.Fatal(w.Body.String())
		}
		var ids []string
		for _, m := range l.Data {
			ids = append(ids, m.ID)
		}
		slices.Sort(ids)
		return ids
	}
	if ids := listed(secrets[0]); !slices.Equal(ids, []string{"group/both"}) {
		t.Fatalf("a key naming group/both lists %v", ids)
	}
	if ids := listed(secrets[1]); !slices.Equal(ids, []string{"group/both", "group/outer", "group/solo"}) {
		t.Fatalf("a key naming group/* lists %v", ids)
	}
	if ids := listed(secrets[2]); slices.Contains(ids, "group/outer") || !slices.Contains(ids, "group/solo") || slices.Contains(ids, "plan/m1") || slices.Contains(ids, "group/both") {
		t.Fatalf("a key naming group/solo and spare/m2 lists %v", ids)
	}
	chat := func(secret, model string) *httptest.ResponseRecorder {
		return do(secret, "POST", "/v1/chat/completions", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
	}
	for _, c := range []struct {
		key   int
		model string
		code  int
		from  string
	}{
		{0, "group/both", 200, "from-plan"},
		{0, "both", 200, "from-plan"},
		{0, "plan/m1", 403, ""},
		{0, "spare/m2", 403, ""},
		{0, "group/solo", 403, ""},
		{1, "group/solo", 200, "from-plan"},
		{1, "plan/m1", 403, ""},
		{2, "group/solo", 200, "from-plan"},
		{2, "group/outer", 403, ""},
		{2, "group/both", 403, ""},
	} {
		w := chat(secrets[c.key], c.model)
		if w.Code != c.code || c.from != "" && !strings.Contains(w.Body.String(), c.from) {
			t.Fatalf("key %d asking %s: %d %s", c.key, c.model, w.Code, w.Body.String())
		}
	}
	// the group's first member out of quota: the next member, through it
	plan.code, plan.reply = 429, `{"error":{"message":"slow down"}}`
	if w := chat(secrets[0], "group/both"); w.Code != 200 || !strings.Contains(w.Body.String(), "from-spare") {
		t.Fatal("a named group's next member", w.Code, w.Body.String())
	}
	// but plan's own fallback, spare/m2, isn't Solo's
	spareCalls := spare.calls
	if w := chat(secrets[1], "group/solo"); w.Code == 200 || spare.calls != spareCalls {
		t.Fatal("a named group fell back outside itself", w.Code, w.Body.String())
	}
}

// A gateway key held to some models (#882) counts no tokens on one it may
// not use — Anthropic's count_tokens and Gemini's :countTokens, a group
// judged by its members — and System One asks its decision model only of
// a key that may use it; a free key goes on as before, and no provider is
// asked for any of it.
func TestGatewayKeyModelsHoldCountTokensAndSystemOne(t *testing.T) {
	fresh(t)
	plan := &fake{t: t, ctype: "application/json", reply: `{"id":"from-plan","choices":[]}`}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	twoProviders(t, plan, spare)
	var decided int
	jev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decided++
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"answers":{"intent":{"choice":"bug"}},"usage":{"input_tokens":10,"output_tokens":2}}`)
	}))
	t.Cleanup(jev.Close)
	if err := provider.Save(provider.Provider{ID: "jev", Name: "Jev", Key: "k", Decide: jev.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "Both", Members: []string{"plan/m1", "spare/m2"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Held", "Free")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"plan/*"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	do := func(secret, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	// counting tokens on a model it may not use, by id, bare and through a
	// group with one in: said in Anthropic's own error shape
	for _, model := range []string{"spare/m2", "m2", "group/both"} {
		w := do(secrets[0], "POST", "/v1/messages/count_tokens", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
		var e struct {
			Error struct{ Message, Type string }
		}
		json.Unmarshal(w.Body.Bytes(), &e)
		if w.Code != 403 || e.Error.Type != "permission_error" || !strings.Contains(e.Error.Message, `"Held"`) || !strings.Contains(e.Error.Message, "plan/*") {
			t.Fatalf("counting %s: %d %s", model, w.Code, w.Body.String())
		}
	}
	// one it may use is counted, and a free key counts anything
	for _, c := range []struct{ secret, model string }{
		{secrets[0], "plan/m1"}, {secrets[1], "spare/m2"},
	} {
		if w := do(c.secret, "POST", "/v1/messages/count_tokens", `{"model":"`+c.model+`","messages":[{"role":"user","content":"hi"}]}`); w.Code != 200 || !strings.Contains(w.Body.String(), "input_tokens") {
			t.Fatalf("counting %s: %d %s", c.model, w.Code, w.Body.String())
		}
	}
	// Gemini's counting, the model in its URL, is held the same way
	w := do(secrets[0], "POST", "/v1beta/models/spare/m2:countTokens", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	var g struct {
		Error struct {
			Code   int
			Status string
		}
	}
	if w.Code != 403 || json.Unmarshal(w.Body.Bytes(), &g) != nil || g.Error.Code != 403 || g.Error.Status != "PERMISSION_DENIED" {
		t.Fatalf("gemini counting: %d %s", w.Code, w.Body.String())
	}
	if w = do(secrets[0], "POST", "/v1beta/models/plan/m1:countTokens", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`); w.Code != 200 || !strings.Contains(w.Body.String(), "totalTokens") {
		t.Fatalf("gemini counting allowed: %d %s", w.Code, w.Body.String())
	}
	if plan.calls != 0 || spare.calls != 0 || decided != 0 {
		t.Fatal("a held count or decision reached a provider", plan.calls, spare.calls, decided)
	}
	// System One's decision model is a model: the held key is refused it,
	// a free key asks, and one given the decider asks too
	const ask = `{"model":"jev/jev-latest","state":{"message":"hi"},"questions":{"intent":{"type":"choice"}}}`
	w = do(secrets[0], "POST", "/v1/systemone", ask)
	var e struct {
		Error struct{ Message, Type string }
	}
	json.Unmarshal(w.Body.Bytes(), &e)
	if w.Code != 403 || e.Error.Type != "permission_error" || !strings.Contains(e.Error.Message, `"Held"`) {
		t.Fatalf("system one held: %d %s", w.Code, w.Body.String())
	}
	if decided != 0 {
		t.Fatal("a held decision reached its provider", decided)
	}
	if w = do(secrets[1], "POST", "/v1/systemone", ask); w.Code != 200 || decided != 1 {
		t.Fatalf("a free key's decision: %d %s, decided %d", w.Code, w.Body.String(), decided)
	}
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"jev/*"}}); err != nil {
		t.Fatal(err)
	}
	if w = do(secrets[0], "POST", "/v1/systemone", ask); w.Code != 200 || decided != 2 {
		t.Fatalf("a key given the decider: %d %s, decided %d", w.Code, w.Body.String(), decided)
	}
}

// Another magpie added as a provider with a gateway key held to some
// models asks this one for its image and video models too
// (provider.DrawersHeader, VideomakersHeader). It is told of those the
// key may use alone, as it is of chat models; a key free of models, and
// this computer, are told of every one.
func TestGatewayKeyModelsHoldTheImageAndVideoLists(t *testing.T) {
	grokSignedIn(t)
	newGrokMedia(t)
	keys, secrets := newCaller(t, "Elsewhere", "Grok", "Free")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"elsewhere/*"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := access.Update("models-key", access.Change{Key: keys[1].ID, Models: []string{"grok/grok-imagine-video"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	listed := func(secret string) (image, video []string) {
		r := httptest.NewRequest("GET", "/v1/models", nil)
		if secret != "" {
			r.Header.Set("Authorization", "Bearer "+secret)
		}
		r.Header.Set(provider.DrawersHeader, "1")
		r.Header.Set(provider.VideomakersHeader, "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var l struct{ Data []struct{ ID, Kind string } }
		if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
			t.Fatal(w.Body.String())
		}
		for _, m := range l.Data {
			switch m.Kind {
			case "image":
				image = append(image, m.ID)
			case "video":
				video = append(video, m.ID)
			}
		}
		return image, video
	}
	if image, video := listed(""); !slices.Contains(image, "grok/grok-imagine-image") || !slices.Contains(video, "grok/grok-imagine-video") {
		t.Fatalf("this computer is told of image %v, video %v", image, video)
	}
	if image, video := listed(secrets[2]); !slices.Contains(image, "grok/grok-imagine-image") || !slices.Contains(video, "grok/grok-imagine-video") {
		t.Fatalf("a free key is told of image %v, video %v", image, video)
	}
	if image, video := listed(secrets[0]); len(image) != 0 || len(video) != 0 {
		t.Fatalf("a key held elsewhere is told of image %v, video %v", image, video)
	}
	if image, video := listed(secrets[1]); len(image) != 0 || !slices.Equal(video, []string{"grok/grok-imagine-video"}) {
		t.Fatalf("a key held to one video model is told of image %v, video %v", image, video)
	}
}

// A gateway key held to an account that serves only a chat model must not
// be told of that provider's image/video models in /v1/models (#2579): the
// chat catalog already applies accountServes; drawerObjects and
// videomakerObjects used to list them from modelAllowed alone.
func TestGatewayKeyAccountsHoldTheImageAndVideoLists(t *testing.T) {
	grokSignedIn(t)
	newGrokMedia(t)
	// the plugin's one account, which grokSignedIn signs in, held to a
	// chat model of its own
	p, err := provider.Find("grok")
	if err != nil || len(p.AccountRefs()) != 1 {
		t.Fatalf("Grok's accounts: %v, %v", p.AccountRefs(), err)
	}
	ref := p.AccountRefs()[0]
	keys, secrets := newCaller(t, "Held", "Free")
	if _, err := access.Update("accounts-key", access.Change{Key: keys[0].ID, Accounts: []string{"grok/" + ref}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAccountModels("grok", ref, []string{"fake-1"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = provider.SetAccountModels("grok", ref, nil)
	})
	h := New().Handler()
	listed := func(secret string) (image, video []string) {
		r := httptest.NewRequest("GET", "/v1/models", nil)
		if secret != "" {
			r.Header.Set("Authorization", "Bearer "+secret)
		}
		r.Header.Set(provider.DrawersHeader, "1")
		r.Header.Set(provider.VideomakersHeader, "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var l struct{ Data []struct{ ID, Kind string } }
		if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
			t.Fatal(w.Body.String())
		}
		for _, m := range l.Data {
			switch m.Kind {
			case "image":
				image = append(image, m.ID)
			case "video":
				video = append(video, m.ID)
			}
		}
		return image, video
	}
	if image, video := listed(""); !slices.Contains(image, "grok/grok-imagine-image") || !slices.Contains(video, "grok/grok-imagine-video") {
		t.Fatalf("this computer is told of image %v, video %v", image, video)
	}
	if image, video := listed(secrets[1]); !slices.Contains(image, "grok/grok-imagine-image") || !slices.Contains(video, "grok/grok-imagine-video") {
		t.Fatalf("a free key is told of image %v, video %v", image, video)
	}
	if image, video := listed(secrets[0]); len(image) != 0 || len(video) != 0 {
		t.Fatalf("a key whose account serves no image or video model is told of image %v, video %v", image, video)
	}
}

// A gateway key held to a routing group (Kautism on Discord: a key held to
// two groups, and Clef and Jev listed nowhere) has the group's own
// decision model asked for every turn, whatever the key's models: Jev
// picks the turn's rule. Asking Jev itself, at /v1/systemone or in
// another magpie's list of decision models, is a model of its own, which
// the key reaches only once it names it, as the key editor now lets it.
func TestGatewayKeyHeldToAGroupKeepsItsDecider(t *testing.T) {
	s, _, _, j := jevved(t, "", provider.Rule{Use: "b/big", Intent: "debugging"})
	j.choice, j.sure = "debugging", 0.9
	keys, secrets := newCaller(t, "Groups")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"group/r"}}); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	do := func(path, body string, header ...string) *httptest.ResponseRecorder {
		method := "POST"
		if body == "" {
			method = "GET"
		}
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secrets[0])
		for i := 0; i+1 < len(header); i += 2 {
			r.Header.Set(header[i], header[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := do("/v1/chat/completions", chat("why does this crash?", nil, 0, "")); w.Code != 200 || !strings.Contains(w.Body.String(), "from kb") {
		t.Fatalf("the group's turn: %d %s", w.Code, w.Body.String())
	}
	if r := lastRoute(s); r.Rule == nil || r.Rule.Classified.Intent != "debugging" || len(j.turns()) != 1 {
		t.Fatalf("the group's Jev wasn't asked for the held key: %+v, %d asks", r.Rule, len(j.turns()))
	}
	const ask = `{"model":"ts/jev-latest","state":{"message":"hi"},"questions":{"intent":{"type":"choice"}}}`
	listed := func() bool {
		w := do("/v1/models", "", provider.DecidersHeader, "1")
		return strings.Contains(w.Body.String(), `"ts/jev-latest"`)
	}
	if w := do("/v1/systemone", ask); w.Code != 403 || len(j.turns()) != 1 {
		t.Fatalf("Jev asked by a key held to a group alone: %d %s", w.Code, w.Body.String())
	}
	if listed() {
		t.Fatal("Jev listed to a key held to a group alone")
	}
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"group/r", "ts/jev-latest"}}); err != nil {
		t.Fatal(err)
	}
	if w := do("/v1/systemone", ask); w.Code != 200 || len(j.turns()) != 2 {
		t.Fatalf("Jev picked for the key: %d %s", w.Code, w.Body.String())
	}
	if !listed() {
		t.Fatal("Jev picked for the key isn't listed to it")
	}
}

// kautism on Discord: a gateway key given one model, gemini 3.8, was
// shown — and could use — a routing group the user made of gemini 3.8
// alone, though the user never gave the key that group. A group of the
// user's own is the key's only when it names it, on every route: the
// model lists, chat completions, Anthropic's messages and count_tokens.
// A bare name that is both such a group and the key's model is the model.
// A group magpie found, one model two providers serve, is the key's when
// it may use both, as before; a key naming the group is given it.
func TestGatewayKeyGivenAModelIsNotGivenItsGroup(t *testing.T) {
	fresh(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	google := &fake{t: t, ctype: "application/json", reply: `{"id":"from-google","choices":[]}`}
	vertex := &fake{t: t, ctype: "application/json", reply: `{"id":"from-vertex","choices":[]}`}
	for _, x := range []struct {
		p provider.Provider
		f *fake
	}{
		{provider.Provider{ID: "google", Name: "Google", Key: "k", Models: []string{"gemini-3.8", "gemini-3.1-pro"}}, google},
		{provider.Provider{ID: "vertex", Name: "Vertex", Key: "k", Models: []string{"gemini-3.8"}}, vertex},
	} {
		up := httptest.NewServer(x.f)
		t.Cleanup(up.Close)
		x.p.Chat = up.URL + "/v1"
		if err := provider.Save(x.p); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []provider.Group{
		{ID: "only-38", Name: "Only 3.8", Members: []string{"google/gemini-3.8"}, Routing: provider.Ordered},
		{ID: "gemini-3-8", Name: "Gemini 3.8", Members: []string{"google/gemini-3.8"}, Routing: provider.Ordered},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	keys, secrets := newCaller(t, "One model", "Both providers", "Given the group")
	for i, ms := range [][]string{{"google/gemini-3.8"}, {"google/gemini-3.8", "vertex/gemini-3.8"}, {"google/gemini-3.8", "group/only-38"}} {
		if _, err := access.Update("models-key", access.Change{Key: keys[i].ID, Models: ms}); err != nil {
			t.Fatal(err)
		}
	}
	h := New().Handler()
	do := func(secret, path, body string) *httptest.ResponseRecorder {
		method := "POST"
		if body == "" {
			method = "GET"
		}
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	listed := func(secret string) []string {
		var l struct{ Data []struct{ ID string } }
		w := do(secret, "/v1/models", "")
		if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
			t.Fatal(w.Body.String())
		}
		var ids []string
		for _, m := range l.Data {
			ids = append(ids, m.ID)
		}
		slices.Sort(ids)
		return ids
	}
	chat := func(secret, model string) *httptest.ResponseRecorder {
		return do(secret, "/v1/chat/completions", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
	}
	if ids := listed(secrets[0]); !slices.Equal(ids, []string{"google/gemini-3.8"}) {
		t.Fatalf("a key given google/gemini-3.8 lists %v", ids)
	}
	if w := do(secrets[0], "/v1/models/group/only-38", ""); w.Code == 200 {
		t.Fatal("a key found a group it wasn't given", w.Body.String())
	}
	for _, model := range []string{"group/only-38", "group/gemini-3-8", "Only 3.8", "google/gemini-3.1-pro"} {
		if w := chat(secrets[0], model); w.Code != 403 {
			t.Fatalf("chat %s: %d %s", model, w.Code, w.Body.String())
		}
		msg := `{"model":"` + model + `","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
		if w := do(secrets[0], "/v1/messages", msg); w.Code != 403 {
			t.Fatalf("messages %s: %d %s", model, w.Code, w.Body.String())
		}
		if w := do(secrets[0], "/v1/messages/count_tokens", msg); w.Code != 403 {
			t.Fatalf("count_tokens %s: %d %s", model, w.Code, w.Body.String())
		}
	}
	if google.calls != 0 || vertex.calls != 0 {
		t.Fatal("a refused request reached a provider", google.calls, vertex.calls)
	}
	// its model, by id and bare — the bare name the group Gemini 3.8 goes
	// by too — is served, and counted
	for _, model := range []string{"google/gemini-3.8", "gemini-3.8"} {
		if w := chat(secrets[0], model); w.Code != 200 || !strings.Contains(w.Body.String(), "from-google") {
			t.Fatalf("chat %s: %d %s", model, w.Code, w.Body.String())
		}
		if w := do(secrets[0], "/v1/messages/count_tokens", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`); w.Code != 200 {
			t.Fatalf("count_tokens %s: %d %s", model, w.Code, w.Body.String())
		}
	}
	// a group magpie found: one model of both providers
	if ids := listed(secrets[1]); !slices.ContainsFunc(ids, func(id string) bool { return strings.HasPrefix(id, "auto/") }) || slices.Contains(ids, "group/only-38") || slices.Contains(ids, "group/gemini-3-8") {
		t.Fatalf("a key given both providers' gemini-3.8 lists %v", ids)
	}
	if w := chat(secrets[1], "group/auto-gemini-3-8"); w.Code != 200 {
		t.Fatalf("the found group: %d %s", w.Code, w.Body.String())
	}
	// given the group, it is the key's
	if ids := listed(secrets[2]); !slices.Equal(ids, []string{"google/gemini-3.8", "group/only-38"}) {
		t.Fatalf("a key given group/only-38 lists %v", ids)
	}
	if w := chat(secrets[2], "group/only-38"); w.Code != 200 || !strings.Contains(w.Body.String(), "from-google") {
		t.Fatalf("a group given: %d %s", w.Code, w.Body.String())
	}
}
