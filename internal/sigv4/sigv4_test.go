package sigv4

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// The signer against AWS's own examples: the SigV4 test suite's (service
// "service") and the S3 documentation's, which sign a range, a key with $
// in it, a Date header, and a query with a name alone and one with values.
func TestSigV4Vectors(t *testing.T) {
	const empty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	suite := Signer{ID: "AKIDEXAMPLE", Secret: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", Region: "us-east-1", Service: "service"}
	s3doc := Signer{ID: "AKIAIOSFODNN7EXAMPLE", Secret: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", Region: "us-east-1", Service: "s3"}
	suiteAt := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	botoAt := time.Date(2026, 11, 24, 20, 35, 19, 0, time.UTC)
	const stsToken = "AQoDYXdzEPT//////////wEXAMPLEtc764bNrC9SAPBSM22wDOk4x4HIZ8j4FZTwdQWLWsKWHGBuFqwAeMicRXmxfpSPfIeoIYRqTflfKD8YUuwthAx7mSEI/qkPpKPi/kMcGdQrmGdeehM4IC1NtBmUpp2wUE8phUZampKsburEDy0KPkyQDYwT7WZ0wq5VSXDvp75YU9HFvlRd8Tx6q6fE8YQcHNVXAkiY9q6d+xo0rKwT38xVqr7ZD0u0iPPkUL64lIZbqBAz+scqKmlzm8FDrypNC9Yjc8fPOLn9FX9KSYvKTr4rvx3iSIlTJabIQwj2ICCR/oLxBA=="
	withToken := func(s Signer, tok string) Signer { s.Token = tok; return s }
	few := func(s Signer) Signer { s.Few = true; return s }
	bedrock := func(region, service string) Signer {
		return Signer{ID: "AKIDEXAMPLE", Secret: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", Token: "FQoGZXIvYXdzEXAMPLETOKEN",
			Region: region, Service: service, Few: true, Twice: true}
	}
	s3At := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name    string
		s       Signer
		at      time.Time
		method  string
		url     string
		headers map[string]string
		payload string
		want    string
	}{
		{"get-vanilla", suite, suiteAt, "GET", "https://example.amazonaws.com/", nil, empty,
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"},
		{"get-vanilla-query-order-key-case", suite, suiteAt, "GET", "https://example.amazonaws.com/?Param2=value2&Param1=value1", nil, empty,
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=b97d918cfa904a5beff61c982a1b6f458b799221646efd99d3219ec94cdf2500"},
		{"post-vanilla", suite, suiteAt, "POST", "https://example.amazonaws.com/", nil, empty,
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5da7c1a2acd57cee7505fc6676e4e544621c30862966e37dddb68e92efbe5d6b"},
		{"get-utf8", suite, suiteAt, "GET", "https://example.amazonaws.com/\u1234", nil, empty,
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=8318018e0b0f223aa2bbf98705b62bb787dc9c0e678f255a891fd03141be5d85"},
		{"get-space-normalized", suite, suiteAt, "GET", "https://example.amazonaws.com/example space/", nil, empty,
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=652487583200325589f1fba4c7e578f72c47cb61beeca81406b39ddec1366741"},
		{"get-vanilla-query-unreserved", suite, suiteAt, "GET", "https://example.amazonaws.com/?-._~0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz=-._~0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz", nil, empty,
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=9c3e54bfcdf0b19771a7f523ee5669cdf59bc7cc0884027167c21bb143a40197"},
		{"post-header-key-sort", suite, suiteAt, "POST", "https://example.amazonaws.com/", map[string]string{"My-Header1": "value1"}, empty,
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;my-header1;x-amz-date, Signature=c5410059b04c1ee005303aed430f6e6645f61f4dc9e1461ec8f8916fdf18852c"},
		{"get-header-value-trim", suite, suiteAt, "GET", "https://example.amazonaws.com/", map[string]string{"My-Header1": " value1", "My-Header2": ` "a   b   c"`}, empty,
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;my-header1;my-header2;x-amz-date, Signature=acc3ed3afb60bb290fc8d2dd0098b9911fcaa05412b367055dee359757a9c736"},
		{"get-vanilla-with-session-token", withToken(suite, "6e86291e8372ff2a2260956d9b8aae1d763fbf315fa00fa31553b73ebf194267"), suiteAt, "GET", "https://example.amazonaws.com/", nil, empty,
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date;x-amz-security-token, Signature=07ec1639c89043aa0e3e2de82b96708f198cceab042d4a97044c66dd9f74e7f8"},
		{"post-sts-header-before", withToken(suite, stsToken), suiteAt, "POST", "https://example.amazonaws.com/", nil, empty,
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date;x-amz-security-token, Signature=85d96828115b5dc0cfc3bd16ad9e210dd772bbebba041836c64533a82be05ead"},
		// Few leaves a header that isn't x-amz-* out: post-header-key-sort
		// signed so is post-vanilla
		{"post-header-key-sort, Few", few(suite), suiteAt, "POST", "https://example.amazonaws.com/", map[string]string{"My-Header1": "value1"}, empty,
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5da7c1a2acd57cee7505fc6676e4e544621c30862966e37dddb68e92efbe5d6b"},
		// Bedrock, as botocore 1.42.45's SigV4Auth signs it (no SDK test
		// suite case has the service): a path with an escaped : in it,
		// escaped again; a session token; Mantle's own service name
		{"bedrock invoke-with-response-stream", bedrock("us-west-2", "bedrock"), botoAt, "POST",
			"https://bedrock-runtime.us-west-2.amazonaws.com/model/anthropic.claude-v2%3A1/invoke-with-response-stream",
			map[string]string{"Content-Type": "application/json", "Anthropic-Beta": "x"}, Hash([]byte(`{"prompt":"hi"}`)),
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261124/us-west-2/bedrock/aws4_request, SignedHeaders=host;x-amz-date;x-amz-security-token, Signature=2d4120d69f2a9f83f954ba680be35bf8a351420d792d65159e946e9297ee2e42"},
		{"bedrock chat completions", bedrock("eu-west-1", "bedrock"), botoAt, "POST",
			"https://bedrock-runtime.eu-west-1.amazonaws.com/openai/v1/chat/completions", nil,
			Hash([]byte(`{"model":"openai.gpt-oss-120b-1:0","stream":true}`)),
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261124/eu-west-1/bedrock/aws4_request, SignedHeaders=host;x-amz-date;x-amz-security-token, Signature=9e43ae46521e59b3521125dc5ed327d424d6cba8f6af769466dd4ff6da00e824"},
		{"bedrock mantle models", bedrock("us-east-1", "bedrock-mantle"), botoAt, "GET",
			"https://bedrock-mantle.us-east-1.api.aws/v1/models", nil, empty,
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261124/us-east-1/bedrock-mantle/aws4_request, SignedHeaders=host;x-amz-date;x-amz-security-token, Signature=703c844c7c4a7a9e5b7202d3f808c466ef8e0acdb4232a1816be16085b78f1eb"},
		{"s3 GET object", s3doc, s3At, "GET", "https://examplebucket.s3.amazonaws.com/test.txt",
			map[string]string{"Range": "bytes=0-9", "X-Amz-Content-Sha256": empty}, empty,
			"AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"},
		{"s3 PUT object", s3doc, s3At, "PUT", "https://examplebucket.s3.amazonaws.com/" + Escape("test$file.text", true),
			map[string]string{"Date": "Fri, 24 May 2013 00:00:00 GMT", "X-Amz-Storage-Class": "REDUCED_REDUNDANCY",
				"X-Amz-Content-Sha256": "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072"},
			Hash([]byte("Welcome to Amazon S3.")),
			"AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class, Signature=98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd"},
		{"s3 GET lifecycle", s3doc, s3At, "GET", "https://examplebucket.s3.amazonaws.com/?lifecycle",
			map[string]string{"X-Amz-Content-Sha256": empty}, empty,
			"AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543"},
		{"s3 list objects", s3doc, s3At, "GET", "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J",
			map[string]string{"X-Amz-Content-Sha256": empty}, empty,
			"AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7"},
	} {
		req, err := http.NewRequest(c.method, c.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		c.s.Sign(req, c.payload, c.at)
		if got := req.Header.Get("Authorization"); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
		if !strings.HasPrefix(req.Header.Get("X-Amz-Date"), c.at.Format("20060102T")) {
			t.Errorf("%s: X-Amz-Date %q", c.name, req.Header.Get("X-Amz-Date"))
		}
		if req.Header.Get("X-Amz-Security-Token") != c.s.Token {
			t.Errorf("%s: X-Amz-Security-Token %q", c.name, req.Header.Get("X-Amz-Security-Token"))
		}
	}
	if got := Escape("a b/c~d+e$é", true); got != "a%20b/c~d%2Be%24%C3%A9" {
		t.Errorf("escaped: %s", got)
	}
}
