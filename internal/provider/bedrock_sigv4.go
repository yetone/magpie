package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/sigv4"
)

// SignsWithAccessKey reports whether p is Amazon Bedrock (its runtime or
// Mantle) asked with an AWS access key, signed with SigV4, in place of a
// Bedrock API key (#7687): an account whose policy allows no API keys
// still has an IAM user's or role's. A Bedrock API key, when p has one,
// is what is sent; the access key then goes unused.
func (p Provider) SignsWithAccessKey() bool {
	return p.Account == nil && p.Key == "" && p.AccessKeyID != "" && p.SecretAccessKey != "" && p.IsBedrock()
}

// awsNow is the time a request is signed at; a test holds it.
var awsNow = time.Now

// signAWS signs req with p's access key as Bedrock checks it: the
// service and region its host names (bedrock-runtime.{region}.amazonaws.com
// is "bedrock", bedrock-mantle.{region}.api.aws "bedrock-mantle"), over
// host and the x-amz-* headers alone, since the gateway still changes
// others after Sign (the user's anthropic-beta), and the hash of the body
// the request will send: read again through GetBody, the caller's body
// when there is none, or read from the request, which is then left to
// send it whole.
func (p Provider) signAWS(req *http.Request, body []byte) error {
	h := sha256.New()
	switch {
	case req.Body == nil || req.Body == http.NoBody:
	case req.GetBody != nil:
		rc, err := req.GetBody()
		if err != nil {
			return err
		}
		_, err = io.Copy(h, rc)
		rc.Close()
		if err != nil {
			return err
		}
	case body != nil:
		h.Write(body)
	default:
		b, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return err
		}
		SetBody(req, b)
		h.Write(b)
	}
	region, service := p.awsScope(req.URL.Host)
	sigv4.Signer{ID: p.AccessKeyID, Secret: p.SecretAccessKey, Token: p.SessionToken,
		Region: region, Service: service, Few: true, Twice: true}.Sign(req, hex.EncodeToString(h.Sum(nil)), awsNow())
	return nil
}

// awsScope is the region and service a request to host is signed for:
// read from Bedrock's own host, or, for one in front of it (a proxy, a
// test's server), from the provider's addresses; us-east-1 when none
// names one. The service is Mantle's at a Mantle host or for its preset.
func (p Provider) awsScope(host string) (region, service string) {
	region, service = bedrockHostScope(host)
	if region != "" {
		return region, service
	}
	service = "bedrock"
	if p.IsBedrockMantle() {
		service = "bedrock-mantle"
	}
	for _, base := range []string{p.Anthropic, p.Chat, p.Responses} {
		if r, _ := bedrockHostScope(HostOf(base)); r != "" {
			return r, service
		}
	}
	return "us-east-1", service
}

// bedrockHostScope is the region and service of a Bedrock host:
// bedrock-runtime[-fips].{region}.amazonaws.com[.cn] and
// bedrock-mantle.{region}.api.aws; "" for any other.
func bedrockHostScope(host string) (region, service string) {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	labels := strings.Split(strings.ToLower(host), ".")
	if len(labels) < 4 {
		return "", ""
	}
	rest := strings.Join(labels[2:], ".")
	switch {
	case (labels[0] == "bedrock-runtime" || labels[0] == "bedrock-runtime-fips") && (rest == "amazonaws.com" || rest == "amazonaws.com.cn"):
		return labels[1], "bedrock"
	case labels[0] == "bedrock-mantle" && rest == "api.aws":
		return labels[1], "bedrock-mantle"
	}
	return "", ""
}
