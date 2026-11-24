package provider

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sigv4"
)

// #7687: Bedrock with an AWS access key in place of its API key. Its
// requests are signed with SigV4 as botocore signs them; a key, when there
// is one, wins.

// botocore 1.42.45's SigV4Auth, for the access key in sigv4_test.go, at
// 2026-11-24T20:35:19Z: the chat completions request below.
const botoChat = "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261124/eu-west-1/bedrock/aws4_request, SignedHeaders=host;x-amz-date;x-amz-security-token, Signature=9e43ae46521e59b3521125dc5ed327d424d6cba8f6af769466dd4ff6da00e824"

func awsProvider(t *testing.T, preset string) Provider {
	t.Helper()
	p, err := FromPreset(preset)
	if err != nil {
		t.Fatal(err)
	}
	p.Key = ""
	p.AccessKeyID, p.SecretAccessKey, p.SessionToken = "AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "FQoGZXIvYXdzEXAMPLETOKEN"
	return p
}

func holdAWSClock(t *testing.T) {
	t.Helper()
	was := awsNow
	awsNow = func() time.Time { return time.Date(2026, 11, 24, 20, 35, 19, 0, time.UTC) }
	t.Cleanup(func() { awsNow = was })
}

func TestSignsWithAccessKey(t *testing.T) {
	keepSecretsHome(t)
	bed := awsProvider(t, "bedrock")
	mantle := awsProvider(t, "bedrock-mantle")
	custom := Provider{ID: "aws", Chat: "https://bedrock-runtime.ap-south-1.amazonaws.com/openai/v1", AccessKeyID: "AK", SecretAccessKey: "SK"}
	withKey := bed
	withKey.Key = "ABSKbedrock"
	noSecret := bed
	noSecret.SecretAccessKey = ""
	volc := Provider{ID: "volcengine", Chat: "https://ark.cn-beijing.volces.com/api/coding/v3", AccessKeyID: "AK", SecretAccessKey: "SK"}
	acct := bed
	acct.Account = &Account{}
	for _, c := range []struct {
		name string
		p    Provider
		want bool
	}{
		{"Bedrock", bed, true},
		{"Mantle", mantle, true},
		{"Bedrock by its address", custom, true},
		{"an API key wins", withKey, false},
		{"no Secret", noSecret, false},
		{"Volcengine's plan key", volc, false},
		{"an account", acct, false},
	} {
		if got := c.p.SignsWithAccessKey(); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// ready without a key, and refused without the Secret
	if !bed.Ready() || !mantle.Ready() || noSecret.Ready() {
		t.Fatalf("ready: %v %v %v", bed.Ready(), mantle.Ready(), noSecret.Ready())
	}
}

func TestBedrockHostScope(t *testing.T) {
	for host, want := range map[string][2]string{
		"bedrock-runtime.eu-west-1.amazonaws.com":          {"eu-west-1", "bedrock"},
		"bedrock-runtime.us-west-2.amazonaws.com:443":      {"us-west-2", "bedrock"},
		"bedrock-runtime-fips.us-east-1.amazonaws.com":     {"us-east-1", "bedrock"},
		"bedrock-runtime.cn-north-1.amazonaws.com.cn":      {"cn-north-1", "bedrock"},
		"bedrock-mantle.ap-southeast-2.api.aws":            {"ap-southeast-2", "bedrock-mantle"},
		"BEDROCK-MANTLE.US-EAST-1.API.AWS":                 {"us-east-1", "bedrock-mantle"},
		"bedrock-runtime.eu-west-1.amazonaws.com.evil.com": {"", ""},
		"bedrock-runtime.amazonaws.com":                    {"", ""},
		"127.0.0.1:8080":                                   {"", ""},
		"bedrock.us-east-1.amazonaws.com":                  {"", ""},
	} {
		if r, s := bedrockHostScope(host); r != want[0] || s != want[1] {
			t.Errorf("%s: %q %q, want %q %q", host, r, s, want[0], want[1])
		}
	}
	// a host that isn't Bedrock's (a proxy, a test's server) is signed for
	// the region of the provider's own addresses
	p := Provider{Anthropic: "https://bedrock-runtime.ap-northeast-1.amazonaws.com/anthropic"}
	if r, s := p.awsScope("127.0.0.1:9"); r != "ap-northeast-1" || s != "bedrock" {
		t.Fatalf("scope: %q %q", r, s)
	}
	m := Provider{Preset: "bedrock-mantle"}
	if r, s := m.awsScope("127.0.0.1:9"); r != "us-east-1" || s != "bedrock-mantle" {
		t.Fatalf("Mantle scope: %q %q", r, s)
	}
}

// Sign gives the request botocore's signature, whichever way its body
// comes: read again through GetBody, given by the caller, or read from a
// body that can't be read twice, which then still sends whole. No API key
// goes with it.
func TestBedrockSignMatchesBotocore(t *testing.T) {
	keepSecretsHome(t)
	holdAWSClock(t)
	p := awsProvider(t, "bedrock")
	p.Chat = "https://bedrock-runtime.eu-west-1.amazonaws.com/openai/v1"
	const body = `{"model":"openai.gpt-oss-120b-1:0","stream":true}`
	url := p.Chat + "/chat/completions"
	for _, c := range []struct {
		name string
		req  func() *http.Request
		arg  []byte
	}{
		{"GetBody", func() *http.Request { r, _ := http.NewRequest("POST", url, strings.NewReader(body)); return r }, nil},
		{"the caller's", func() *http.Request {
			r, _ := http.NewRequest("POST", url, io.NopCloser(strings.NewReader(body)))
			return r
		}, []byte(body)},
		{"read once", func() *http.Request {
			r, _ := http.NewRequest("POST", url, io.NopCloser(bytes.NewBufferString(body)))
			return r
		}, nil},
	} {
		req := c.req()
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("anthropic-beta", "x")
		if err := p.Sign(context.Background(), req, Chat, c.arg); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := req.Header.Get("Authorization"); got != botoChat {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, botoChat)
		}
		if req.Header.Get("X-Amz-Security-Token") != p.SessionToken || req.Header.Get("x-api-key") != "" {
			t.Errorf("%s: headers %v", c.name, req.Header)
		}
		sent, _ := io.ReadAll(req.Body)
		if string(sent) != body {
			t.Errorf("%s: sends %q", c.name, sent)
		}
	}

	// Bedrock's own invoke-with-response-stream, a model id's escaped :
	// in its path: escaped again, as botocore signs it
	inv := awsProvider(t, "bedrock")
	req, _ := http.NewRequest("POST", "https://bedrock-runtime.us-west-2.amazonaws.com/model/anthropic.claude-v2%3A1/invoke-with-response-stream", strings.NewReader(`{"prompt":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	if err := inv.Sign(context.Background(), req, Anthropic, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := req.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261124/us-west-2/bedrock/aws4_request, SignedHeaders=host;x-amz-date;x-amz-security-token, Signature=2d4120d69f2a9f83f954ba680be35bf8a351420d792d65159e946e9297ee2e42"; got != want {
		t.Errorf("invoke-with-response-stream:\n got %s\nwant %s", got, want)
	}

	// with a Bedrock API key, that is sent, and nothing is signed
	p.Key = "ABSKbedrock"
	req, _ = http.NewRequest("POST", url, strings.NewReader(body))
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer ABSKbedrock" || req.Header.Get("X-Amz-Date") != "" {
		t.Fatalf("with a key: %v", req.Header)
	}
}

// The session token, which no form sends back, stays through a sync of the
// same access key from a magpie that had none, and goes with another key.
func TestKeepSecretsSessionToken(t *testing.T) {
	here := Provider{ID: "bedrock", AccessKeyID: "ASIA1", SecretAccessKey: "SK1", SessionToken: "tok"}
	if got := KeepSecrets(Provider{ID: "bedrock", AccessKeyID: "ASIA1", SecretAccessKey: "SK1"}, here, false); got.SessionToken != "tok" {
		t.Fatalf("same key: %q", got.SessionToken)
	}
	if got := KeepSecrets(Provider{ID: "bedrock", AccessKeyID: "ASIA2", SecretAccessKey: "SK2"}, here, false); got.SessionToken != "" {
		t.Fatalf("another key took this one's token: %q", got.SessionToken)
	}
	if got := KeepSecrets(Provider{ID: "bedrock"}, here, true); got.AccessKeyID != "ASIA1" || got.SecretAccessKey != "SK1" || got.SessionToken != "tok" {
		t.Fatalf("without keys: %+v", got)
	}
}

// awsCheck is what a Bedrock server does with a request: signs it again
// from what arrived (its host, path, X-Amz-* headers and the bytes of its
// body) at the time it names, and compares.
func awsCheck(r *http.Request, body []byte, id, secret, region, service string) string {
	at, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		return "no X-Amz-Date"
	}
	again, _ := http.NewRequest(r.Method, "http://"+r.Host+r.URL.RequestURI(), nil)
	again.Header.Set("X-Amz-Security-Token", r.Header.Get("X-Amz-Security-Token"))
	sigv4.Signer{ID: id, Secret: secret, Token: r.Header.Get("X-Amz-Security-Token"), Region: region, Service: service, Few: true, Twice: true}.
		Sign(again, sigv4.Hash(body), at)
	if got, want := r.Header.Get("Authorization"), again.Header.Get("Authorization"); got != want {
		return "signature " + got + ", want " + want
	}
	return ""
}

// Mantle's model list, the one Bedrock list there is, is signed for
// bedrock-mantle with the access key.
func TestMantleListSigned(t *testing.T) {
	keepSecretsHome(t)
	var mu sync.Mutex
	var bad []string
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		asked++
		if e := awsCheck(r, body, "AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "us-east-1", "bedrock-mantle"); e != "" {
			bad = append(bad, r.URL.Path+": "+e)
		}
		if r.Header.Get("x-api-key") != "" || strings.HasPrefix(r.Header.Get("Authorization"), "Bearer") {
			bad = append(bad, r.URL.Path+": a key was sent")
		}
		mu.Unlock()
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"id":"openai.gpt-oss-120b","object":"model"}]}`)
	}))
	t.Cleanup(srv.Close)
	p := awsProvider(t, "bedrock-mantle")
	p.Chat, p.Responses, p.Anthropic = srv.URL+"/v1", srv.URL+"/v1", srv.URL+"/anthropic"
	ms, err := p.List(context.Background())
	if err != nil || len(ms) == 0 || ms[0].ID != "openai.gpt-oss-120b" {
		t.Fatalf("list: %+v %v", ms, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if asked == 0 || len(bad) > 0 {
		t.Fatalf("asked %d: %v", asked, bad)
	}
}
