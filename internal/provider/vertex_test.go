package provider

import (
	"bytes"
	"cmp"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// vertexHome gives a test a home of its own with no Google credentials in
// it, neither GOOGLE_APPLICATION_CREDENTIALS nor CLOUDSDK_CONFIG set, and
// no Vertex AI token kept from another test.
func vertexHome(t *testing.T) string {
	t.Helper()
	isolate(t)
	h := t.TempDir()
	testenv.SetHome(t, h)
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(h, "AppData", "Local"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(h, ".local", "share"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(h, ".local", "state"))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("CLOUDSDK_CONFIG", "")
	forgetVertexTokens()
	t.Cleanup(forgetVertexTokens)
	return h
}

// gcloudCredentials is where `gcloud auth application-default login`
// writes a person's credentials for home: ~/.config/gcloud, or
// %APPDATA%\gcloud on Windows.
func gcloudCredentials(home string) string {
	dir := filepath.Join(home, ".config", "gcloud")
	if runtime.GOOS == "windows" {
		dir = filepath.Join(os.Getenv("APPDATA"), "gcloud")
	}
	return filepath.Join(dir, "application_default_credentials.json")
}

// vertexGoogle is Google as a Vertex AI provider meets it: the token
// endpoint, IAM Credentials' generateAccessToken and a project's
// generateContent. It keeps every request each was sent.
type vertexGoogle struct {
	url  string
	quit chan struct{} // closed as the test ends, for a request still held
	got  chan struct{} // told of a token request held
	mu   sync.Mutex
	// set before the request it is for
	answers vertexAnswers
	// what each was sent
	tokens, iam, vertex []vertexCall
}

// vertexAnswers are how vertexGoogle answers. The token endpoint mints
// ya29.minted-<n> lasting expiresIn seconds (3599 when 0) after slow, or
// answers status and reply when status is set; a request is held
// unanswered while held is, and answered once gate is closed when that is
// set. IAM Credentials mints ya29.impersonated-<n> lasting iamLife (an hour
// when 0), or answers iamStatus and iamReply.
type vertexAnswers struct {
	expiresIn int
	slow      time.Duration
	status    int
	reply     string
	held      bool
	gate      chan struct{}
	iamLife   time.Duration
	iamStatus int
	iamReply  string
}

// vertexCall is a request vertexGoogle was sent.
type vertexCall struct {
	path   string // as it was sent, escaped
	header http.Header
	body   string
}

// newVertexGoogle serves a vertexGoogle on loopback, Vertex AI's root,
// Google's token endpoint and IAM Credentials pointed at it until the test
// ends.
func newVertexGoogle(t *testing.T) *vertexGoogle {
	t.Helper()
	f := &vertexGoogle{quit: make(chan struct{}), got: make(chan struct{}, 1)}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(f.quit) }) // before Close, which waits for a held request
	f.url = srv.URL
	t.Cleanup(VertexForTest(srv.URL+"/vertex", srv.URL+"/token", srv.URL+"/iam/v1"))
	return f
}

func (f *vertexGoogle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c := vertexCall{r.URL.EscapedPath(), r.Header.Clone(), string(b)}
	token, iam, vertex := r.URL.Path == "/token", strings.HasPrefix(r.URL.Path, "/iam/v1/"), strings.HasPrefix(r.URL.Path, "/vertex/")
	f.mu.Lock()
	n := 0
	switch {
	case token:
		f.tokens = append(f.tokens, c)
		n = len(f.tokens)
	case iam:
		f.iam = append(f.iam, c)
		n = len(f.iam)
	case vertex:
		f.vertex = append(f.vertex, c)
	}
	a := f.answers
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if token && a.gate != nil {
		select {
		case f.got <- struct{}{}:
		default:
		}
		select {
		case <-a.gate:
		case <-r.Context().Done():
			return
		case <-f.quit:
			return
		}
	}
	switch {
	case token && a.held:
		select {
		case f.got <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-f.quit:
		}
	case token && a.status != 0:
		time.Sleep(a.slow)
		w.WriteHeader(a.status)
		io.WriteString(w, a.reply)
	case token:
		time.Sleep(a.slow)
		fmt.Fprintf(w, `{"access_token":"ya29.minted-%d","expires_in":%d,"scope":"https://www.googleapis.com/auth/cloud-platform","token_type":"Bearer"}`, n, cmp.Or(a.expiresIn, 3599))
	case iam && a.iamStatus != 0:
		w.WriteHeader(a.iamStatus)
		io.WriteString(w, a.iamReply)
	case iam:
		fmt.Fprintf(w, `{"accessToken":"ya29.impersonated-%d","expireTime":%q}`, n, time.Now().Add(cmp.Or(a.iamLife, time.Hour)).UTC().Format(time.RFC3339))
	case vertex && strings.Contains(r.URL.Path, "/models/gemini-9-pro:"):
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, "{\"error\":{\"code\":404,\"message\":\"Publisher Model `projects/acme-vertex/locations/global/publishers/google/models/gemini-9-pro` was not found or your project does not have access to it. Please ensure you are using a valid model version. For more information, see: https://cloud.google.com/vertex-ai/generative-ai/docs/learn/model-versions\",\"status\":\"NOT_FOUND\"}}")
	case vertex:
		io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"Hi"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2},"modelVersion":"gemini-3.8-flash"}`)
	default:
		http.NotFound(w, r)
	}
}

func (f *vertexGoogle) tokenCalls() []vertexCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.tokens)
}

func (f *vertexGoogle) iamCalls() []vertexCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.iam)
}

func (f *vertexGoogle) vertexCalls() []vertexCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.vertex)
}

// vertexUser is a person's credentials as `gcloud auth application-default
// login` writes them.
func vertexUser(refresh string) map[string]any {
	return map[string]any{
		"account":          "",
		"client_id":        "1234567890-vertextest.apps.googleusercontent.com",
		"client_secret":    "vertex-test-secret",
		"quota_project_id": "acme-vertex",
		"refresh_token":    refresh,
		"type":             "authorized_user",
		"universe_domain":  "googleapis.com",
	}
}

// vertexServiceKey is a service account's key file as the Cloud console
// gives it.
func vertexServiceKey(email, keyID, key string) map[string]any {
	return map[string]any{
		"type":                        "service_account",
		"project_id":                  "acme-vertex",
		"private_key_id":              keyID,
		"private_key":                 key,
		"client_email":                email,
		"client_id":                   "104857600123456789012",
		"auth_uri":                    "https://accounts.google.com/o/oauth2/auth",
		"token_uri":                   "https://oauth2.googleapis.com/token",
		"auth_provider_x509_cert_url": "https://www.googleapis.com/oauth2/v1/certs",
		"client_x509_cert_url":        "https://www.googleapis.com/robot/v1/metadata/x509/" + url.PathEscape(email),
		"universe_domain":             "googleapis.com",
	}
}

// vertexImpersonation is the file `gcloud auth application-default login
// --impersonate-service-account` writes: source's token is traded for one
// of the service account the URL names.
func vertexImpersonation(at string, delegates []string, source any) map[string]any {
	return map[string]any{
		"delegates":                         delegates,
		"service_account_impersonation_url": at,
		"source_credentials":                source,
		"type":                              "impersonated_service_account",
	}
}

// vertexRSA is a service account's RSA key, made once for the tests.
var vertexRSA = sync.OnceValues(func() (*rsa.PrivateKey, error) { return rsa.GenerateKey(rand.Reader, 2048) })

// vertexAt is a Vertex AI provider at the project acme-vertex in loc,
// signed with the credentials in file ("" finds them as Google's libraries
// do).
func vertexAt(loc, file string) Provider {
	return Provider{ID: "vertex-prod", Name: "Vertex prod", Preset: VertexPreset,
		Vertex: &Vertex{Project: "acme-vertex", Location: loc, Credentials: file}}
}

// signVertex signs a request for model at p's address, as the gateway
// sends one, and gives the Authorization it carries.
func signVertex(ctx context.Context, p Provider, model string) (string, error) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Base(Gemini)+VertexPath(model), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	err = p.Sign(ctx, req, Gemini, body)
	return req.Header.Get("Authorization"), err
}

// jwtOf is a JWT's header and claims, once its signature verifies with pub.
func jwtOf(t *testing.T, jwt string, pub *rsa.PublicKey) (head, claims map[string]any) {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("no JWT: %q", jwt)
	}
	var seg [3][]byte
	for i, s := range parts {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatalf("the JWT's part %d isn't unpadded base64url: %v", i, err)
		}
		seg[i] = b
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], seg[2]); err != nil {
		t.Fatalf("the JWT's signature doesn't verify with the key's public half: %v", err)
	}
	if json.Unmarshal(seg[0], &head) != nil || json.Unmarshal(seg[1], &claims) != nil {
		t.Fatalf("the JWT's header or claims aren't JSON: %s %s", seg[0], seg[1])
	}
	return head, claims
}

// Each location is asked at a host of its own: global at
// aiplatform.googleapis.com, the us and eu multi-regions at their rep
// hosts, a region at its own. A project or location no address can be made
// of gives none, so nothing is sent anywhere, and a model typed in by hand
// stays one segment of the path.
func TestVertexAddress(t *testing.T) {
	vertexHome(t)
	for loc, want := range map[string]string{
		"":             "https://aiplatform.googleapis.com/v1/projects/acme-vertex/locations/global",
		"global":       "https://aiplatform.googleapis.com/v1/projects/acme-vertex/locations/global",
		"us":           "https://aiplatform.us.rep.googleapis.com/v1/projects/acme-vertex/locations/us",
		"eu":           "https://aiplatform.eu.rep.googleapis.com/v1/projects/acme-vertex/locations/eu",
		"us-central1":  "https://us-central1-aiplatform.googleapis.com/v1/projects/acme-vertex/locations/us-central1",
		"europe-west4": "https://europe-west4-aiplatform.googleapis.com/v1/projects/acme-vertex/locations/europe-west4",
	} {
		p := vertexAt(loc, "")
		if got := p.vertexBase(); got != want {
			t.Errorf("at %q: %s, want %s", loc, got, want)
		}
		if got := p.Base(Gemini); got != want {
			t.Errorf("at %q generateContent's base is %s, want %s", loc, got, want)
		}
	}
	// a domain-scoped project's id has a dot and a colon in it
	p := vertexAt("global", "")
	p.Vertex.Project = "example.com:acme-vertex"
	if got := p.vertexBase(); got != "https://aiplatform.googleapis.com/v1/projects/example.com:acme-vertex/locations/global" {
		t.Errorf("a domain-scoped project: %s", got)
	}
	for _, v := range []*Vertex{
		nil, {}, {Location: "us-central1"},
		{Project: "Acme-Vertex"}, {Project: "acme-vertex/../other-project"}, {Project: "acme-vertex?alt=sse"},
		{Project: "acme-vertex", Location: "us central1"}, {Project: "acme-vertex", Location: "../global"}, {Project: "acme-vertex", Location: "us-central1/x"},
	} {
		p := Provider{Name: "Vertex prod", Preset: VertexPreset, Vertex: v}
		if got := p.vertexBase(); got != "" || p.Base(Gemini) != "" || len(p.Speaks()) > 0 || p.Ready() {
			t.Errorf("%+v has an address: %q, speaks %v, ready %v", v, got, p.Speaks(), p.Ready())
		}
	}
	if got := VertexPath("gemini-3.1-pro-preview-customtools"); got != "/publishers/google/models/gemini-3.1-pro-preview-customtools:streamGenerateContent?alt=sse" {
		t.Errorf("a model's path: %s", got)
	}
	if got := VertexPath("tuned/gemini 1?x#y"); got != "/publishers/google/models/tuned%2Fgemini%201%3Fx%23y:streamGenerateContent?alt=sse" {
		t.Errorf("a model with a slash, a space, ? and # in it: %s", got)
	}
}

// Save turns away a Vertex AI provider whose requests it couldn't address
// or sign, saying what to give instead, and keeps nothing of it; and one
// given an API key, since Vertex AI is asked with the user's Google
// credentials.
func TestVertexSaveRefuses(t *testing.T) {
	home := vertexHome(t)
	for _, c := range []struct {
		v    *Vertex
		key  string
		keys []KeyAccount
		want string
	}{
		{v: &Vertex{Project: "acme-vertex"}, key: "AIzaSyD-not-a-real-key", want: "Google Vertex AI is asked with your Google credentials, not an API key"},
		{v: &Vertex{Project: "acme-vertex"}, keys: []KeyAccount{{Name: "express", Key: "AIzaSyD-not-a-real-key"}}, want: "Google Vertex AI is asked with your Google credentials, not an API key"},
		{want: "Google Vertex AI needs the id of your Google Cloud project"},
		{v: &Vertex{Location: "us-central1"}, want: "Google Vertex AI needs the id of your Google Cloud project"},
		{v: &Vertex{Project: "acme_vertex"}, want: `"acme_vertex" is no Google Cloud project id`},
		{v: &Vertex{Project: "-acme-vertex"}, want: `"-acme-vertex" is no Google Cloud project id`},
		{v: &Vertex{Project: "acme-vertex/locations"}, want: `"acme-vertex/locations" is no Google Cloud project id`},
		{v: &Vertex{Project: "acme-vertex", Location: "us central1"}, want: `"us central1" is no Vertex AI location: global, us, eu or a region such as us-central1`},
		{v: &Vertex{Project: "acme-vertex", Location: "us-central1/"}, want: `"us-central1/" is no Vertex AI location: global, us, eu or a region such as us-central1`},
		{v: &Vertex{Project: "acme-vertex", Credentials: "keys/acme-vertex.json"}, want: `give the credentials file's full path, not "keys/acme-vertex.json"`},
		// another user's home, which nothing expands: it would be read
		// under whatever folder magpie was started in
		{v: &Vertex{Project: "acme-vertex", Credentials: "~other/acme-vertex.json"}, want: `give the credentials file's full path, not "~other/acme-vertex.json"`},
		{v: &Vertex{Project: "acme-vertex", Impersonate: "vertex-runner"}, want: `"vertex-runner" is no service account's email`},
		{v: &Vertex{Project: "acme-vertex", Impersonate: "vertex-runner@acme vertex.iam.gserviceaccount.com"}, want: `"vertex-runner@acme vertex.iam.gserviceaccount.com" is no service account's email`},
	} {
		err := Save(Provider{ID: "vertex-prod", Name: "Vertex prod", Preset: VertexPreset, Vertex: c.v, Key: c.key, Keys: c.keys})
		if err == nil || err.Error() != c.want {
			t.Errorf("%+v, key %q %v: %v, want %q", c.v, c.key, c.keys, err, c.want)
		}
	}
	if _, err := os.Stat(Path()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a provider refused was kept: %v", err)
	}
	// a file's full path, or one under the home, is taken
	for _, cred := range []string{filepath.Join(home, "keys", "acme-vertex.json"), "~/keys/acme-vertex.json"} {
		if err := Save(Provider{ID: "vertex-prod", Name: "Vertex prod", Preset: VertexPreset, Vertex: &Vertex{Project: "acme-vertex", Credentials: cred}}); err != nil {
			t.Errorf("credentials %s: %v", cred, err)
		}
	}
}

// A key put in a Vertex AI provider's entry by hand stands in for
// nothing: the provider as found has none, isn't ready without a project,
// signs with its Google token, and saves without it (switched off, here).
// A Provider made with one, as the app's typed Refresh makes it, still
// signs with the token.
func TestVertexKeyKeptDropped(t *testing.T) {
	home := vertexHome(t)
	newVertexGoogle(t)
	writeFile(t, gcloudCredentials(home), vertexUser("1//0g-test-refresh"))
	stray := []KeyAccount{{Name: "express", Key: "AQ.Ab8RN6-other"}}
	writeFile(t, Path(), map[string]any{"providers": []Provider{
		{ID: "vertex-prod", Name: "Vertex prod", Preset: VertexPreset, Key: "AQ.Ab8RN6-stray", KeyName: "express", Keys: stray, Vertex: &Vertex{Project: "acme-vertex"}},
		{ID: "vertex-bare", Name: "Vertex bare", Preset: VertexPreset, Key: "AQ.Ab8RN6-stray"},
	}})
	p, err := Find("vertex-prod")
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != "" || p.KeyName != "" || p.Keys != nil || !p.On() {
		t.Fatalf("found with key %q (%q), keys %v, on %v", p.Key, p.KeyName, p.Keys, p.On())
	}
	if bare, err := Find("vertex-bare"); err != nil || bare.Ready() {
		t.Fatalf("with a key and no project: ready %v (%v)", bare.Ready(), err)
	}
	if auth, err := signVertex(context.Background(), *p, "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.minted-1" {
		t.Fatalf("signed with %q: %v", auth, err)
	}
	if err := SetOff("vertex-prod", true); err != nil {
		t.Fatalf("switched off: %v", err)
	}
	var f struct {
		Providers []map[string]any `json:"providers"`
	}
	if b, err := os.ReadFile(Path()); err != nil || json.Unmarshal(b, &f) != nil || f.Providers[0]["id"] != "vertex-prod" || f.Providers[0]["off"] != true {
		t.Fatalf("providers.json: %v (%v)", f.Providers, err)
	}
	for _, k := range []string{"key", "keyName", "keys"} {
		if v, ok := f.Providers[0][k]; ok && v != "" {
			t.Errorf("saved with its %s %v", k, v)
		}
	}
	typed := vertexAt("global", "")
	typed.Key = "AQ.Ab8RN6-typed"
	if auth, err := signVertex(context.Background(), typed, "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.minted-1" {
		t.Fatalf("made with a key, signed with %q: %v", auth, err)
	}
	if typed.Vertex = nil; typed.Ready() {
		t.Fatal("a key made it ready with no project")
	}
}

// A Vertex AI provider is kept trimmed, its project, location and account
// lowercased, at global when no location is given, and without the base
// URLs a form or an import may have carried: its requests go to its
// project's address alone. Another preset's provider keeps no Vertex.
func TestVertexSaveNormalizes(t *testing.T) {
	vertexHome(t)
	key := filepath.Join(t.TempDir(), "Keys", "acme-vertex.json")
	err := Save(Provider{ID: "vertex-prod", Name: "Vertex prod", Preset: VertexPreset,
		Chat: "https://relay.example.com/v1", Responses: "https://relay.example.com/v1",
		Anthropic: "https://relay.example.com", Decide: "https://relay.example.com/v1",
		Vertex: &Vertex{Project: " Acme-Vertex ", Credentials: " " + key + "\n", Impersonate: " Vertex-Runner@Acme-Vertex.iam.gserviceaccount.com "}})
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk-relay", Vertex: &Vertex{Project: "acme-vertex"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(b, &f); err != nil || len(f.Providers) != 2 {
		t.Fatalf("providers.json: %s (%v)", b, err)
	}
	want := map[string]any{"project": "acme-vertex", "location": "global", "credentials": key, "impersonate": "vertex-runner@acme-vertex.iam.gserviceaccount.com"}
	if got := f.Providers[0]["vertex"]; !reflect.DeepEqual(got, want) {
		t.Errorf("kept as %v, want %v", got, want)
	}
	for _, k := range []string{"chat", "responses", "anthropic", "decide"} {
		if u, ok := f.Providers[0][k]; ok {
			t.Errorf("a Vertex AI provider kept its %s URL %v", k, u)
		}
	}
	if v, ok := f.Providers[1]["vertex"]; ok {
		t.Errorf("an OpenAI-compatible provider kept a Vertex: %v", v)
	}
	if v := normalize(Provider{Preset: VertexPreset, Vertex: &Vertex{Project: " ", Location: " "}}).Vertex; v != nil {
		t.Errorf("nothing given is kept as %+v", v)
	}
	if v := normalize(Provider{Preset: VertexPreset, Vertex: &Vertex{Project: "acme-vertex", Location: " Europe-West4 "}}).Vertex; v == nil || v.Location != "europe-west4" {
		t.Errorf("a region is kept as %+v", v)
	}
}

// A saved Vertex AI provider is one of the user's own, though it has no
// base URL: listed and found rather than taken for a subscription's model
// picks, ready and on without a key, speaking generateContent at its
// project's address, and its id the user's, so a plugin's provider of the
// same id takes another. Its models are the ones its location serves,
// given without asking Vertex AI for a list.
func TestVertexProviderListed(t *testing.T) {
	vertexHome(t)
	if err := Save(Provider{ID: "vertex-prod", Name: "Vertex prod", Preset: VertexPreset, Vertex: &Vertex{Project: "acme-vertex"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(All(), "vertex-prod"); !ok {
		t.Fatal("not listed")
	}
	p, err := Find("vertex-prod")
	if err != nil {
		t.Fatal(err)
	}
	base := "https://aiplatform.googleapis.com/v1/projects/acme-vertex/locations/global"
	if !p.Ready() || !p.On() || !slices.Equal(p.Speaks(), []Protocol{Gemini}) || p.Base(Gemini) != base {
		t.Fatalf("ready %v, on %v, speaks %v at %q", p.Ready(), p.On(), p.Speaks(), p.Base(Gemini))
	}
	if !customIDs()["vertex-prod"] || PluginID("vertex-prod") != "vertex-prod-plugin" {
		t.Fatalf("not the user's own: their ids %v, a plugin's %s", customIDs(), PluginID("vertex-prod"))
	}
	global := Preset(VertexPreset).Models
	for _, loc := range []string{"global", "us", "eu", "us-central1", "europe-west4"} {
		if err := Save(Provider{ID: "vertex-prod", Name: "Vertex prod", Preset: VertexPreset, Vertex: &Vertex{Project: "acme-vertex", Location: loc}}); err != nil {
			t.Fatal(err)
		}
		p, err := Find("vertex-prod")
		if err != nil {
			t.Fatal(err)
		}
		fetched, err := p.Fetch(context.Background())
		if err != nil {
			t.Fatalf("at %s: %v", loc, err)
		}
		got := modelIDs(p.Available())
		if !slices.Equal(modelIDs(fetched), got) || !slices.Equal(modelIDs(p.Exposed()), got) {
			t.Errorf("at %s: fetched %v, available %v, exposed %v", loc, modelIDs(fetched), got, modelIDs(p.Exposed()))
		}
		var wrong func(string) bool
		switch loc {
		case "global":
			wrong = func(m string) bool { return !slices.Contains(global, m) }
		case "us", "eu":
			// Gemini 3's Flash models, of those global serves
			wrong = func(m string) bool {
				return !strings.HasPrefix(m, "gemini-3") || !strings.Contains(m, "-flash") || strings.Contains(m, "preview") || !slices.Contains(global, m)
			}
		default:
			wrong = func(m string) bool { return !strings.HasPrefix(m, "gemini-2.5-") }
		}
		if len(got) == 0 || slices.ContainsFunc(got, wrong) || loc == "global" && len(got) != len(global) {
			t.Errorf("at %s: %v", loc, got)
		}
	}
}

// A Vertex AI provider's project is each copy's own: a caller changing the
// one it found leaves the list a request holds as it was, a copy the user
// makes is at the same project and location until they say otherwise, and
// a renamed one keeps them.
func TestVertexCopies(t *testing.T) {
	vertexHome(t)
	if err := Save(Provider{ID: "vertex-prod", Name: "Vertex prod", Preset: VertexPreset, Vertex: &Vertex{Project: "acme-vertex", Location: "us"}}); err != nil {
		t.Fatal(err)
	}
	release := Hold()
	a, err := Find("vertex-prod")
	if err != nil {
		t.Fatal(err)
	}
	a.Vertex.Project = "changed"
	b, err := Find("vertex-prod")
	release()
	if err != nil || b.Vertex.Project != "acme-vertex" {
		t.Fatalf("a caller's change reached the held list: %+v (%v)", b, err)
	}
	id, err := AddCopy(Provider{Name: "Vertex prod copy", Preset: VertexPreset}, "vertex-prod")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Find(id)
	if err != nil || c.Vertex == nil || *c.Vertex != (Vertex{Project: "acme-vertex", Location: "us"}) {
		t.Fatalf("the copy %s: %+v (%v)", id, c, err)
	}
	if err := Rename("vertex-prod", "vertex-main"); err != nil {
		t.Fatal(err)
	}
	r, err := Find("vertex-main")
	if err != nil || r.Base(Gemini) != "https://aiplatform.us.rep.googleapis.com/v1/projects/acme-vertex/locations/us" {
		t.Fatalf("renamed: %+v (%v)", r, err)
	}
}

// With a person's credentials, the ones `gcloud auth application-default
// login` writes, the refresh token is traded at Google's token endpoint for
// an access token the request carries. The token is kept: the next request
// is signed without asking again.
func TestVertexUserCredentials(t *testing.T) {
	home := vertexHome(t)
	f := newVertexGoogle(t)
	writeFile(t, gcloudCredentials(home), vertexUser("1//0g-test-refresh"))
	p := vertexAt("global", "")
	for range 2 {
		if auth, err := signVertex(context.Background(), p, "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.minted-1" {
			t.Fatalf("signed with %q: %v", auth, err)
		}
	}
	calls := f.tokenCalls()
	if len(calls) != 1 {
		t.Fatalf("the token endpoint was asked %d times, want once", len(calls))
	}
	form, err := url.ParseQuery(calls[0].body)
	want := url.Values{"grant_type": {"refresh_token"}, "client_id": {"1234567890-vertextest.apps.googleusercontent.com"},
		"client_secret": {"vertex-test-secret"}, "refresh_token": {"1//0g-test-refresh"}}
	if err != nil || !reflect.DeepEqual(form, want) {
		t.Fatalf("form %v (%v), want %v", form, err, want)
	}
	if h := calls[0].header; h.Get("Content-Type") != "application/x-www-form-urlencoded" || h.Get("Authorization") != "" {
		t.Fatalf("a refresh sent as %q with Authorization %q", h.Get("Content-Type"), h.Get("Authorization"))
	}
}

// Requests signed together while no token is kept wait for the one being
// minted, rather than each minting its own.
func TestVertexTokenMintedOnce(t *testing.T) {
	home := vertexHome(t)
	f := newVertexGoogle(t)
	f.answers.slow = 100 * time.Millisecond
	writeFile(t, gcloudCredentials(home), vertexUser("1//0g-test-refresh"))
	p := vertexAt("global", "")
	auths, errs := make([]string, 16), make([]error, 16)
	var wg sync.WaitGroup
	for i := range auths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			auths[i], errs[i] = signVertex(context.Background(), p, "gemini-3.5-flash")
		}()
	}
	wg.Wait()
	for i := range auths {
		if errs[i] != nil || auths[i] != "Bearer ya29.minted-1" {
			t.Fatalf("request %d signed with %q: %v", i, auths[i], errs[i])
		}
	}
	if n := len(f.tokenCalls()); n != 1 {
		t.Fatalf("%d requests together minted %d tokens, want one", len(auths), n)
	}
}

// A kept token is minted again once it is within five minutes of lapsing,
// and when its credentials file changes (gcloud signed in again): a file of
// another size, or one written at another time.
func TestVertexTokenRenewed(t *testing.T) {
	home := vertexHome(t)
	f := newVertexGoogle(t)
	file := gcloudCredentials(home)
	writeFile(t, file, vertexUser("1//0g-first-sign-in"))
	p := vertexAt("global", "")
	sign := func(want string) {
		t.Helper()
		if auth, err := signVertex(context.Background(), p, "gemini-3.5-flash"); err != nil || auth != "Bearer "+want {
			t.Fatalf("signed with %q (%v), want %s", auth, err, want)
		}
	}
	// five minutes left: each request mints one
	f.answers.expiresIn = 300
	sign("ya29.minted-1")
	sign("ya29.minted-2")
	// six: it is kept
	f.answers.expiresIn = 360
	sign("ya29.minted-3")
	sign("ya29.minted-3")
	// signed in again, with another refresh token
	writeFile(t, file, vertexUser("1//0g-second-sign-in"))
	sign("ya29.minted-4")
	if form, _ := url.ParseQuery(f.tokenCalls()[3].body); form.Get("refresh_token") != "1//0g-second-sign-in" {
		t.Fatalf("minted from %v, not the file as it is now", form)
	}
	sign("ya29.minted-4")
	// the same bytes, written at another time
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	sign("ya29.minted-5")
	sign("ya29.minted-5")
	// signed in again, which Google then refuses: the request fails, and
	// the token still held, of the sign-in before, isn't sent in its place
	f.answers.status, f.answers.reply = http.StatusBadRequest, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`
	writeFile(t, file, vertexUser("1//0g-third-sign-in"))
	if auth, err := signVertex(context.Background(), p, "gemini-3.5-flash"); err == nil || auth != "" || !strings.Contains(err.Error(), "sign in again") {
		t.Fatalf("signed with %q (%v), want the refusal", auth, err)
	}
}

// A mint Google never answers fails within vertexMintWait, even for a
// request with no deadline of its own, and the next request mints anew
// rather than waiting behind it.
func TestVertexMintBounded(t *testing.T) {
	home := vertexHome(t)
	f := newVertexGoogle(t)
	writeFile(t, gcloudCredentials(home), vertexUser("1//0g-test-refresh"))
	wait := vertexMintWait
	vertexMintWait = 200 * time.Millisecond
	t.Cleanup(func() { vertexMintWait = wait })
	p := vertexAt("global", "")
	f.mu.Lock()
	f.answers.held = true
	f.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second) // the test's own bound, not the mint's
	defer cancel()
	start := time.Now()
	if _, err := signVertex(ctx, p, "gemini-3.5-flash"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a mint never answered: %v", err)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("a mint never answered was waited on for %s", d)
	}
	f.mu.Lock() // the request held may still be answering
	f.answers.held = false
	f.mu.Unlock()
	if auth, err := signVertex(context.Background(), p, "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.minted-2" {
		t.Fatalf("after it, signed with %q (%v)", auth, err)
	}
}

// What Google's answers lack is said before anything is sent: a person's
// credentials with no client, a token endpoint's 200 with no token, IAM
// Credentials' with none. Nothing is signed.
func TestVertexAnswersLacking(t *testing.T) {
	home := vertexHome(t)
	f := newVertexGoogle(t)
	file := gcloudCredentials(home)
	creds := vertexUser("1//0g-test-refresh")
	delete(creds, "client_id")
	writeFile(t, file, creds)
	p := vertexAt("global", "")
	if auth, err := signVertex(context.Background(), p, "gemini-3.5-flash"); err == nil || auth != "" || len(f.tokenCalls()) != 0 {
		t.Fatalf("credentials with no client_id: signed with %q (%v) after %d token requests", auth, err, len(f.tokenCalls()))
	}
	writeFile(t, file, vertexUser("1//0g-test-refresh"))
	f.answers.status, f.answers.reply = http.StatusOK, `{"expires_in":3599,"token_type":"Bearer"}`
	if auth, err := signVertex(context.Background(), p, "gemini-3.5-flash"); err == nil || auth != "" || !strings.Contains(err.Error(), "Google gave no access token") {
		t.Fatalf("a 200 with no token: signed with %q (%v)", auth, err)
	}
	f.answers.status = 0
	p.Vertex.Impersonate = "vertex-runner@acme-vertex.iam.gserviceaccount.com"
	f.answers.iamStatus, f.answers.iamReply = http.StatusOK, `{"expireTime":"2099-01-01T00:00:00Z"}`
	if auth, err := signVertex(context.Background(), p, "gemini-3.5-flash"); err == nil || auth != "" || !strings.Contains(err.Error(), "IAM Credentials gave no access token") {
		t.Fatalf("IAM's 200 with no token: signed with %q (%v)", auth, err)
	}
}

// A person's credentials Google refuses (revoked, or past the
// organization's session length) say to sign in again. Google failing
// (5xx), or asking to be asked again later (408, 429, which Google's own
// auth library retries), doesn't: a new sign-in changes nothing there. The
// request isn't signed, and a refusal isn't kept: the next request asks
// again.
func TestVertexRefusedSignIn(t *testing.T) {
	home := vertexHome(t)
	f := newVertexGoogle(t)
	file := gcloudCredentials(home)
	writeFile(t, file, vertexUser("1//0g-revoked"))
	p := vertexAt("global", "")
	for _, c := range []struct {
		status      int
		reply, want string
		again       bool
	}{
		{400, `{"error":"invalid_grant","error_description":"reauth related error (invalid_rapt)","error_uri":"https://support.google.com/a/answer/9368756","error_subtype":"invalid_rapt"}`,
			"the sign-in was refused (400): reauth related error (invalid_rapt)", true},
		{400, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`, "the sign-in was refused (400): Token has been expired or revoked.", true},
		{401, `{"error":"invalid_client","error_description":"Unauthorized"}`, "the sign-in was refused (401): Unauthorized", true},
		{500, `{"error":"internal_failure","error_description":"Backend Error"}`, "the sign-in was refused (500): Backend Error", false},
		{503, "Service Unavailable", "the sign-in was refused (503): Service Unavailable", false},
		{408, "", "the sign-in was refused (408): Request Timeout", false},
		{429, "", "the sign-in was refused (429): Too Many Requests", false},
	} {
		f.answers.status, f.answers.reply = c.status, c.reply
		auth, err := signVertex(context.Background(), p, "gemini-3.5-flash")
		if err == nil || auth != "" {
			t.Fatalf("%d: signed with %q", c.status, auth)
		}
		msg := err.Error()
		if !strings.Contains(msg, "the Google credentials in "+file+": "+c.want) {
			t.Errorf("%d: %s", c.status, msg)
		}
		if again := strings.Contains(msg, "sign in again with `gcloud auth application-default login`"); again != c.again {
			t.Errorf("%d: says to sign in again %v, want %v: %s", c.status, again, c.again, msg)
		}
	}
	f.answers.status = 0
	if auth, err := signVertex(context.Background(), p, "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.minted-8" {
		t.Fatalf("after the refusals, signed with %q: %v", auth, err)
	}
}

// The advice to sign in again is for the file Google refused: gcloud's
// own Application Default Credentials are written again by gcloud, as
// they were made (impersonating, for a file made so); the file gcloud
// keeps an account's credentials in for other tools, by signing that
// account in again; and any other file, which no gcloud sign-in writes, is
// the user's to make again.
func TestVertexSignInAgainSays(t *testing.T) {
	home := vertexHome(t)
	f := newVertexGoogle(t)
	f.answers.status, f.answers.reply = 400, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`
	const runner = "vertex-runner@acme-vertex.iam.gserviceaccount.com"
	impersonating := vertexImpersonation("https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/"+runner+":generateAccessToken", nil, vertexUser("1//0g-revoked"))
	gcloud := filepath.Dir(gcloudCredentials(home))
	work := filepath.Join(home, "gcloud-work")
	for _, c := range []struct {
		name, file, cloudsdk string
		creds                map[string]any
		want                 string
	}{
		{"gcloud's", gcloudCredentials(home), "", vertexUser("1//0g-revoked"), "sign in again with `gcloud auth application-default login`"},
		{"gcloud's, impersonating", gcloudCredentials(home), "", impersonating, "sign in again with `gcloud auth application-default login --impersonate-service-account=" + runner + "`"},
		{"CLOUDSDK_CONFIG's", filepath.Join(work, "application_default_credentials.json"), work, vertexUser("1//0g-revoked"), "sign in again with `gcloud auth application-default login`"},
		{"an account's, for other tools", filepath.Join(gcloud, "legacy_credentials", "dev@example.com", "adc.json"), "", vertexUser("1//0g-revoked"), "sign in again with `gcloud auth login dev@example.com`"},
		{"gcloud's while CLOUDSDK_CONFIG names another folder", gcloudCredentials(home), work, vertexUser("1//0g-revoked"), "sign in again and write that file anew, or give the provider another credentials file"},
		{"a copy", filepath.Join(home, "keys", "vertex.json"), "", vertexUser("1//0g-revoked"), "sign in again and write that file anew, or give the provider another credentials file"},
		{"a copy, impersonating", filepath.Join(home, "keys", "runner.json"), "", impersonating, "sign in again and write that file anew, or give the provider another credentials file"},
	} {
		t.Setenv("CLOUDSDK_CONFIG", c.cloudsdk)
		writeFile(t, c.file, c.creds)
		_, err := signVertex(context.Background(), vertexAt("global", c.file), "gemini-3.5-flash")
		want := "the Google credentials in " + c.file + ": the sign-in was refused (400): Token has been expired or revoked.; " + c.want
		if err == nil || err.Error() != want {
			t.Errorf("%s: %v\nwant %s", c.name, err, want)
		}
	}
}

// waitCtx tells waiting when a request begins to wait on another's mint:
// the first thing to ask for its Done.
type waitCtx struct {
	context.Context
	waiting chan<- struct{}
	once    sync.Once
}

func (c *waitCtx) Done() <-chan struct{} {
	c.once.Do(func() { c.waiting <- struct{}{} })
	return c.Context.Done()
}

// Requests that waited through a mint which failed are given its error
// rather than each minting in turn, which would have the last wait as long
// as all the mints before it; a request made afterwards mints again. A
// mint cut short because its own request was given up on fails none of
// the others: the next of them mints.
func TestVertexFailedMintShared(t *testing.T) {
	home := vertexHome(t)
	f := newVertexGoogle(t)
	writeFile(t, gcloudCredentials(home), vertexUser("1//0g-test-refresh"))
	p := vertexAt("global", "")
	gate := make(chan struct{})
	f.answers.status, f.answers.reply, f.answers.gate = 503, "Service Unavailable", gate
	errs := make(chan error, 4)
	sign := func(ctx context.Context) {
		auth, err := signVertex(ctx, p, "gemini-3.5-flash")
		if err == nil {
			err = fmt.Errorf("signed with %q", auth)
		}
		errs <- err
	}
	go sign(context.Background())
	<-f.got // its mint is waiting on Google
	waiting := make(chan struct{}, 3)
	for range 3 {
		go sign(&waitCtx{Context: context.Background(), waiting: waiting})
	}
	for range 3 {
		<-waiting
	}
	close(gate)
	for range 4 {
		if err := <-errs; !strings.Contains(err.Error(), "the sign-in was refused (503): Service Unavailable") {
			t.Errorf("a request: %v", err)
		}
	}
	if n := len(f.tokenCalls()); n != 1 {
		t.Fatalf("4 requests together minted %d times, want once", n)
	}
	f.answers.status, f.answers.gate = 0, nil
	if auth, err := signVertex(context.Background(), p, "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.minted-2" {
		t.Fatalf("after the failure, signed with %q: %v", auth, err)
	}

	// given up on while minting
	forgetVertexTokens()
	gate = make(chan struct{})
	f.answers.gate = gate
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := signVertex(ctx, p, "gemini-3.5-flash")
		first <- err
	}()
	<-f.got
	auth := make(chan string, 1)
	go func() {
		a, err := signVertex(&waitCtx{Context: context.Background(), waiting: waiting}, p, "gemini-3.5-flash")
		if err != nil {
			a = err.Error()
		}
		auth <- a
	}()
	<-waiting
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("the request given up on: %v", err)
	}
	select {
	case <-f.got: // the one waiting mints in its place
	case a := <-auth:
		t.Fatalf("the request waiting was given %q, not a mint of its own", a)
	}
	close(gate)
	if a := <-auth; a != "Bearer ya29.minted-4" {
		t.Fatalf("the request waiting signed with %q, want its own mint", a)
	}
}

// A service account key is traded for a token with a JWT it signs (RFC
// 7523), as Google's libraries do: RS256 under the key's id, from the
// account, for Google Cloud's APIs, addressed to the token endpoint and
// good for an hour. A key in PKCS #1 is read too; one that isn't RSA, or
// isn't a key, is refused before anything is sent.
func TestVertexServiceAccountKey(t *testing.T) {
	vertexHome(t)
	f := newVertexGoogle(t)
	key, err := vertexRSA()
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	pkcs1 := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	const email, keyID = "vertex-caller@acme-vertex.iam.gserviceaccount.com", "6f1c2d3e4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d"
	dir := t.TempDir()
	file := filepath.Join(dir, "acme-vertex-6f1c2d3e4a5b.json")
	writeFile(t, file, vertexServiceKey(email, keyID, pkcs8))
	before := time.Now()
	if auth, err := signVertex(context.Background(), vertexAt("global", file), "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.minted-1" {
		t.Fatalf("signed with %q: %v", auth, err)
	}
	after := time.Now()
	form, err := url.ParseQuery(f.tokenCalls()[0].body)
	if err != nil || len(form) != 2 || form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
		t.Fatalf("form %v (%v)", form, err)
	}
	head, claims := jwtOf(t, form.Get("assertion"), &key.PublicKey)
	if head["alg"] != "RS256" || head["kid"] != keyID {
		t.Errorf("header %v", head)
	}
	iat, _ := claims["iat"].(float64)
	exp, _ := claims["exp"].(float64)
	if claims["iss"] != email || claims["scope"] != "https://www.googleapis.com/auth/cloud-platform" || claims["aud"] != f.url+"/token" {
		t.Errorf("claims %v", claims)
	}
	if exp-iat != 3600 || iat > float64(after.Unix()) || iat < float64(before.Add(-time.Minute).Unix()) {
		t.Errorf("issued at %v, lapses at %v, signed at %v", iat, exp, before.Unix())
	}
	// PKCS #1, with no key id
	old := filepath.Join(dir, "pkcs1.json")
	k := vertexServiceKey(email, "", pkcs1)
	delete(k, "private_key_id")
	writeFile(t, old, k)
	if auth, err := signVertex(context.Background(), vertexAt("global", old), "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.minted-2" {
		t.Fatalf("PKCS #1: signed with %q: %v", auth, err)
	}
	form, _ = url.ParseQuery(f.tokenCalls()[1].body)
	if head, _ := jwtOf(t, form.Get("assertion"), &key.PublicKey); head["alg"] != "RS256" || head["kid"] != nil {
		t.Errorf("PKCS #1: header %v", head)
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalPKCS8PrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		creds map[string]any
		want  string
	}{
		"ec.json":       {vertexServiceKey(email, keyID, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER}))), "their private_key is no RSA key"},
		"no-pem.json":   {vertexServiceKey(email, keyID, base64.StdEncoding.EncodeToString(der)), "their private_key is no PEM key"},
		"no-email.json": {vertexServiceKey("", keyID, pkcs8), "their client_email is missing"},
	} {
		file := filepath.Join(dir, name)
		writeFile(t, file, c.creds)
		_, err := signVertex(context.Background(), vertexAt("global", file), "gemini-3.5-flash")
		if err == nil || err.Error() != "the Google credentials in "+file+": "+c.want {
			t.Errorf("%s: %v, want %s", name, err, c.want)
		}
	}
	if n := len(f.tokenCalls()); n != 2 {
		t.Fatalf("keys refused were sent: %d token requests", n)
	}
}

// With a service account to act as, the credentials' token is traded at
// IAM Credentials for one of that account's, as gcloud's
// --impersonate-service-account does, and requests are signed with it
// until it nearly lapses by IAM's expireTime, not the credentials' own. A
// refusal of the credentials acting as the account names the role they
// need on it.
func TestVertexImpersonate(t *testing.T) {
	vertexHome(t)
	f := newVertexGoogle(t)
	file := filepath.Join(t.TempDir(), "application_default_credentials.json")
	writeFile(t, file, vertexUser("1//0g-test-refresh"))
	const runner = "vertex-runner@acme-vertex.iam.gserviceaccount.com"
	p := vertexAt("global", file)
	p.Vertex.Impersonate = runner
	sign := func(p Provider, want string) {
		t.Helper()
		if auth, err := signVertex(context.Background(), p, "gemini-3.5-flash"); err != nil || auth != "Bearer "+want {
			t.Fatalf("signed with %q (%v), want %s", auth, err, want)
		}
	}
	// IAM's token lapses in four minutes, the credentials' in an hour:
	// each request mints one
	f.answers.iamLife = 4 * time.Minute
	sign(p, "ya29.impersonated-1")
	sign(p, "ya29.impersonated-2")
	f.answers.iamLife = time.Hour
	sign(p, "ya29.impersonated-3")
	sign(p, "ya29.impersonated-3")
	if n, i := len(f.tokenCalls()), len(f.iamCalls()); n != 3 || i != 3 {
		t.Fatalf("%d token and %d IAM requests, want 3 of each", n, i)
	}
	c := f.iamCalls()[0]
	if c.path != "/iam/v1/projects/-/serviceAccounts/"+runner+":generateAccessToken" {
		t.Fatalf("IAM Credentials asked at %s", c.path)
	}
	if c.header.Get("Authorization") != "Bearer ya29.minted-1" || c.header.Get("Content-Type") != "application/json" {
		t.Fatalf("IAM Credentials asked with %q as %q", c.header.Get("Authorization"), c.header.Get("Content-Type"))
	}
	var body map[string]any
	want := map[string]any{"scope": []any{"https://www.googleapis.com/auth/cloud-platform"}, "lifetime": "3600s"}
	if err := json.Unmarshal([]byte(c.body), &body); err != nil || !reflect.DeepEqual(body, want) {
		t.Fatalf("IAM Credentials asked for %s, want %v", c.body, want)
	}
	f.answers.iamStatus = http.StatusForbidden
	f.answers.iamReply = `{"error":{"code":403,"message":"Permission 'iam.serviceAccounts.getAccessToken' denied on resource (or it may not exist).","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"IAM_PERMISSION_DENIED","domain":"iam.googleapis.com","metadata":{"permission":"iam.serviceAccounts.getAccessToken"}}]}}`
	p.Vertex.Impersonate = "vertex-admin@acme-vertex.iam.gserviceaccount.com"
	_, err := signVertex(context.Background(), p, "gemini-3.5-flash")
	if err == nil || !strings.Contains(err.Error(), "impersonating vertex-admin@acme-vertex.iam.gserviceaccount.com: the sign-in was refused (403): Permission 'iam.serviceAccounts.getAccessToken' denied") ||
		!strings.Contains(err.Error(), "roles/iam.serviceAccountTokenCreator") {
		t.Fatalf("403: %v", err)
	}
	f.answers.iamStatus = http.StatusNotFound
	f.answers.iamReply = `{"error":{"code":404,"message":"Not found; Gaia id not found for email vertex-gone@acme-vertex.iam.gserviceaccount.com","status":"NOT_FOUND"}}`
	p.Vertex.Impersonate = "vertex-gone@acme-vertex.iam.gserviceaccount.com"
	_, err = signVertex(context.Background(), p, "gemini-3.5-flash")
	if err == nil || !strings.Contains(err.Error(), "impersonating vertex-gone@acme-vertex.iam.gserviceaccount.com: the sign-in was refused (404): Not found") ||
		strings.Contains(err.Error(), "serviceAccountTokenCreator") {
		t.Fatalf("404: %v", err)
	}
	// an account named with what a path would read as its end, escaped
	f.answers.iamStatus = 0
	p.Vertex.Impersonate = "ops#1?x@acme-vertex.iam.gserviceaccount.com"
	if _, err := signVertex(context.Background(), p, "gemini-3.5-flash"); err != nil {
		t.Fatal(err)
	}
	if c := f.iamCalls(); c[len(c)-1].path != "/iam/v1/projects/-/serviceAccounts/ops%231%3Fx@acme-vertex.iam.gserviceaccount.com:generateAccessToken" {
		t.Fatalf("IAM Credentials asked at %s", c[len(c)-1].path)
	}
}

// The file `gcloud auth application-default login
// --impersonate-service-account` writes is read as gcloud reads it: its
// source credentials are minted, and IAM Credentials is asked for the
// account its URL names, along its delegates' chain. The token goes to IAM
// Credentials alone, never to the host the file's URL names.
func TestVertexGcloudImpersonation(t *testing.T) {
	vertexHome(t)
	f := newVertexGoogle(t)
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		http.Error(w, "the token came here", http.StatusTeapot)
	}))
	t.Cleanup(other.Close)
	at := func(account string) string {
		return other.URL + "/v1/projects/-/serviceAccounts/" + account + ":generateAccessToken"
	}
	const runner = "vertex-runner@acme-vertex.iam.gserviceaccount.com"
	dir := t.TempDir()
	file := filepath.Join(dir, "application_default_credentials.json")
	writeFile(t, file, vertexImpersonation(at(runner),
		[]string{"first-hop@acme-vertex.iam.gserviceaccount.com", "projects/-/serviceAccounts/second-hop@acme-vertex.iam.gserviceaccount.com"},
		vertexUser("1//0g-test-refresh")))
	if auth, err := signVertex(context.Background(), vertexAt("global", file), "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.impersonated-1" {
		t.Fatalf("signed with %q: %v", auth, err)
	}
	if form, _ := url.ParseQuery(f.tokenCalls()[0].body); form.Get("refresh_token") != "1//0g-test-refresh" {
		t.Fatalf("the source minted with %v", form)
	}
	c := f.iamCalls()[0]
	if c.path != "/iam/v1/projects/-/serviceAccounts/"+runner+":generateAccessToken" || c.header.Get("Authorization") != "Bearer ya29.minted-1" {
		t.Fatalf("IAM Credentials asked at %s with %q", c.path, c.header.Get("Authorization"))
	}
	var body map[string]any
	want := map[string]any{"scope": []any{"https://www.googleapis.com/auth/cloud-platform"}, "lifetime": "3600s",
		"delegates": []any{"projects/-/serviceAccounts/first-hop@acme-vertex.iam.gserviceaccount.com", "projects/-/serviceAccounts/second-hop@acme-vertex.iam.gserviceaccount.com"}}
	if err := json.Unmarshal([]byte(c.body), &body); err != nil || !reflect.DeepEqual(body, want) {
		t.Fatalf("IAM Credentials asked for %s, want %v", c.body, want)
	}
	// an account written escaped is asked for as itself, with no chain
	escaped := filepath.Join(dir, "escaped.json")
	writeFile(t, escaped, vertexImpersonation(at("vertex-runner%40acme-vertex.iam.gserviceaccount.com"), []string{}, vertexUser("1//0g-test-refresh")))
	if auth, err := signVertex(context.Background(), vertexAt("global", escaped), "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.impersonated-2" {
		t.Fatalf("escaped: signed with %q: %v", auth, err)
	}
	if c := f.iamCalls()[1]; c.path != "/iam/v1/projects/-/serviceAccounts/"+runner+":generateAccessToken" || strings.Contains(c.body, "delegates") {
		t.Fatalf("escaped: IAM Credentials asked at %s for %s", c.path, c.body)
	}
	for name, c := range map[string]struct {
		creds map[string]any
		want  string
	}{
		"nested.json":     {vertexImpersonation(at(runner), nil, vertexImpersonation(at(runner), nil, vertexUser("1//0g-test-refresh"))), "an impersonation's source is another impersonation"},
		"no-account.json": {vertexImpersonation(at(""), nil, vertexUser("1//0g-test-refresh")), "their service_account_impersonation_url names no service account"},
		"no-source.json":  {vertexImpersonation(at(runner), nil, nil), "their source_credentials can't be read"},
	} {
		file := filepath.Join(dir, name)
		writeFile(t, file, c.creds)
		_, err := signVertex(context.Background(), vertexAt("global", file), "gemini-3.5-flash")
		if err == nil || err.Error() != "the Google credentials in "+file+": "+c.want {
			t.Errorf("%s: %v, want %s", name, err, c.want)
		}
	}
	if n, i := len(f.tokenCalls()), len(f.iamCalls()); n != 2 || i != 2 {
		t.Fatalf("credentials refused were sent: %d token and %d IAM requests", n, i)
	}
	if n := elsewhere.Load(); n != 0 {
		t.Fatalf("the host the file's URL names was sent %d requests", n)
	}
}

// A token given without when it lapses, by the token endpoint without
// expires_in or by IAM Credentials without expireTime, is taken to last an
// hour: the next request is signed with it, not with one minted again.
func TestVertexUndatedTokenKept(t *testing.T) {
	home := vertexHome(t)
	f := newVertexGoogle(t)
	writeFile(t, gcloudCredentials(home), vertexUser("1//0g-test-refresh"))
	f.answers.status = http.StatusOK
	f.answers.reply = `{"access_token":"ya29.undated","scope":"https://www.googleapis.com/auth/cloud-platform","token_type":"Bearer"}`
	f.answers.iamStatus = http.StatusOK
	f.answers.iamReply = `{"accessToken":"ya29.impersonated-undated"}`
	p, runner := vertexAt("global", ""), vertexAt("global", "")
	runner.Vertex.Impersonate = "vertex-runner@acme-vertex.iam.gserviceaccount.com"
	for range 2 {
		if auth, err := signVertex(context.Background(), p, "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.undated" {
			t.Fatalf("signed with %q: %v", auth, err)
		}
		if auth, err := signVertex(context.Background(), runner, "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.impersonated-undated" {
			t.Fatalf("impersonating, signed with %q: %v", auth, err)
		}
	}
	// a token for the person, and one as the impersonation's source
	if n, i := len(f.tokenCalls()), len(f.iamCalls()); n != 2 || i != 1 {
		t.Fatalf("%d token and %d IAM requests, want 2 and 1", n, i)
	}
}

// Credentials no token is minted from are refused, saying what to use
// instead, before anything is sent: workload identity federation's, a
// sovereign cloud's, a kind magpie doesn't read, a person's with no
// refresh token, and a file that isn't JSON. A missing file is named: the
// provider's own is to be corrected or cleared, and when it is the one
// gcloud would have written, the refusal says how to sign in.
func TestVertexCredentialsRefused(t *testing.T) {
	home := vertexHome(t)
	f := newVertexGoogle(t)
	dir := t.TempDir()
	sovereign := vertexUser("1//0g-test-refresh")
	sovereign["universe_domain"] = "s3nsapis.fr"
	for name, c := range map[string]struct {
		creds map[string]any
		want  string
	}{
		"workload-identity.json": {map[string]any{
			"type":                              "external_account",
			"audience":                          "//iam.googleapis.com/projects/123456789012/locations/global/workloadIdentityPools/github/providers/acme",
			"subject_token_type":                "urn:ietf:params:oauth:token-type:jwt",
			"token_url":                         "https://sts.googleapis.com/v1/token",
			"credential_source":                 map[string]any{"file": "/var/run/secrets/tokens/gcp-ksa/token"},
			"service_account_impersonation_url": "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/vertex-runner@acme-vertex.iam.gserviceaccount.com:generateAccessToken",
			"universe_domain":                   "googleapis.com",
		}, "workload identity federation (external_account) isn't read: use a person's credentials, a service account key, or impersonation"},
		"workforce.json": {map[string]any{
			"type":            "external_account_authorized_user",
			"audience":        "//iam.googleapis.com/locations/global/workforcePools/acme/providers/okta",
			"refresh_token":   "workforce-refresh",
			"token_url":       "https://sts.googleapis.com/v1/oauthtoken",
			"token_info_url":  "https://sts.googleapis.com/v1/introspect",
			"client_id":       "workforce-client",
			"client_secret":   "workforce-secret",
			"universe_domain": "googleapis.com",
		}, "workload identity federation (external_account_authorized_user) isn't read: use a person's credentials, a service account key, or impersonation"},
		"sovereign.json": {sovereign, "they are for s3nsapis.fr; Vertex AI is asked at googleapis.com"},
		"gdch.json": {map[string]any{
			"type": "gdch_service_account", "format_version": "1", "project": "acme", "name": "vertex",
			"private_key_id": "abcdef", "private_key": "-----BEGIN EC PRIVATE KEY-----\n-----END EC PRIVATE KEY-----\n",
			"ca_cert_path": "/etc/ssl/acme-ca.pem", "token_uri": "https://service-identity.acme.example/authenticate",
		}, `they are of type "gdch_service_account"; authorized_user, service_account and impersonated_service_account are read`},
		"no-refresh.json": {vertexUser(""), "they have no refresh token"},
	} {
		file := filepath.Join(dir, name)
		writeFile(t, file, c.creds)
		_, err := signVertex(context.Background(), vertexAt("global", file), "gemini-3.5-flash")
		if err == nil || err.Error() != "the Google credentials in "+file+": "+c.want {
			t.Errorf("%s: %v, want %s", name, err, c.want)
		}
	}
	// the page the key was to be downloaded from, saved in its place
	page := filepath.Join(dir, "key.json")
	if err := os.WriteFile(page, []byte("<!DOCTYPE html><html lang=en><title>Sign in - Google Accounts</title></html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := signVertex(context.Background(), vertexAt("global", page), "gemini-3.5-flash"); err == nil || !strings.HasPrefix(err.Error(), page+" holds no Google credentials: ") {
		t.Errorf("not JSON: %v", err)
	}
	gone := filepath.Join(dir, "gone.json")
	if _, err := signVertex(context.Background(), vertexAt("global", gone), "gemini-3.5-flash"); err == nil ||
		err.Error() != "no Google credentials at "+gone+": correct the provider's credentials file, or clear it to use Application Default Credentials" {
		t.Errorf("a missing file given: %v", err)
	}
	want := "no Google credentials at " + gcloudCredentials(home) + ": sign in with `gcloud auth application-default login`, or give the provider a credentials file"
	if _, err := signVertex(context.Background(), vertexAt("global", ""), "gemini-3.5-flash"); err == nil || err.Error() != want {
		t.Errorf("none where gcloud writes them: %v, want %s", err, want)
	}
	if n, i := len(f.tokenCalls()), len(f.iamCalls()); n != 0 || i != 0 {
		t.Fatalf("credentials refused were sent: %d token and %d IAM requests", n, i)
	}
}

// A file GOOGLE_APPLICATION_CREDENTIALS names that isn't there is refused,
// as Google's own libraries refuse it rather than look further, and the
// refusal names the variable. Signing in with gcloud writes a file that
// isn't read while the variable is set, so that isn't what the refusal says
// to do, gcloud's file there or not.
func TestVertexEnvCredentialsMissing(t *testing.T) {
	home := vertexHome(t)
	f := newVertexGoogle(t)
	writeFile(t, gcloudCredentials(home), vertexUser("1//0g-test-refresh"))
	gone := filepath.Join(t.TempDir(), "acme-vertex.json")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", gone)
	_, err := signVertex(context.Background(), vertexAt("global", ""), "gemini-3.5-flash")
	if err == nil || !strings.Contains(err.Error(), gone) || !strings.Contains(err.Error(), "GOOGLE_APPLICATION_CREDENTIALS") ||
		strings.Contains(err.Error(), "gcloud auth application-default login") {
		t.Errorf("a missing file GOOGLE_APPLICATION_CREDENTIALS names: %v", err)
	}
	if n := len(f.tokenCalls()); n != 0 {
		t.Fatalf("%d tokens minted from another file", n)
	}
}

// A GOOGLE_APPLICATION_CREDENTIALS that isn't a full path names no file
// wherever magpie was started, and gcloud's file, read in its place, may be
// another account's: the person's own, where the variable named a service
// account's key. It is refused by name, and nothing is minted. One that
// starts ~/ is under the home, as the provider's own file is.
func TestVertexEnvCredentialsNotFull(t *testing.T) {
	home := vertexHome(t)
	f := newVertexGoogle(t)
	writeFile(t, gcloudCredentials(home), vertexUser("1//0g-person"))
	writeFile(t, filepath.Join(home, "keys", "vertex.json"), vertexUser("1//0g-from-env"))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join("keys", "vertex.json"))
	_, err := signVertex(context.Background(), vertexAt("global", ""), "gemini-3.5-flash")
	if err == nil || !strings.Contains(err.Error(), "GOOGLE_APPLICATION_CREDENTIALS") || !strings.Contains(err.Error(), "not a full path") {
		t.Errorf("a relative GOOGLE_APPLICATION_CREDENTIALS: %v", err)
	}
	if n := len(f.tokenCalls()); n != 0 {
		t.Fatalf("%d tokens minted from gcloud's file in its place", n)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "~/keys/vertex.json")
	if auth, err := signVertex(context.Background(), vertexAt("global", ""), "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.minted-1" {
		t.Fatalf("signed with %q: %v", auth, err)
	}
	if form, _ := url.ParseQuery(f.tokenCalls()[0].body); form.Get("refresh_token") != "1//0g-from-env" {
		t.Fatalf("minted from %v", form)
	}
}

// The credentials are found where Google's own libraries find them: the
// file given (one under the home as ~/…), else the one
// GOOGLE_APPLICATION_CREDENTIALS names, else the one gcloud keeps in its
// folder, CLOUDSDK_CONFIG's or its own whatever XDG_CONFIG_HOME says.
func TestVertexCredentialsFile(t *testing.T) {
	home := vertexHome(t)
	dir := t.TempDir()
	given, env, cloudsdk := filepath.Join(dir, "acme-vertex.json"), filepath.Join(dir, "env.json"), filepath.Join(dir, "gcloud-work")
	is := func(in, want string) {
		t.Helper()
		if got, err := vertexCredentialsFile(in); err != nil || got != want {
			t.Errorf("%q: %s (%v), want %s", in, got, err, want)
		}
	}
	is(given, given)
	is("~/keys/acme-vertex.json", filepath.Join(home, "keys", "acme-vertex.json"))
	if runtime.GOOS == "windows" {
		is(`~\keys\acme-vertex.json`, filepath.Join(home, "keys", "acme-vertex.json"))
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "elsewhere"))
	is("", gcloudCredentials(home))
	t.Setenv("CLOUDSDK_CONFIG", cloudsdk)
	is("", filepath.Join(cloudsdk, "application_default_credentials.json"))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", env)
	is("", env)
	is(given, given)
	// a ~ that starts a variable's path is the home, as in the given one
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "~/keys/env.json")
	is("", filepath.Join(home, "keys", "env.json"))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("CLOUDSDK_CONFIG", "~/gcloud-work")
	is("", filepath.Join(home, "gcloud-work", "application_default_credentials.json"))
	// and one that isn't a full path names no file wherever magpie was
	// started: it is refused by name, the next place not looked in
	for _, c := range []struct{ name, v string }{{"GOOGLE_APPLICATION_CREDENTIALS", "env.json"}, {"CLOUDSDK_CONFIG", "gcloud-work"}} {
		t.Setenv(c.name, c.v)
		want := c.name + ` is "` + c.v + `", not a full path: give the whole path, unset it, or give the provider a credentials file`
		if got, err := vertexCredentialsFile(""); err == nil || err.Error() != want || got != "" {
			t.Errorf("%s=%s: %s (%v), want %s", c.name, c.v, got, err, want)
		}
		is(given, given)
		t.Setenv(c.name, "")
	}
	// and a file under the home is the one a request is signed with
	f := newVertexGoogle(t)
	writeFile(t, filepath.Join(home, "keys", "acme-vertex.json"), vertexUser("1//0g-from-home"))
	if auth, err := signVertex(context.Background(), vertexAt("global", "~/keys/acme-vertex.json"), "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.minted-1" {
		t.Fatalf("signed with %q: %v", auth, err)
	}
	if form, _ := url.ParseQuery(f.tokenCalls()[0].body); form.Get("refresh_token") != "1//0g-from-home" {
		t.Fatalf("minted from %v", form)
	}
}

// A Google token goes nowhere but the provider's own project and location:
// a request to another project, to a location whose name starts with this
// one's, to the address alone or to another host isn't signed, nor is one
// for a provider with no project, and no token is minted for them. One at
// the address is signed, the user's own headers beside the token.
func TestVertexSignOnlyAtItsAddress(t *testing.T) {
	vertexHome(t)
	f := newVertexGoogle(t)
	file := filepath.Join(t.TempDir(), "application_default_credentials.json")
	writeFile(t, file, vertexUser("1//0g-test-refresh"))
	p := vertexAt("global", file)
	p.Headers = map[string]string{"X-Vertex-AI-LLM-Request-Type": "shared"}
	root := f.url + "/vertex/v1/projects/"
	model := "/publishers/google/models/gemini-3.5-flash:streamGenerateContent?alt=sse"
	refuse := func(p Provider, u string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, u, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		err = p.Sign(context.Background(), req, Gemini, []byte("{}"))
		if want := p.Name + " is asked only at Vertex AI, at its project's address"; err == nil || err.Error() != want || req.Header.Get("Authorization") != "" {
			t.Errorf("%s: %v, signed with %q", u, err, req.Header.Get("Authorization"))
		}
	}
	for _, u := range []string{
		root + "acme-vertex-2/locations/global" + model,
		root + "acme-vertex/locations/global-x" + model,
		root + "acme-vertex/locations/us" + model,
		root + "acme-vertex/locations/global",
		"https://generativelanguage.googleapis.com/v1beta/models/gemini-3.5-flash:streamGenerateContent?alt=sse",
		"https://aiplatform.googleapis.com/v1/projects/acme-vertex/locations/global" + model,
	} {
		refuse(p, u)
	}
	for _, v := range []*Vertex{nil, {Credentials: file}, {Project: "Acme Vertex", Credentials: file}} {
		refuse(Provider{Name: "Vertex dev", Preset: VertexPreset, Vertex: v}, root+"acme-vertex/locations/global"+model)
	}
	if n := len(f.tokenCalls()); n != 0 {
		t.Fatalf("%d tokens minted for requests not signed", n)
	}
	req, _ := http.NewRequest(http.MethodPost, p.Base(Gemini)+VertexPath("gemini-3.5-flash"), strings.NewReader("{}"))
	if err := p.Sign(context.Background(), req, Gemini, []byte("{}")); err != nil || req.Header.Get("Authorization") != "Bearer ya29.minted-1" {
		t.Fatalf("at its address: %v, signed with %q", err, req.Header.Get("Authorization"))
	}
	if h := req.Header["X-Vertex-AI-LLM-Request-Type"]; !slices.Equal(h, []string{"shared"}) || req.Header.Get("x-api-key") != "" || req.Header.Get("x-goog-api-key") != "" {
		t.Fatalf("headers %v", req.Header)
	}
	// a domain-scoped project's address has a dot and a colon in it
	q := vertexAt("global", file)
	q.Vertex.Project = "example.com:acme-vertex"
	if auth, err := signVertex(context.Background(), q, "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.minted-1" {
		t.Fatalf("a domain-scoped project: signed with %q: %v", auth, err)
	}
}

// A Vertex AI provider's test asks generateContent at its project, whole
// rather than streamed, for 16 tokens after as little thinking as the
// model takes: Gemini 3 at low, 2.5 Pro its least, 128 tokens, and 2.5's
// Flash models none. Each of its models can be tested, and both tests reach
// Vertex AI signed with the minted token.
func TestVertexTestRequest(t *testing.T) {
	vertexHome(t)
	f := newVertexGoogle(t)
	file := filepath.Join(t.TempDir(), "application_default_credentials.json")
	writeFile(t, file, vertexUser("1//0g-test-refresh"))
	q := vertexAt("global", file)
	base := f.url + "/vertex/v1/projects/acme-vertex/locations/global"
	low, budget := map[string]any{"thinkingLevel": "low"}, func(n float64) map[string]any { return map[string]any{"thinkingBudget": n} }
	for model, think := range map[string]map[string]any{
		"gemini-3.5-flash":       low,
		"gemini-3.1-pro-preview": low,
		"gemini-3-flash-preview": low,
		"gemini-2.5-pro":         budget(128),
		"Gemini-2.5-Pro":         budget(128),
		"gemini-2.5-flash":       budget(0),
		"gemini-2.5-flash-lite":  budget(0),
		"gemini-2.0-flash-001":   nil,
	} {
		u, body := vertexTest(q, model)
		if u != base+"/publishers/google/models/"+model+":generateContent" {
			t.Errorf("%s asked at %s", model, u)
		}
		var b struct {
			Contents []struct {
				Role  string `json:"role"`
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"contents"`
			Config map[string]any `json:"generationConfig"`
		}
		if err := json.Unmarshal([]byte(body), &b); err != nil || len(b.Contents) != 1 || b.Contents[0].Role != "user" || len(b.Contents[0].Parts) != 1 || b.Contents[0].Parts[0].Text != "hi" {
			t.Errorf("%s: %s (%v)", model, body, err)
		}
		got, _ := b.Config["thinkingConfig"].(map[string]any)
		if b.Config["maxOutputTokens"] != 16.0 || !reflect.DeepEqual(got, think) {
			t.Errorf("%s: %s", model, body)
		}
		if tu, tb := tinyBody(q, Gemini, model); tu != u || tb != body {
			t.Errorf("%s: the probe asks %s %s", model, tu, tb)
		}
	}
	if why := q.ModelTest(); why != "" {
		t.Fatalf("its models can't each be tested: %s", why)
	}
	if err := Save(q); err != nil {
		t.Fatal(err)
	}
	p, err := Find("vertex-prod")
	if err != nil {
		t.Fatal(err)
	}
	rs := p.Test(context.Background())
	if len(rs) != 1 || !rs[0].OK || rs[0].Protocol != Gemini || rs[0].Status != http.StatusOK || rs[0].Model != p.Exposed()[0].ID {
		t.Fatalf("test: %+v", rs)
	}
	rs = p.TestModels(context.Background(), []string{"gemini-2.5-pro", "gemini-9-pro"})
	if !rs[0].OK || rs[1].OK || rs[1].Status != http.StatusNotFound || !strings.Contains(rs[1].Error, "gemini-9-pro") {
		t.Fatalf("models' tests: %+v", rs)
	}
	calls := f.vertexCalls()
	if len(calls) != 3 {
		t.Fatalf("Vertex AI was asked %d times, want 3", len(calls))
	}
	for _, c := range calls {
		if c.header.Get("Authorization") != "Bearer ya29.minted-1" || !strings.HasPrefix(c.path, "/vertex/v1/projects/acme-vertex/locations/global/publishers/google/models/") ||
			!strings.HasSuffix(c.path, ":generateContent") {
			t.Errorf("asked at %s with %q", c.path, c.header.Get("Authorization"))
		}
		if strings.Contains(c.path, "/gemini-2.5-pro:") && !strings.Contains(c.body, `"thinkingBudget":128`) {
			t.Errorf("2.5 Pro tested with %s", c.body)
		}
	}
}

// A request given up on while its token is minted returns at once, not
// after Google answers or the half minute minting may take, and the next
// request mints afresh.
func TestVertexMintCancelled(t *testing.T) {
	vertexHome(t)
	f := newVertexGoogle(t)
	file := filepath.Join(t.TempDir(), "application_default_credentials.json")
	writeFile(t, file, vertexUser("1//0g-test-refresh"))
	p := vertexAt("global", file)
	f.answers.held = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := signVertex(ctx, p, "gemini-3.5-flash")
		done <- err
	}()
	select {
	case <-f.got:
	case <-time.After(10 * time.Second):
		t.Fatal("the token endpoint was never asked")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("given up on: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("still waiting on the token endpoint after the request was given up on")
	}
	f.answers.held = false
	if auth, err := signVertex(context.Background(), p, "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.minted-2" {
		t.Fatalf("the next request signed with %q: %v", auth, err)
	}
}

// A request given up on while it waits for another's token to be minted
// from the same credentials returns at once too, not when the one ahead of
// it is done, which is half a minute while Google's token endpoint hangs.
func TestVertexMintWaitCancelled(t *testing.T) {
	vertexHome(t)
	f := newVertexGoogle(t)
	file := filepath.Join(t.TempDir(), "application_default_credentials.json")
	writeFile(t, file, vertexUser("1//0g-test-refresh"))
	p := vertexAt("global", file)
	f.answers.held = true
	first, stop := context.WithCancel(context.Background())
	defer stop()
	minted := make(chan error, 1)
	go func() {
		_, err := signVertex(first, p, "gemini-3.5-flash")
		minted <- err
	}()
	select {
	case <-f.got:
	case <-time.After(10 * time.Second):
		t.Fatal("the token endpoint was never asked")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := signVertex(ctx, p, "gemini-3.5-flash")
		done <- err
	}()
	time.Sleep(100 * time.Millisecond) // waiting on the first one's token by now
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("given up on: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("a request given up on still waits for another's token to be minted")
		stop()
		<-done
	}
	stop()
	<-minted
}
