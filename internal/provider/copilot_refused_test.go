package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// copilot371Models is a Copilot Student account's /models as #371 measured
// it: each model's policy.state, and whether /chat/completions served it.
// The issue gave no more of the list; its endpoints, picker flags, billing
// and gpt-4.1 standing as Copilot's base model are the list's usual ones.
func copilot371Models() string {
	var data []string
	add := func(id, extra string) {
		data = append(data, fmt.Sprintf(`{"id":%q,"name":%q,"model_picker_enabled":true,"supported_endpoints":["/chat/completions"],"capabilities":{"type":"chat"}%s}`, id, id, extra))
	}
	// policy enabled: only gpt-4.1 served
	for _, id := range []string{"claude-haiku-4.5", "gpt-5.6-luna", "gpt-5.4-mini", "kimi-k3", "mai-code-1.1-flash"} {
		add(id, `,"policy":{"state":"enabled"},"billing":{"is_premium":true,"multiplier":0.33}`)
	}
	add("gpt-5-mini", `,"policy":{"state":"enabled"},"billing":{"is_premium":false,"multiplier":0}`)
	add("gpt-4.1", `,"policy":{"state":"enabled"},"is_chat_default":true,"is_chat_fallback":true,"billing":{"is_premium":false,"multiplier":0}`)
	// policy disabled, with terms to accept: none served
	for _, id := range []string{"claude-opus-5", "claude-sonnet-5.5", "claude-fable-5.1", "gemini-3.5-flash", "gemini-3.6-flash", "gemini-3.7-flash", "gemini-3.8-flash", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-6-sol", "gpt-6.1-sol", "gpt-6-astra"} {
		add(id, `,"policy":{"state":"disabled","terms":"Enable access to the latest models."},"billing":{"is_premium":true,"multiplier":1}`)
	}
	// no policy: served
	add("gpt-4o", `,"billing":{"is_premium":false,"multiplier":0}`)
	return `{"data":[` + strings.Join(data, ",") + `]}`
}

// copilot371Served is what #371 found /chat/completions served the account.
var copilot371Served = []string{"gpt-4.1", "gpt-4o", "gpt-4o-mini", "gpt-3.5-turbo"}

// student371 stands in for Copilot's API for #371's account: the models
// above, refused as Copilot refuses them, and Auto's session (#256), when
// it has one, picking a model the account is refused.
type student371 struct {
	mu    sync.Mutex
	auto  bool     // whether /models/session gives a session; 404 when not
	asked []string // "<model> <Copilot-Session-Token> <status>"
}

func (c *student371) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	defer c.mu.Unlock()
	switch r.URL.Path {
	case "/models":
		io.WriteString(w, copilot371Models())
	case "/models/gpt-5.6-sol/policy", "/models/gpt-5.6-terra/policy":
		// terms accepted, as they are for any account: the model is no
		// more served for it
		io.WriteString(w, `{"state":"enabled"}`)
	case "/models/session":
		if !c.auto {
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"message":"Not Found"}}`)
			return
		}
		io.WriteString(w, `{"session_token":"auto-tok","selected_model":"claude-haiku-4.5","available_models":["claude-haiku-4.5","gpt-5-mini","gpt-4.1"],"expires_at":`+fmt.Sprint(time.Now().Add(time.Hour).Unix())+`}`)
	default:
		m := bodyModel(b)
		status := 200
		if !slices.Contains(copilot371Served, m) {
			status = 400
		}
		c.asked = append(c.asked, fmt.Sprintf("%s %s %d", m, r.Header.Get("Copilot-Session-Token"), status))
		w.WriteHeader(status)
		if status == 400 {
			io.WriteString(w, `{"error":{"message":"The requested model is not supported.","code":"model_not_supported","param":"model","type":"invalid_request_error"}}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}
}

func student371Provider(t *testing.T, auto bool) (Provider, *student371) {
	t.Helper()
	signIn(t)
	up := &student371{auto: auto}
	api := httptest.NewServer(up)
	t.Cleanup(api.Close)
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
	}))
	t.Cleanup(tokens.Close)
	old := CopilotTokenURL
	CopilotTokenURL = tokens.URL
	t.Cleanup(func() { CopilotTokenURL = old })
	copilotSessions = map[string]copilotSession{}
	copilotAutoSessions = map[string]copilotAutoSession{}
	copilotTerms = map[string]map[string]bool{}
	copilotPicks = map[string][]string{}
	copilotRefusedAt = map[string]map[string]time.Time{}

	p, _ := find(All(), "copilot")
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	return p, up
}

// send has p's backend answer body as the gateway sends it: once more when
// the account says the request is worth it.
func (s *student371) send(t *testing.T, ctx context.Context, p Provider, body string) int {
	t.Helper()
	for range 3 {
		req, _ := http.NewRequest("POST", p.Chat+"/chat/completions", strings.NewReader(body))
		if err := p.Sign(ctx, req, Chat, []byte(body)); err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 400 || !p.Retry(ctx, []byte(body), res.StatusCode, b) {
			return res.StatusCode
		}
	}
	return 0
}

func modelIDs(ms []catalog.Model) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

// #371: a Copilot Student account's list offers models Copilot then
// refuses it (400 "The requested model is not supported."), policy enabled
// or not, and magpie exposed the four picked for it — one of them not even
// in the list — every one refused. A pick the list no longer has is left
// out; a model the account was refused leaves its models and its picks.
func TestCopilotRefusedLeavesModels(t *testing.T) {
	p, up := student371Provider(t, false)
	p.Models = []string{"gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}
	if got := modelIDs(p.Exposed()); !slices.Equal(got, []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}) {
		t.Fatalf("exposed %q: gpt-6-luna isn't in the account's list", got)
	}
	for _, m := range p.Models[1:] {
		if code := up.send(t, context.Background(), p, `{"model":"`+m+`","messages":[]}`); code != 400 {
			t.Fatalf("%s: %d", m, code)
		}
	}
	// a model named by hand is sent once: the refusal is the agent's to see
	if len(up.asked) != 3 {
		t.Fatalf("asked %q", up.asked)
	}
	avail := modelIDs(p.Available())
	for _, m := range p.Models {
		if slices.Contains(avail, m) {
			t.Fatalf("%s still listed after Copilot refused it: %q", m, avail)
		}
	}
	exposed := modelIDs(p.Exposed())
	if len(exposed) == 0 || slices.ContainsFunc(exposed, func(id string) bool { return slices.Contains(p.Models, id) }) {
		t.Fatalf("exposed %q: none of the picks is served, so as if none were picked", exposed)
	}
	if !slices.Contains(exposed, "gpt-4.1") || !slices.Contains(exposed, CopilotAuto) {
		t.Fatalf("exposed %q", exposed)
	}
	// another account's refusals are its own
	if copilotRefuses("gho_other", "gpt-5.6-luna") || !copilotRefuses("gho_x", "gpt-5.6-luna") {
		t.Fatal("refusals are kept by account")
	}
	// a refusal is forgotten after a while: a plan may change
	copilotRefusedMu.Lock()
	copilotRefusedAt["gho_x"]["gpt-5.6-luna"] = time.Now().Add(-copilotRefusedFor)
	copilotRefusedMu.Unlock()
	if !slices.Contains(modelIDs(p.Available()), "gpt-5.6-luna") {
		t.Fatal("gpt-5.6-luna not listed again once its refusal is old")
	}
}

// #256: Copilot Auto for a Student account with no Auto session (404)
// stood in with the first enabled model of the list — claude-haiku-4.5,
// refused it. It takes Copilot's base model (is_chat_fallback), and passes
// over a model the account was refused.
func TestCopilotAutoFallbackServed(t *testing.T) {
	p, up := student371Provider(t, false)
	ctx, model, err := p.ResolveAuto(context.Background(), CopilotAuto)
	if err != nil || model != "gpt-4.1" {
		t.Fatalf("Auto stood in with %q, %v; want Copilot's base model gpt-4.1", model, err)
	}
	if code := up.send(t, ctx, p, `{"model":"gpt-4.1","messages":[]}`); code != 200 {
		t.Fatalf("auto: %d %q", code, up.asked)
	}
	// once refused that one too, the next it may pick
	copilotRefused(ctx, copilotApp{Token: "gho_x"}, "gpt-4.1", 400, []byte(`{"error":{"code":"model_not_supported"}}`))
	if _, model, _ := p.ResolveAuto(context.Background(), CopilotAuto); model != "gpt-5-mini" {
		t.Fatalf("after gpt-4.1 was refused, Auto picked %q", model)
	}
}

// #256 (v0.1.498 on): Auto's session picks a model Copilot then refuses the
// account. The request is sent again with another model Auto offers, in
// the same session, and Auto passes the refused model over from then on.
func TestCopilotAutoRefusedPicksAnother(t *testing.T) {
	p, up := student371Provider(t, true)
	ctx, model, err := p.ResolveAuto(context.Background(), CopilotAuto)
	if err != nil || model != "claude-haiku-4.5" {
		t.Fatalf("resolve: %q %v", model, err)
	}
	if code := up.send(t, ctx, p, `{"model":"claude-haiku-4.5","messages":[]}`); code != 200 {
		t.Fatalf("auto: %d %q", code, up.asked)
	}
	want := []string{"claude-haiku-4.5 auto-tok 400", "gpt-5-mini auto-tok 400", "gpt-4.1 auto-tok 200"}
	if !slices.Equal(up.asked, want) {
		t.Fatalf("asked %q, want %q", up.asked, want)
	}
	if _, model, _ := p.ResolveAuto(context.Background(), CopilotAuto); model != "gpt-4.1" {
		t.Fatalf("next Auto request picked %q", model)
	}
	// a refusal that isn't one of the model (the wrong endpoint) is the
	// gateway's to mend, not noted
	if copilotRefused(ctx, copilotApp{Token: "gho_x"}, "gpt-4o", 400, []byte(`{"error":{"message":"model gpt-4o is not accessible via the /chat/completions endpoint","code":"unsupported_api_for_model"}}`)) || copilotRefuses("gho_x", "gpt-4o") {
		t.Fatal("a wrong endpoint taken for a refused model")
	}
}
