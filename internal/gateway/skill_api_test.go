package gateway

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"gopkg.in/yaml.v3"
)

// skillAPIPath is the magpie-api skill (thedavidweng on X): the one an
// agent installs from Library › Market › Skills, which teaches it this
// gateway's own API. The plugins repo (magpie-community/plugins,
// skills/magpie-api/SKILL.md) serves it; this copy is the same bytes, kept
// here so the test below holds it to the handlers.
const skillAPIPath = "testdata/magpie-api/SKILL.md"

// skillField is a field the skill documents under one endpoint, as a path
// into the reply ("data", "windows", "used").
type skillField struct {
	path []string
	line int
}

// skillEndpoints reads the skill's "## GET /path" sections and the fields
// each lists: a bullet names its fields in backticks before its first
// colon, and an indented bullet is a field of the first one its parent
// names.
func skillEndpoints(t *testing.T, doc string) map[string][]skillField {
	t.Helper()
	bullet := regexp.MustCompile("^( *)- (.*)$")
	name := regexp.MustCompile("`([^`]+)`")
	ident := regexp.MustCompile("^[a-z][a-z_A-Z]*$")
	out := map[string][]skillField{}
	section := ""
	var parents []string
	for i, line := range strings.Split(doc, "\n") {
		if h, ok := strings.CutPrefix(line, "## "); ok {
			section, parents = "", nil
			if p, ok := strings.CutPrefix(h, "GET "); ok {
				section = p
				out[section] = nil
			}
			continue
		}
		m := bullet.FindStringSubmatch(line)
		if section == "" || m == nil {
			continue
		}
		level := len(m[1]) / 2
		if len(m[1])%2 != 0 || level > len(parents) {
			t.Fatalf("SKILL.md:%d: a bullet indented %d spaces under %v", i+1, len(m[1]), parents)
		}
		// the bullet's head: up to its first colon outside backticks
		text, head := m[2], ""
		in := false
		for j, c := range text {
			if c == '`' {
				in = !in
			}
			if c == ':' && !in {
				head = text[:j]
				break
			}
		}
		var fields []string
		for _, f := range name.FindAllStringSubmatch(head, -1) {
			if !ident.MatchString(f[1]) {
				t.Fatalf("SKILL.md:%d: %q is no field name", i+1, f[1])
			}
			fields = append(fields, f[1])
		}
		if len(fields) == 0 {
			t.Fatalf("SKILL.md:%d: a bullet under GET %s names no field before its colon: %q", i+1, section, line)
		}
		parents = append(parents[:level], fields[0])
		for _, f := range fields {
			out[section] = append(out[section], skillField{path: append(slices.Clone(parents[:level]), f), line: i + 1})
		}
	}
	return out
}

// inReply: path names a field of some object in the reply.
func inReply(v any, path []string) bool {
	if len(path) == 0 {
		return true
	}
	switch v := v.(type) {
	case []any:
		for _, e := range v {
			if inReply(e, path) {
				return true
			}
		}
	case map[string]any:
		if c, ok := v[path[0]]; ok {
			return inReply(c, path[1:])
		}
	}
	return false
}

// skillJSONField is typ's field written under the JSON name, through pointers,
// slices and embedded structs.
func skillJSONField(typ reflect.Type, name string) (reflect.Type, bool) {
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil, false
	}
	for i := range typ.NumField() {
		f := typ.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == "-" || !f.IsExported() {
			continue
		}
		if tag == "" && f.Anonymous {
			if ft, ok := skillJSONField(f.Type, name); ok {
				return ft, true
			}
			continue
		}
		if cmp.Or(tag, f.Name) == name {
			return f.Type, true
		}
	}
	return nil, false
}

// inType: path names a field the handler's type writes (one the reply
// leaves out when empty, omitempty).
func inType(typ reflect.Type, path []string) bool {
	if typ == nil {
		return false
	}
	for _, p := range path {
		ft, ok := skillJSONField(typ, p)
		if !ok {
			return false
		}
		typ = ft
	}
	return true
}

// replyOfType fails on a field of the reply that typ doesn't write: the
// type stands for the reply only while the handler writes it.
func replyOfType(t *testing.T, where string, v any, typ reflect.Type) {
	t.Helper()
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return
	}
	switch v := v.(type) {
	case []any:
		for _, e := range v {
			replyOfType(t, where, e, typ)
		}
	case map[string]any:
		for k, c := range v {
			ft, ok := skillJSONField(typ, k)
			if !ok {
				t.Errorf("%s: the reply has %q, which %s doesn't write: the handler writes another type", where, k, typ)
				continue
			}
			replyOfType(t, where+"."+k, c, ft)
		}
	}
}

// The skill's front matter is read by strict YAML loaders too, magpie's
// own Library (yaml.v3, in library.parseMeta) among them: a plain
// description with "magpie: choosing" in it was a nested mapping to them,
// so pi refused the skill and the Library read no name or description
// (iAInNet, plugins#1621).
func TestMagpieAPISkillFrontMatterIsYAML(t *testing.T) {
	b, err := os.ReadFile(skillAPIPath)
	if err != nil {
		t.Fatal(err)
	}
	rest, ok := strings.CutPrefix(string(b), "---\n")
	front, _, ok2 := strings.Cut(rest, "\n---\n")
	if !ok || !ok2 {
		t.Fatal("the skill has no front matter")
	}
	var m struct{ Name, Description string }
	if err := yaml.Unmarshal([]byte(front), &m); err != nil {
		t.Fatalf("the front matter isn't YAML: %v", err)
	}
	if m.Name != "magpie-api" || !strings.Contains(m.Description, "Use when orchestrating work across models or agents through magpie: choosing") {
		t.Fatalf("the front matter reads as %+v", m)
	}
}

// The magpie-api skill tells an agent the gateway's own API: where it is,
// which key it takes, and the fields of /v1/magpie/quotas, /v1/models and
// /v1/magpie/route. Every field it lists is one the handler writes, found
// in a real reply here or, where the reply leaves it out when empty, in
// the type the handler writes it from; every rule it states (any key on
// this computer, a gateway key from another, a held key refused with
// gateway_key_held, the session header, the wait) is tried.
func TestMagpieAPISkillMatchesTheHandlers(t *testing.T) {
	b, err := os.ReadFile(skillAPIPath)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	if !strings.HasPrefix(doc, "---\nname: magpie-api\n") {
		t.Fatal("the skill's front matter names it otherwise")
	}
	endpoints := skillEndpoints(t, doc)
	for _, p := range []string{"/v1/magpie/quotas", "/v1/models", "/v1/magpie/route"} {
		if len(endpoints[p]) == 0 {
			t.Fatalf("the skill lists no fields of GET %s", p)
		}
	}

	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
	}))
	t.Cleanup(limited.Close)
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"ok","model":"m","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}))
	t.Cleanup(ok.Close)
	if err := provider.Save(provider.Provider{ID: "ra", Name: "RA", Key: "ra-key", Models: []string{"m"}, Chat: limited.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "rb", Name: "RB", Key: "rb-key", KeyName: "Team", Keys: []provider.KeyAccount{{Name: "Spare", Key: "rb-key-2"}}, Chat: ok.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	// rb's model as its live list tells it: a window, a reply limit, efforts
	if err := catalog.SaveLive("rb", ok.URL+"/v1", []catalog.Model{{ID: "m", Name: "Model M", Efforts: []string{"low", "high"}, Context: 200000, Output: 32000}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "R", Members: []string{"ra/m", "rb/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	// rb's key's card as last read, kept on disk (quotas.json): a window
	// counted in the vendor's credits, an unlimited one, and a balance. A
	// provider of two keys names each card by its key's name (cardKeys).
	sum := sha256.Sum256([]byte("rb-key"))
	resets := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second)
	kept := map[string]any{"rb/balance " + hex.EncodeToString(sum[:8]): map[string]any{"at": time.Now().Add(-time.Minute),
		"quota": provider.SubscriptionQuota{Provider: "rb", User: "Team", Plan: "Pro", Balance: "$12.40", Windows: []provider.QuotaWindow{
			{Name: "5 hours", Used: 40, ResetsAt: &resets, Display: "1.2k / 3k", Amount: 1200, Limit: 3000, Unit: "credits"},
			{Name: "Monthly", Unlimited: true},
		}}}}
	if err := os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "quotas.json"), mustJSON(kept), 0o600); err != nil {
		t.Fatal(err)
	}
	provider.ForgetKeptCardsForTest()
	provider.ForgetBalances()
	provider.ForgetPlanQuotas()
	// gateway keys, magpie shared on the local network: one held to a
	// budget
	keys, secrets := newCaller(t, "Laptop", "Budget")
	if _, err := access.Update("limit-key", access.Change{Key: keys[1].ID, Limit: &access.Limit{Period: "day", Tokens: 1_000_000}}); err != nil {
		t.Fatal(err)
	}

	h := lanGuard(New().Handler())
	const here, lan = "127.0.0.1:5000", "192.168.1.9:5000"
	do := func(method, from, target, body string, hdr ...string) (int, any) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		r := httptest.NewRequest(method, target, rd)
		r.RemoteAddr = from
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	field := func(v any, path ...string) any {
		for _, p := range path {
			m, _ := v.(map[string]any)
			v = m[p]
		}
		return v
	}
	// checks each field the skill lists under GET path: in the reply, or
	// in the type the handler writes it from
	documented := func(t *testing.T, path string, reply any, typ reflect.Type) {
		t.Helper()
		if typ != nil {
			replyOfType(t, path, reply, typ)
		}
		for _, f := range endpoints[path] {
			switch {
			case inReply(reply, f.path):
			case inType(typ, f.path):
				t.Logf("GET %s: %s is left out of this reply when empty; %s writes it", path, strings.Join(f.path, "."), typ)
			default:
				t.Errorf("SKILL.md:%d: GET %s has no field %s", f.line, path, strings.Join(f.path, "."))
			}
		}
	}

	t.Run("find the gateway", func(t *testing.T) {
		if !strings.Contains(doc, "`http://"+DefaultAddr+"`") {
			t.Errorf("the skill doesn't name the default address %s", DefaultAddr)
		}
		code, out := do("GET", here, "/api/hello", "")
		if code != 200 || field(out, "name") != "magpie" || !strings.Contains(doc, "`GET /api/hello` answers `{\"name\":\"magpie\",…}`") {
			t.Errorf("/api/hello: %d %v", code, out)
		}
	})

	t.Run("keys", func(t *testing.T) {
		// this computer: any key, or none, by either header
		for _, hdr := range [][]string{nil, {"Authorization", "Bearer "}, {"Authorization", "Bearer not-a-key"}, {"x-api-key", "not-a-key"}} {
			if code, out := do("GET", here, "/v1/magpie/quotas?kept=1", "", hdr...); code != 200 {
				t.Errorf("here with %v: %d %v", hdr, code, out)
			}
		}
		// another computer: a gateway key, by either header, and nothing else
		for _, hdr := range [][]string{{"Authorization", "Bearer " + secrets[0]}, {"x-api-key", secrets[0]}} {
			if code, out := do("GET", lan, "/v1/magpie/quotas?kept=1", "", hdr...); code != 200 {
				t.Errorf("from the LAN with %s: %d %v", hdr[0], code, out)
			}
		}
		for _, hdr := range [][]string{nil, {"Authorization", "Bearer not-a-key"}} {
			if code, _ := do("GET", lan, "/v1/magpie/quotas?kept=1", "", hdr...); code == 200 {
				t.Errorf("from the LAN with %v: told the quotas", hdr)
			}
		}
		// a held key is refused the quotas, with the code the skill names,
		// and told its own limit
		code, out := do("GET", lan, "/v1/magpie/quotas?kept=1", "", "Authorization", "Bearer "+secrets[1])
		if code != 403 || field(out, "error", "code") != provider.KeyHeldCode || !strings.Contains(doc, "`error.code` `"+provider.KeyHeldCode+"`") {
			t.Errorf("a held key: %d %v", code, out)
		}
		if code, out := do("GET", lan, "/v1/magpie/limit", "", "Authorization", "Bearer "+secrets[1]); code != 200 || !strings.Contains(doc, "`GET /v1/magpie/limit`") {
			t.Errorf("a held key's limit: %d %v", code, out)
		}
	})

	// a turn of session s1 on the group: ra refuses, rb answers
	if !strings.Contains(doc, "`X-Magpie-Session: <id>`") || SessionHeader != "X-Magpie-Session" {
		t.Fatalf("the skill names the session header otherwise than %s", SessionHeader)
	}
	turn := func(t *testing.T, session string, hdr string) {
		t.Helper()
		code, out := do("POST", here, "/v1/chat/completions", `{"model":"group/r","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`, hdr, session)
		if code != 200 {
			t.Fatalf("the turn: %d %v", code, out)
		}
	}
	turn(t, "s1", SessionHeader)

	code, models := do("GET", here, "/v1/models", "", "Authorization", "Bearer x")
	if code != 200 {
		t.Fatalf("/v1/models: %d %v", code, models)
	}
	t.Run("GET /v1/models", func(t *testing.T) {
		out := models
		documented(t, "/v1/models", out, nil)
		var group, m map[string]any
		for _, e := range field(out, "data").([]any) {
			switch e := e.(map[string]any); e["id"] {
			case "group/r":
				group = e
			case "rb/m":
				m = e
			}
		}
		if group == nil || group["owned_by"] != "ra" {
			t.Errorf("a routing group is listed as group/<name>, owned by its first member's provider: %v", group)
		}
		if m == nil || m["owned_by"] != "rb" || m["display_name"] != "Model M" || m["reasoning"] != true ||
			!reflect.DeepEqual(m["reasoning_levels"], []any{"low", "high"}) || m["context_window"] != float64(200000) || m["max_output_tokens"] != float64(32000) {
			t.Errorf("rb/m: %v", m)
		}
		// the kinds the skill names are the ones the list takes
		for _, q := range []string{"image,video,embedding,rerank", "all"} {
			if !strings.Contains(doc, "`?kind="+q+"`") {
				t.Errorf("the skill doesn't name ?kind=%s", q)
			}
			if code, out := do("GET", here, "/v1/models?kind="+q, ""); code != 200 {
				t.Errorf("?kind=%s: %d %v", q, code, out)
			}
		}
	})

	t.Run("GET /v1/magpie/quotas", func(t *testing.T) {
		if !strings.Contains(doc, "`?kept=1`") {
			t.Error("the skill doesn't name ?kept=1")
		}
		code, out := do("GET", here, "/v1/magpie/quotas?kept=1", "", "Authorization", "Bearer x")
		if code != 200 || field(out, "object") != "list" {
			t.Fatalf("%d %v", code, out)
		}
		documented(t, "/v1/magpie/quotas", out, reflect.TypeFor[struct {
			Object string           `json:"object"`
			Data   []provider.Quota `json:"data"`
		}]())
		var card map[string]any
		for _, e := range field(out, "data").([]any) {
			e := e.(map[string]any)
			// a card's provider is the start of the ids of the models
			// that spend it
			if p, _ := e["provider"].(string); !slices.ContainsFunc(field(models, "data").([]any), func(m any) bool {
				return strings.HasPrefix(field(m, "id").(string), p+"/")
			}) {
				t.Errorf("no model id starts with %s/", p)
			}
			if e["provider"] == "rb" {
				card = e
			}
			if k := e["kind"]; !strings.Contains(doc, "`"+k.(string)+"` (") {
				t.Errorf("kind %v isn't one the skill names", k)
			}
		}
		if card == nil {
			t.Fatalf("no card of rb: %v", out)
		}
		ws, _ := card["windows"].([]any)
		if card["kind"] != "balance" || card["user"] != "Team" || card["balance"] != "$12.40" || len(ws) != 2 || card["last"] != true || card["lastServedAt"] == nil {
			t.Fatalf("rb's card: %v", card)
		}
		w := ws[0].(map[string]any)
		if w["used"] != float64(40) || w["remaining"] != float64(60) || w["resetsAt"] != resets.Format(time.RFC3339) || w["display"] != "1.2k / 3k" {
			t.Errorf("a window: %v", w)
		}
	})

	t.Run("GET /v1/magpie/route", func(t *testing.T) {
		if !strings.Contains(doc, "up to "+strconv.Itoa(int(routeWaitMax/time.Second))+" seconds") {
			t.Errorf("the skill doesn't say a wait is up to %s", routeWaitMax)
		}
		if code, out := do("GET", here, "/v1/magpie/route", ""); code != 400 {
			t.Errorf("no session named: %d %v", code, out)
		}
		// none yet: route null
		code, out := do("GET", here, "/v1/magpie/route?session=none", "")
		if code != 200 || field(out, "session") != "none" || field(out, "route") != nil {
			t.Errorf("a session with no route: %d %v", code, out)
		}
		// by ?session= and by the header, the same route
		code, out = do("GET", here, "/v1/magpie/route?session=s1", "")
		if code != 200 {
			t.Fatalf("%d %v", code, out)
		}
		if c, o := do("GET", here, "/v1/magpie/route", "", SessionHeader, "s1"); c != 200 || !reflect.DeepEqual(o, out) {
			t.Errorf("by the header: %d %v, by ?session= %v", c, o, out)
		}
		documented(t, "/v1/magpie/route", out, reflect.TypeFor[struct {
			Session string        `json:"session"`
			Seq     int64         `json:"seq"`
			Route   *SessionRoute `json:"route"`
		}]())
		rt, _ := field(out, "route").(map[string]any)
		tries, _ := field(rt, "tries").([]any)
		if rt["asked"] != "group/r" || rt["asked_effort"] != "high" || rt["group"] != "r" || rt["model"] != "rb/m" || rt["done"] != true || rt["status"] != float64(200) || len(tries) != 2 ||
			field(tries[0], "model") != "ra/m" || field(tries[0], "status") != float64(429) || field(tries[0], "fail") == nil || field(tries[1], "model") != "rb/m" {
			t.Errorf("the route: %v", out)
		}
		// after=<seq>&wait= waits for the session's next route
		seq := strconv.FormatInt(int64(field(out, "seq").(float64)), 10)
		// (t.Context ends the wait, should the test end first)
		polled := make(chan any, 1)
		go func() {
			r := httptest.NewRequestWithContext(t.Context(), "GET", "/v1/magpie/route?session=s1&after="+seq+"&wait=30", nil)
			r.RemoteAddr = here
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			var o any
			json.Unmarshal(w.Body.Bytes(), &o)
			polled <- o
		}()
		t.Cleanup(func() { <-polled })
		turn(t, "s1", SessionHeader)
		select {
		case o := <-polled:
			polled <- o // for the cleanup
			if n, _ := field(o, "seq").(float64); strconv.FormatInt(int64(n), 10) == seq {
				t.Errorf("the wait came back with the route it was past: %v", o)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("the wait never heard the next turn")
		}
		// an agent's own session header gives the session too
		turn(t, "cc-1", "X-Claude-Code-Session-Id")
		if _, o := do("GET", here, "/v1/magpie/route?session=cc-1", ""); field(o, "route") == nil {
			t.Errorf("a Claude Code session's route: %v", o)
		}
	})
}
