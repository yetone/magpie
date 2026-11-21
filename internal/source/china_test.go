package source

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// the 「国内镜像」 switch is off in every test but those that turn it on,
// whatever the settings of the HOME the tests run in say; and that HOME is
// one of their own, never the user's
func TestMain(m *testing.M) {
	China = func() bool { return false }
	os.Exit(testenv.Run(m))
}

func chinaOn(t *testing.T) {
	was := China
	China = func() bool { return true }
	t.Cleanup(func() { China = was })
}

func TestChinaFirst(t *testing.T) {
	for raw, want := range map[string]string{
		"https://registry.npmjs.org/@magpie-community%2fopencode-kiro-auth":                                         "https://registry.npmmirror.com/@magpie-community%2fopencode-kiro-auth",
		"https://registry.npmjs.org/-/v1/search?text=opencode&size=20":                                              "https://registry.npmmirror.com/-/v1/search?text=opencode&size=20",
		"https://github.com/oven-sh/bun/releases/download/bun-v1.4.2/bun-darwin-aarch64.zip":                        "https://registry.npmmirror.com/-/binary/bun/bun-v1.4.2/bun-darwin-aarch64.zip",
		"https://raw.githubusercontent.com/magpie-community/plugins/main/registry.json":                             "https://cdn.jsdelivr.net/gh/magpie-community/plugins@main/registry.json",
		"https://raw.githubusercontent.com/anthropics/claude-plugins-official/main/.claude-plugin/marketplace.json": "https://cdn.jsdelivr.net/gh/anthropics/claude-plugins-official@main/.claude-plugin/marketplace.json",
		"https://raw.githubusercontent.com/anthropics/claude-plugins-official/main":                                 "",
		"https://raw.githubusercontent.com/anthropics/claude-code/main/.claude-plugin/marketplace.json":             "",
		"https://huggingface.co/LocalAI-io/privacy-filter-GGUF/resolve/935d868/privacy-filter-q8.gguf":              "https://hf-mirror.com/LocalAI-io/privacy-filter-GGUF/resolve/935d868/privacy-filter-q8.gguf",
		// no other model, and no page of Hugging Face's
		"https://huggingface.co/someone/else/resolve/main/x.gguf":                                     "",
		"https://huggingface.co/QuantumNous/astrlink-guard/resolve/7216b6a/int8/model_int8.onnx_data": "https://hf-mirror.com/QuantumNous/astrlink-guard/resolve/7216b6a/int8/model_int8.onnx_data",
		"https://huggingface.co/QuantumNous/other/resolve/7216b6a/x":                                  "",
		"https://huggingface.co/LocalAI-io/privacy-filter-GGUF":                                       "",
		"https://huggingface.co/LocalAI-io/privacy-filter-GGUF/resolve/main/x?dl=1":                   "",
		// npm's download counts are npmjs's own: npmmirror counts its own
		"https://api.npmjs.org/downloads/point/last-week/pkg": "",
		// nothing else of GitHub's: no proxy stands in for it
		"https://github.com/oven-sh/bun/releases/download/":                          "",
		"https://github.com/o/r/releases/download/v1/x.zip":                          "",
		"https://api.github.com/repos/oven-sh/bun/releases/latest":                   "",
		"https://raw.githubusercontent.com/someone/else/main/registry.json":          "",
		"https://raw.githubusercontent.com/magpie-community/plugins/main/x.json?t=1": "",
		"http://registry.npmjs.org/pkg":                                              "",
	} {
		if got := chinaFirst(raw); got != want {
			t.Errorf("chinaFirst(%s) = %q, want %q", raw, got, want)
		}
	}
}

func TestChinaComesFirst(t *testing.T) {
	t.Setenv("MAGPIE_MIRRORS", "")
	raw := "https://registry.npmjs.org/pkg"
	if got := URLs(raw); len(got) != 1 {
		t.Fatalf("switch off: URLs = %q", got)
	}
	chinaOn(t)
	if got := strings.Join(URLs(raw), " "); got != "https://registry.npmmirror.com/pkg "+raw {
		t.Fatalf("switch on: URLs = %q", got)
	}
	// with the fallback mirrors too, npmmirror once, first
	t.Setenv("MAGPIE_MIRRORS", "on")
	t.Setenv("MAGPIE_NPM_REGISTRY", "")
	if got := strings.Join(URLs(raw), " "); got != "https://registry.npmmirror.com/pkg "+raw {
		t.Fatalf("switch and fallback on: URLs = %q", got)
	}
	// GitHub's fallback proxy stays after the official address
	gh := "https://raw.githubusercontent.com/magpie-community/plugins/main/registry.json"
	want := "https://cdn.jsdelivr.net/gh/magpie-community/plugins@main/registry.json " + gh + " https://gh-proxy.com/" + gh
	if got := strings.Join(URLs(gh), " "); got != want {
		t.Fatalf("plugin list URLs = %q, want %q", got, want)
	}
}

// pair is a mirror and an official server, counting their requests
func pair(t *testing.T, mirror, official http.HandlerFunc) (m, o *httptest.Server, mn, on *atomic.Int32) {
	mn, on = new(atomic.Int32), new(atomic.Int32)
	m = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mn.Add(1); mirror(w, r) }))
	o = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { on.Add(1); official(w, r) }))
	t.Cleanup(m.Close)
	t.Cleanup(o.Close)
	return
}

func say(code int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code); io.WriteString(w, body) }
}

func TestChinaMirrorAnswers(t *testing.T) {
	m, o, mn, on := pair(t, say(200, "mirror"), say(200, "official"))
	req, _ := http.NewRequest("GET", o.URL+"/pkg", nil)
	res, err := do(http.DefaultClient, req, []string{m.URL + "/pkg", o.URL + "/pkg"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(b) != "mirror" || mn.Load() != 1 || on.Load() != 0 {
		t.Fatalf("got %q, mirror asked %d, official %d", b, mn.Load(), on.Load())
	}
}

// what a mirror in China hasn't copied yet (a version just published) is
// asked of the official address, where a fallback mirror's 404 would be
// the answer
func TestChinaMirrorsMissIsAskedOfficially(t *testing.T) {
	for _, code := range []int{404, 403, 500} {
		m, o, mn, on := pair(t, say(code, "no"), say(200, "official"))
		req, _ := http.NewRequest("GET", o.URL+"/pkg", nil)
		res, err := do(http.DefaultClient, req, []string{m.URL + "/pkg", o.URL + "/pkg"})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if string(b) != "official" || mn.Load() != 1 || on.Load() != 1 {
			t.Fatalf("%d: got %q, mirror asked %d, official %d", code, b, mn.Load(), on.Load())
		}
	}
	// the official 404 is the answer, the fallback mirror not asked
	m, o, mn, on := pair(t, say(200, "fallback"), say(404, "gone"))
	req, _ := http.NewRequest("GET", o.URL+"/pkg", nil)
	res, err := do(http.DefaultClient, req, []string{o.URL + "/pkg", m.URL + "/pkg"})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 404 || mn.Load() != 0 || on.Load() != 1 {
		t.Fatalf("official 404: %d, fallback asked %d, official %d", res.StatusCode, mn.Load(), on.Load())
	}
}

// the plugin list goes to jsDelivr with the switch, never to a proxy
func TestDoFaithful(t *testing.T) {
	t.Setenv("MAGPIE_MIRRORS", "on")
	t.Setenv("MAGPIE_GITHUB_MIRROR", "https://gh.invalid")
	var asked []string
	c := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		asked = append(asked, r.URL.String())
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}, Request: r}, nil
	})}
	list := "https://raw.githubusercontent.com/magpie-community/plugins/main/registry.json"
	ask := func() {
		asked = nil
		req, _ := http.NewRequest("GET", list, nil)
		res, err := DoFaithful(c, req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}
	ask()
	if strings.Join(asked, " ") != list {
		t.Fatalf("switch off: asked %q", asked)
	}
	chinaOn(t)
	ask()
	if strings.Join(asked, " ") != "https://cdn.jsdelivr.net/gh/magpie-community/plugins@main/registry.json" {
		t.Fatalf("switch on: asked %q", asked)
	}
	// a request with a credential stays official
	asked = nil
	req, _ := http.NewRequest("GET", "https://registry.npmjs.org/pkg", nil)
	req.Header.Set("Authorization", "Bearer x")
	res, err := DoFaithful(c, req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if strings.Join(asked, " ") != "https://registry.npmjs.org/pkg" {
		t.Fatalf("with a credential: asked %q", asked)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The masking models' files, pinned by sha256, have hf-mirror after
// Hugging Face with the switch off and no MAGPIE_MIRRORS (hh0592821 on
// Discord: huggingface.co poisoned in China); before it with the switch
// on, once. Nothing else of Hugging Face's has.
func TestModelMirrorWithoutTheSwitch(t *testing.T) {
	t.Setenv("MAGPIE_MIRRORS", "")
	raw := "https://huggingface.co/QuantumNous/astrlink-guard/resolve/7216b6a/int8/tokenizer.json"
	mirror := "https://hf-mirror.com/QuantumNous/astrlink-guard/resolve/7216b6a/int8/tokenizer.json"
	if got := strings.Join(URLs(raw), " "); got != raw+" "+mirror {
		t.Fatalf("switch off: URLs = %q", got)
	}
	for _, other := range []string{
		"https://huggingface.co/someone/else/resolve/main/x.gguf",
		"https://huggingface.co/QuantumNous/astrlink-guard/resolve/7216b6a/x?dl=1",
		"http://huggingface.co/QuantumNous/astrlink-guard/resolve/7216b6a/x",
	} {
		if got := URLs(other); len(got) != 1 {
			t.Errorf("URLs(%s) = %q", other, got)
		}
	}
	chinaOn(t)
	if got := strings.Join(URLs(raw), " "); got != mirror+" "+raw {
		t.Fatalf("switch on: URLs = %q", got)
	}
}

// Hugging Face's address failing as it does where it is poisoned, the
// file is asked of hf-mirror.
func TestModelFallsToTheMirror(t *testing.T) {
	t.Setenv("MAGPIE_MIRRORS", "")
	var asked atomic.Int32
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		io.WriteString(w, "model")
	}))
	defer mirror.Close()
	c := mirror.Client()
	c.Transport = rewrite{base: c.Transport, to: mirror.URL}
	req, _ := http.NewRequest(http.MethodGet, "https://huggingface.co/QuantumNous/astrlink-guard/resolve/7216b6a/int8/tokenizer.json", nil)
	resp, err := Do(c, req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if b, _ := io.ReadAll(resp.Body); string(b) != "model" || asked.Load() != 1 {
		t.Fatalf("got %q, mirror asked %d", b, asked.Load())
	}
}

// rewrite fails huggingface.co with the certificate hh0592821 saw, and
// sends hf-mirror.com's requests to to.
type rewrite struct {
	base http.RoundTripper
	to   string
}

func (rw rewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	switch r.URL.Host {
	case "huggingface.co":
		return nil, errors.New("tls: failed to verify certificate: x509: certificate is valid for *.facebook.com, facebook.com, not huggingface.co")
	case "hf-mirror.com":
		u, _ := url.Parse(rw.to + r.URL.Path)
		r = r.Clone(r.Context())
		r.URL, r.Host = u, ""
		return rw.base.RoundTrip(r)
	}
	return nil, errors.New("no such host " + r.URL.Host)
}
