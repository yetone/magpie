package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// countAsked is an upstream that counts tokens, noting each time it's asked.
type countAsked struct {
	mu    sync.Mutex
	paths []string
}

func (c *countAsked) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.paths = append(c.paths, r.URL.Path)
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"input_tokens":999}`)
}

func (c *countAsked) asked() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.paths...)
}

func estimated(t *testing.T, rec *httptest.ResponseRecorder) int {
	t.Helper()
	var got struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != 200 || err != nil || got.InputTokens == 0 {
		t.Fatalf("count_tokens: %d %s", rec.Code, rec.Body)
	}
	return got.InputTokens
}

// Claude Code asks count_tokens before each turn; ZCode itself never does,
// and zcode.z.ai answers it with an error every time. A ZCode account's
// count is estimated, the vendor not asked.
func TestZCodeCountTokensEstimated(t *testing.T) {
	fresh(t)
	up := &countAsked{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	p := provider.Provider{ID: "zcode", Name: "ZCode", Anthropic: srv.URL + "/api/v1/zcode-plan/anthropic", Models: []string{"glm-5.1"},
		Account: &provider.Account{Agent: "zcode", User: "trial@example.com", Plan: "Start Plan"}}
	body := []byte(`{"model":"glm-5.1","messages":[{"role":"user","content":"hello there"}]}`)
	rec := httptest.NewRecorder()
	New().countOn(rec, httptest.NewRequest("POST", "/v1/messages/count_tokens", nil), p, "glm-5.1", true, body)
	if n := estimated(t, rec); n == 999 || len(up.asked()) > 0 {
		t.Fatalf("zcode.z.ai was asked to count: %v, %d", up.asked(), n)
	}
}

// ZCode moved onto its plugin estimates as the built-in does.
func TestMovedZCodeCountTokensEstimated(t *testing.T) {
	up := &countAsked{}
	movedFake(t, "zcode", up)
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages/count_tokens",
		strings.NewReader(`{"model":"zcode/fake-claude","messages":[{"role":"user","content":"hello there"}]}`)))
	if n := estimated(t, rec); n == 999 || len(up.asked()) > 0 {
		t.Fatalf("the vendor was asked to count: %v, %d", up.asked(), n)
	}
}

// edgeBlockPage is Alibaba Cloud's ESA block page, as zcode.z.ai served it.
const edgeBlockPage = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>405</title></head>
<body><div class="main"><h1>Sorry, your request has been blocked due to unusual activity.</h1>
<p>Request ID: 0bd6a2b417592xxxx</p>
<a href="https://errors.aliyun.com/error/405?code=blocked">errors.aliyun.com</a></div></body></html>`

// A vendor's edge firewall blocking this address answers an HTML page: the
// agent and the trace are told so in plain words, not the page, and the
// group's next member is asked, as for another provider's failure.
func TestEdgeBlockedSaidPlainly(t *testing.T) {
	fresh(t)
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusMethodNotAllowed)
		io.WriteString(w, edgeBlockPage)
	}))
	t.Cleanup(blocked.Close)
	var other int
	fine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		other++
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"ok","choices":[{"message":{"role":"assistant","content":"from b"}}]}`)
	}))
	t.Cleanup(fine.Close)
	for _, p := range []provider.Provider{
		{ID: "za", Name: "ZA", Key: "k", Models: []string{"m"}, Chat: blocked.URL + "/v1"},
		{ID: "zb", Name: "ZB", Key: "k", Models: []string{"m"}, Chat: fine.URL + "/v1"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}

	// alone: the agent is told what happened
	s := New()
	code, reply := postAs(t, s, "", `{"model":"za/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusMethodNotAllowed || !strings.Contains(reply, provider.BlockedHint) || strings.Contains(reply, "<") {
		t.Fatalf("blocked reply: %d %s", code, reply)
	}
	if tr := s.trace.routes[len(s.trace.routes)-1].Tries[0]; !strings.HasSuffix(tr.Error, " — "+provider.BlockedHint) || strings.Contains(tr.Error, "<") {
		t.Fatalf("trace: %+v", tr)
	}

	// in a group: the next member answers
	if err := provider.SaveGroup(provider.Group{Name: "Z", Members: []string{"za/m", "zb/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s = New()
	code, reply = postAs(t, s, "", `{"model":"group/z","messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusOK || !strings.Contains(reply, "from b") || other != 1 {
		t.Fatalf("group reply: %d %s (next asked %d)", code, reply, other)
	}
	tries := s.trace.routes[len(s.trace.routes)-1].Tries
	if len(tries) != 2 || tries[0].Rest == nil || !strings.Contains(tries[0].Error, provider.BlockedHint) {
		t.Fatalf("tries: %+v", tries)
	}
}
