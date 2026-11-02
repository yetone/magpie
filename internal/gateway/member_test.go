package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A reply from a group says which member answered it (#822): its
// X-Magpie-Provider and X-Magpie-Model headers always, relayed or
// translated, streamed or not, after a failover too; its body's model the
// group the agent asked for, unless ReplyModel member (or the request's
// X-Magpie-Response-Model: member) asks for magpie's provider/model id
// there — never for Claude Code or Codex, which read it themselves — or
// ReplyModel or X-Magpie-Response-Model vendor for the vendor's own name,
// for every agent. memberModel in a file from before ReplyModel still
// names the member; X-Magpie-Response-Model: asked the model asked for.
func TestReplyNamesMember(t *testing.T) {
	bodies := map[string]string{
		"/v1/messages":         `{"model":"group/auto","max_tokens":100,"messages":[{"role":"user","content":"hi"}]`,
		"/v1/chat/completions": `{"model":"group/auto","messages":[{"role":"user","content":"hi"}]`,
		"/v1/responses":        `{"model":"group/auto","input":"hi"`,
	}
	paths := []struct {
		name  string
		proto provider.Protocol // the vendor's
		agent string            // the agent's endpoint
	}{
		{"anthropic", provider.Anthropic, "/v1/messages"},
		{"chat", provider.Chat, "/v1/chat/completions"},
		{"responses", provider.Responses, "/v1/responses"},
		{"chat from anthropic", provider.Anthropic, "/v1/chat/completions"},
		{"anthropic from chat", provider.Chat, "/v1/messages"},
		{"responses from chat", provider.Chat, "/v1/responses"},
	}
	const served = "glm-5.3-flash-202606"
	for _, pa := range paths {
		for _, stream := range []bool{false, true} {
			for _, c := range []struct {
				name   string
				reply  string // Settings' ReplyModel; old: memberModel in a file from before it
				header string // X-Magpie-Response-Model
				token  string // the agent's key
				want   string // the model the body names
			}{
				{"default", "", "", "", "group/auto"},
				{"on", "member", "", "", "b/glm-5.3-flash"},
				{"old file on", "old", "", "", "b/glm-5.3-flash"},
				{"asked", "", "member", "", "b/glm-5.3-flash"},
				{"on, vendor asked", "member", "vendor", "", served},
				{"vendor asked", "", "vendor", "", served},
				// ZoeCats on Discord (after v0.1.2760): member picked in
				// Settings names the member to Claude Code and Codex too,
				// as vendor does; they got the name asked for
				{"on, codex", "member", "", TokenFor("codex"), "b/glm-5.3-flash"},
				{"on, claude code", "member", "", TokenFor("claude"), "b/glm-5.3-flash"},
				{"on, claude desktop", "member", "", TokenFor("claude-desktop"), "b/glm-5.3-flash"},
				{"old file on, codex", "old", "", TokenFor("codex"), "b/glm-5.3-flash"},
				{"on, codex, name asked", "member", "asked", TokenFor("codex"), "group/auto"},
				// ZoeCats on Discord: the vendor's own name, as before the
				// asked-for name, for every agent and every request
				{"vendor", "vendor", "", "", served},
				{"vendor, codex", "vendor", "", TokenFor("codex"), served},
				{"vendor, claude code", "vendor", "", TokenFor("claude"), served},
				{"vendor, member asked", "vendor", "member", "", "b/glm-5.3-flash"},
				{"vendor, name asked", "vendor", "asked", "", "group/auto"},
				{"on, name asked", "member", "asked", "", "group/auto"},
			} {
				name := pa.name + "/" + c.name
				if stream {
					name += "/stream"
				}
				t.Run(name, func(t *testing.T) {
					fresh(t)
					// a fails, so b answers after a failover
					down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						http.Error(w, `{"error":{"message":"overloaded"}}`, 529)
					}))
					t.Cleanup(down.Close)
					up := httptest.NewServer(servedBy(pa.proto, served))
					t.Cleanup(up.Close)
					for _, p := range []provider.Provider{
						{ID: "a", Name: "A", Key: "k", Models: []string{"glm-5.3-flash"}},
						{ID: "b", Name: "B", Key: "k", Models: []string{"glm-5.3-flash"}},
					} {
						url := down.URL
						if p.ID == "b" {
							url = up.URL
						}
						switch pa.proto {
						case provider.Chat:
							p.Chat = url + "/v1"
						case provider.Responses:
							p.Responses = url + "/v1"
						case provider.Anthropic:
							p.Anthropic = url
						}
						if err := provider.Save(p); err != nil {
							t.Fatal(err)
						}
					}
					if err := provider.SaveGroup(provider.Group{Name: "auto", Routing: provider.Ordered, Members: []string{"a/glm-5.3-flash", "b/glm-5.3-flash"}}); err != nil {
						t.Fatal(err)
					}
					switch c.reply {
					case "":
					case "old":
						if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(settings.Path(), []byte(`{"memberModel":true}`), 0o600); err != nil {
							t.Fatal(err)
						}
					default:
						if err := settings.Save(settings.Settings{ReplyModel: c.reply}); err != nil {
							t.Fatal(err)
						}
					}
					body := bodies[pa.agent]
					if stream {
						body += `,"stream":true`
					}
					req := httptest.NewRequest("POST", pa.agent, strings.NewReader(body+"}"))
					if c.header != "" {
						req.Header.Set(responseModelHeader, c.header)
					}
					if c.token != "" {
						req.Header.Set("Authorization", "Bearer "+c.token)
					}
					rec := httptest.NewRecorder()
					s := New()
					s.Handler().ServeHTTP(rec, req)
					if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hi") {
						t.Fatalf("%d %s", rec.Code, rec.Body)
					}
					if r := lastRoute(s); len(r.Tries) != 2 {
						t.Fatalf("tries %+v", r.Tries)
					}
					if p, m := rec.Header().Get(providerHeader), rec.Header().Get(modelHeader); p != "b" || m != "b/glm-5.3-flash" {
						t.Errorf("headers say %q %q", p, m)
					}
					want := c.want
					names := modelsNamed(rec.Body.String(), stream)
					if len(names) == 0 {
						t.Fatalf("no model named in %s", rec.Body)
					}
					for _, n := range names {
						if n != want {
							t.Fatalf("the reply names %q, want %q:\n%s", n, want, rec.Body)
						}
					}
					if r := lastRoute(s); r.Served != served {
						t.Errorf("the route says %q served it, want the vendor's %q", r.Served, served)
					}
				})
			}
		}
	}
}

// modelsNamed are the models a reply's body, or each of its events, names.
func modelsNamed(body string, stream bool) []string {
	var datas []string
	if !stream {
		datas = []string{body}
	} else {
		sc := bufio.NewScanner(strings.NewReader(body))
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data:"); ok {
				datas = append(datas, strings.TrimSpace(d))
			}
		}
	}
	var out []string
	for _, d := range datas {
		for _, path := range memberPaths {
			if v := gjson.Get(d, path); v.Type == gjson.String {
				out = append(out, v.Str)
			}
		}
	}
	return out
}

// An event's model is put in place, the rest of the line as it was; a
// line that names none, or isn't JSON, is untouched.
func TestMemberLine(t *testing.T) {
	for in, want := range map[string]string{
		"data: {\"model\":\"glm\",\"x\":1}\n":                   "data: {\"model\":\"b/glm\",\"x\":1}\n",
		"data:{\"message\":{\"model\":\"glm\"}}\r\n":            "data:{\"message\":{\"model\":\"b/glm\"}}\r\n",
		"data: {\"response\":{\"id\":\"r\",\"model\":\"glm\"}}": "data: {\"response\":{\"id\":\"r\",\"model\":\"b/glm\"}}",
		"data: {\"modelVersion\":\"glm\"}\n":                    "data: {\"modelVersion\":\"b/glm\"}\n",
		"data: [DONE]\n":                                        "data: [DONE]\n",
		"event: message_start\n":                                "event: message_start\n",
		"data: {\"delta\":{\"text\":\"\\\"model\\\":\"}}\n":     "data: {\"delta\":{\"text\":\"\\\"model\\\":\"}}\n",
	} {
		if got := string(memberLine([]byte(in), "b/glm")); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// A reply names the model the agent asked for whenever the request went
// out under another name, since agents compare the two and warn when they
// differ (issennknight on Discord: deepseek-v4.1-flash, which the user
// lists by that id, went out as deepseek-v4.1-flash-1017 and came back
// so named): a model the user lists by an id of theirs, a group, a
// provider/model id. One that went out under the name asked for keeps
// the vendor's own text, its dated name. The route and the usage log
// keep the vendor's name either way.
func TestReplyNamesAskedModel(t *testing.T) {
	paths := []struct {
		name  string
		proto provider.Protocol // the vendor's
		agent string            // the agent's endpoint
	}{
		{"anthropic", provider.Anthropic, "/v1/messages"},
		{"chat", provider.Chat, "/v1/chat/completions"},
		{"responses", provider.Responses, "/v1/responses"},
		{"chat from anthropic", provider.Anthropic, "/v1/chat/completions"},
		{"anthropic from chat", provider.Chat, "/v1/messages"},
		{"responses from chat", provider.Chat, "/v1/responses"},
	}
	cases := []struct {
		name   string
		asked  string
		ids    map[string]string // settings' ModelIDs
		served string            // what the vendor's reply says
		header string            // X-Magpie-Response-Model
		want   string
	}{
		{"own id", "deepseek-v4.1-flash", map[string]string{"d/deepseek-v4.1-flash": "deepseek-v4.1-flash-1017"}, "deepseek-v4.1-flash-1017", "", "deepseek-v4.1-flash"},
		{"own id, vendor asked", "deepseek-v4.1-flash", map[string]string{"d/deepseek-v4.1-flash": "deepseek-v4.1-flash-1017"}, "deepseek-v4.1-flash-1017", "vendor", "deepseek-v4.1-flash-1017"},
		{"group", "group/fast", nil, "deepseek-v4.1-flash-1017", "", "group/fast"},
		{"provider id", "d/deepseek-v4.1-flash", nil, "deepseek-v4.1-flash", "", "d/deepseek-v4.1-flash"},
		{"as asked, dated", "deepseek-v4.1-flash", nil, "deepseek-v4.1-flash-20261017", "", "deepseek-v4.1-flash-20261017"},
	}
	for _, pa := range paths {
		for _, stream := range []bool{false, true} {
			for _, c := range cases {
				name := pa.name + "/" + c.name
				if stream {
					name += "/stream"
				}
				t.Run(name, func(t *testing.T) {
					fresh(t)
					var mu sync.Mutex
					var sent []string // the models the vendor was asked for
					serve := servedBy(pa.proto, c.served)
					up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						b, _ := io.ReadAll(r.Body)
						mu.Lock()
						sent = append(sent, gjson.GetBytes(b, "model").Str)
						mu.Unlock()
						r.Body = io.NopCloser(bytes.NewReader(b))
						serve(w, r)
					}))
					t.Cleanup(up.Close)
					p := provider.Provider{ID: "d", Name: "D", Key: "k", Models: []string{"deepseek-v4.1-flash"}}
					switch pa.proto {
					case provider.Chat:
						p.Chat = up.URL + "/v1"
					case provider.Responses:
						p.Responses = up.URL + "/v1"
					case provider.Anthropic:
						p.Anthropic = up.URL
					}
					if err := provider.Save(p); err != nil {
						t.Fatal(err)
					}
					if err := provider.SaveGroup(provider.Group{Name: "fast", Routing: provider.Ordered, Members: []string{"d/deepseek-v4.1-flash"}}); err != nil {
						t.Fatal(err)
					}
					if c.ids != nil {
						if err := settings.Save(settings.Settings{ModelIDs: c.ids}); err != nil {
							t.Fatal(err)
						}
					}
					m, _ := json.Marshal(c.asked)
					body := `{"model":` + string(m) + `,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]`
					if pa.agent == "/v1/responses" {
						body = `{"model":` + string(m) + `,"input":"hi"`
					}
					if stream {
						body += `,"stream":true`
					}
					req := httptest.NewRequest("POST", pa.agent, strings.NewReader(body+"}"))
					if c.header != "" {
						req.Header.Set(responseModelHeader, c.header)
					}
					rec := httptest.NewRecorder()
					s := New()
					s.Handler().ServeHTTP(rec, req)
					if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hi") {
						t.Fatalf("%d %s", rec.Code, rec.Body)
					}
					mu.Lock()
					t.Logf("sent as %v", sent)
					mu.Unlock()
					names := modelsNamed(rec.Body.String(), stream)
					if len(names) == 0 {
						t.Fatalf("no model named in %s", rec.Body)
					}
					for _, n := range names {
						if n != c.want {
							t.Fatalf("the reply names %q, want %q:\n%s", n, c.want, rec.Body)
						}
					}
					if r := lastRoute(s); r.Served != c.served {
						t.Errorf("the route says %q served it, want the vendor's %q", r.Served, c.served)
					}
					if calls := s.Recent(); len(calls) == 0 || calls[0].Usage.Served != c.served {
						t.Errorf("the log says %+v served it, want the vendor's %q", calls, c.served)
					}
				})
			}
		}
	}
}

// A reply to a request that came in under one of magpie's own aliases for
// Claude Desktop or Claude Science keeps the vendor's own name (#4667):
// the alias — magpie-<n>.anthropic.claude-opus-5-5 among them — is
// magpie's deployment name, not one the vendor knows, and naming the reply
// with it put that name in Claude Code's usage records, where ccusage
// counts it as a model of its own instead of the one the subscription
// served. Before the asked-name rule these ids were served the vendor's
// name, as every other agent still is.
func TestReplyToMagpieAliasKeepsVendorName(t *testing.T) {
	const served = "opus-5-5" // what the subscription's reply says
	aliases := []string{
		"magpie-" + aliasNumber("sub/claude-opus-5-5") + desktopClaudeInfix + "claude-opus-5-5",
		"magpie-" + aliasNumber("sub/claude-opus-5-5") + desktopClaudeInfix + "claude-opus-5-5[1m]",
		aliasFor("sub/claude-opus-5-5"),
		desktopEffortAlias + aliasNumber("sub/claude-opus-5-5"),
		scienceAlias + aliasNumber("sub/claude-opus-5-5"),
	}
	for _, stream := range []bool{false, true} {
		for _, asked := range aliases {
			name := asked
			if stream {
				name += "/stream"
			}
			t.Run(name, func(t *testing.T) {
				fresh(t)
				up := httptest.NewServer(servedBy(provider.Anthropic, served))
				t.Cleanup(up.Close)
				if err := provider.Save(provider.Provider{ID: "sub", Name: "Sub", Key: "k", Anthropic: up.URL, Models: []string{"claude-opus-5-5"}}); err != nil {
					t.Fatal(err)
				}
				m, _ := json.Marshal(asked)
				body := `{"model":` + string(m) + `,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]`
				if stream {
					body += `,"stream":true`
				}
				rec := httptest.NewRecorder()
				s := New()
				s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body+"}")))
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hi") {
					t.Fatalf("%d %s", rec.Code, rec.Body)
				}
				names := modelsNamed(rec.Body.String(), stream)
				if len(names) == 0 {
					t.Fatalf("no model named in %s", rec.Body)
				}
				for _, n := range names {
					if n != served {
						t.Fatalf("the reply names %q, want the vendor's %q:\n%s", n, served, rec.Body)
					}
				}
				if r := lastRoute(s); r.Served != served {
					t.Errorf("the route says %q served it, want the vendor's %q", r.Served, served)
				}
				if calls := s.Recent(); len(calls) == 0 || calls[0].Usage.Served != served {
					t.Errorf("the log says %+v served it, want the vendor's %q", calls, served)
				}
			})
		}
	}
}

// A Gemini agent asks in the URL, and the reply's modelVersion names that
// model when the request went out under another name.
func TestGeminiReplyNamesAskedModel(t *testing.T) {
	for _, c := range []struct{ reply, want string }{
		{"", "deepseek-v4.1-flash"},
		{"member", "d/deepseek-v4.1-flash"},
		{"vendor", "deepseek-v4.1-flash-1017"},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprint(c.reply, " stream ", stream), func(t *testing.T) {
				fresh(t)
				up := httptest.NewServer(servedBy(provider.Chat, "deepseek-v4.1-flash-1017"))
				t.Cleanup(up.Close)
				if err := provider.Save(provider.Provider{ID: "d", Name: "D", Key: "k", Chat: up.URL + "/v1", Models: []string{"deepseek-v4.1-flash"}}); err != nil {
					t.Fatal(err)
				}
				if err := settings.Save(settings.Settings{ReplyModel: c.reply, ModelIDs: map[string]string{"d/deepseek-v4.1-flash": "deepseek-v4.1-flash-1017"}}); err != nil {
					t.Fatal(err)
				}
				path := "/v1beta/models/deepseek-v4.1-flash:generateContent"
				if stream {
					path = "/v1beta/models/deepseek-v4.1-flash:streamGenerateContent?alt=sse"
				}
				rec := httptest.NewRecorder()
				New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)))
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hi") {
					t.Fatalf("%d %s", rec.Code, rec.Body)
				}
				names := modelsNamed(rec.Body.String(), stream)
				if len(names) == 0 {
					t.Fatalf("no model named in %s", rec.Body)
				}
				for _, n := range names {
					if n != c.want {
						t.Fatalf("the reply names %q, want %q:\n%s", n, c.want, rec.Body)
					}
				}
			})
		}
	}
}

// A run of a stream's lines is named line by line, and a run that names
// no model, or a name of "", is the same slice back.
func TestMemberLines(t *testing.T) {
	in := []byte("event: response.created\ndata: {\"response\":{\"model\":\"v\"}}\n\nevent: response.output_text.delta\ndata: {\"delta\":\"hi\"}\n\ndata: {\"model\":\"v\"}\n")
	want := "event: response.created\ndata: {\"response\":{\"model\":\"asked\"}}\n\nevent: response.output_text.delta\ndata: {\"delta\":\"hi\"}\n\ndata: {\"model\":\"v\"}\n"
	if got := string(memberLines(in, "asked", memberPathsOf(provider.Responses)[1:])); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	plain := []byte("data: {\"delta\":\"hi\"}\n\n")
	if got := memberLines(plain, "asked", memberPaths); &got[0] != &plain[0] {
		t.Error("a run with no model was copied")
	}
	if got := memberLines(in, "", memberPaths); &got[0] != &in[0] {
		t.Error("no name copied the run")
	}
}

// namedAs is a reply as an agent that asked for asked gets it: every model
// it names is that one (memberWriter), the rest as the vendor wrote it.
func namedAs(reply, asked string) string {
	return replyModels.ReplaceAllString(reply, `"$1"$2"`+asked+`"`)
}

var replyModels = regexp.MustCompile(`"(model|modelVersion)"(\s*:\s*)"[^"]*"`)
