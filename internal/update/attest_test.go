package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// The fixture is the answer api.github.com gave on 2026-11-12 for the
// SHA256SUMS of v0.1.3925 in yetone/magpie-releases, byte for byte.
const (
	attestedSums = "9fbb3e07c679a0a9c391305fde380d27eb61726bf28f4b078b2100f28379ce14"
	otherDigest  = "0000000000000000000000000000000000000000000000000000000000000001"
)

func attestFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "attest", "sums-v0.1.3925.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// attestServer answers the attestation API with body (status code), counts
// the asks and points the updater at it.
func attestServer(t *testing.T, code int, body []byte) *int32 {
	t.Helper()
	var asks int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&asks, 1)
		if !strings.HasPrefix(r.URL.Path, "/repos/yetone/magpie-releases/attestations/sha256:") {
			t.Errorf("asked %s", r.URL.Path)
		}
		w.WriteHeader(code)
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	old := attestAPI
	attestAPI = srv.URL
	t.Cleanup(func() { attestAPI = old })
	t.Setenv("MAGPIE_UPDATE_FEED", "")
	return &asks
}

func logged(t *testing.T) *bytes.Buffer {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

func TestAttestationOfARealBundleHolds(t *testing.T) {
	attestServer(t, 200, attestFixture(t))
	if err := attestation(context.Background(), attestedSums); err != nil {
		t.Fatal(err)
	}
}

// One statement names every file of the release as a subject; a digest
// outside them is not attested. A bundle that attests other bytes is not an attestation of this file.
func TestAttestationOfAnotherDigestRefused(t *testing.T) {
	attestServer(t, 200, attestFixture(t))
	err := attestation(context.Background(), otherDigest)
	if !errors.Is(err, errUnattested) {
		t.Fatalf("got %v", err)
	}
}

// A bundle with one byte of its signature changed.
func TestAttestationTamperedRefused(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(attestFixture(t), &doc); err != nil {
		t.Fatal(err)
	}
	b := doc["attestations"].([]any)[0].(map[string]any)["bundle"].(map[string]any)
	sig := b["dsseEnvelope"].(map[string]any)["signatures"].([]any)[0].(map[string]any)
	s := sig["sig"].(string)
	flip := byte('A')
	if s[10] == 'A' {
		flip = 'B'
	}
	sig["sig"] = s[:10] + string(flip) + s[11:]
	body, _ := json.Marshal(doc)
	attestServer(t, 200, body)
	if err := attestation(context.Background(), attestedSums); !errors.Is(err, errUnattested) {
		t.Fatalf("got %v", err)
	}
}

// A payload edited under the same signature (another subject).
func TestAttestationEditedPayloadRefused(t *testing.T) {
	body := bytes.Replace(attestFixture(t), []byte(`"payloadType"`), []byte(`"payloadType": "x", "payloadTypeWas"`), 1)
	attestServer(t, 200, body)
	if err := attestation(context.Background(), attestedSums); !errors.Is(err, errUnattested) {
		t.Fatalf("got %v", err)
	}
}

// The bundle is real and valid, but from a workflow other than the one
// that releases magpie: each of repo, file and ref is judged.
func TestAttestationWrongIdentityRefused(t *testing.T) {
	attestServer(t, 200, attestFixture(t))
	old := attestID
	t.Cleanup(func() { attestID = old })
	for name, c := range map[string]struct{ san, repo, issuer string }{
		"another repo":     {`^https://github\.com/someone/else/\.github/workflows/release\.yml@refs/heads/main$`, old.Repo, old.Issuer},
		"another workflow": {`^https://github\.com/yetone/magpie-releases/\.github/workflows/other\.yml@refs/heads/main$`, old.Repo, old.Issuer},
		"another ref":      {`^https://github\.com/yetone/magpie-releases/\.github/workflows/release\.yml@refs/heads/dev$`, old.Repo, old.Issuer},
		"another source":   {old.SAN, "https://github.com/yetone/magpie", old.Issuer},
		"another issuer":   {old.SAN, old.Repo, "https://accounts.example.com"},
	} {
		attestID.SAN, attestID.Repo, attestID.Issuer = c.san, c.repo, c.issuer
		if err := attestation(context.Background(), attestedSums); !errors.Is(err, errUnattested) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	attestID = old
	if err := attestation(context.Background(), attestedSums); err != nil {
		t.Fatalf("the real identity: %v", err)
	}
}

// GitHub says it has none: asked and answered, a refusal.
func TestAttestationNoneListedRefused(t *testing.T) {
	for name, c := range map[string]struct {
		code int
		body string
	}{"404": {404, `{"message":"Not Found"}`}, "empty list": {200, `{"attestations":[]}`}} {
		attestServer(t, c.code, []byte(c.body))
		a := Asset{SHA256: attestedSums, attest: true}
		err := attestedFile(context.Background(), a, "magpie-linux-amd64")
		if !errors.Is(err, errUnattested) || !strings.Contains(err.Error(), "not installed") {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

// GitHub unreachable, busy or rate limited, or not answering in JSON:
// unknown. The ed25519-signed checksum has held, so the update goes on, and
// the log says the attestation was not checked.
func TestAttestationUnknownGoesOnAndSaysSo(t *testing.T) {
	for name, c := range map[string]struct {
		code int
		body string
	}{"rate limited": {403, `{"message":"API rate limit exceeded"}`}, "429": {429, ``}, "502": {502, `bad gateway`}, "html": {200, `<html>captive portal</html>`}} {
		attestServer(t, c.code, []byte(c.body))
		buf := logged(t)
		a := Asset{SHA256: attestedSums, attest: true}
		if err := attestedFile(context.Background(), a, "magpie-linux-amd64"); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if !strings.Contains(buf.String(), "without its GitHub build attestation checked") {
			t.Errorf("%s: not said: %q", name, buf.String())
		}
	}
	// nothing listening at all
	srv := httptest.NewServer(http.NotFoundHandler())
	old := attestAPI
	attestAPI = srv.URL
	srv.Close()
	t.Cleanup(func() { attestAPI = old })
	t.Setenv("MAGPIE_UPDATE_FEED", "")
	buf := logged(t)
	if err := attestedFile(context.Background(), Asset{SHA256: attestedSums, attest: true}, "f"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "without its GitHub build attestation checked") {
		t.Fatalf("not said: %q", buf.String())
	}
}

// Unknown is never taken for attested: nothing is claimed, nothing is
// refused, and a bundle that fails after an unknown answer is still refused.
func TestAttestationUnknownIsNotVerified(t *testing.T) {
	attestServer(t, 503, nil)
	err := attestation(context.Background(), attestedSums)
	var u errUnknown
	if err == nil || !errors.As(err, &u) {
		t.Fatalf("got %v", err)
	}
}

func TestAttestationOnlyWhereItIsOwed(t *testing.T) {
	asks := attestServer(t, 404, nil)
	// a file from before attestations, and a test's own feed
	if err := attestedFile(context.Background(), Asset{SHA256: attestedSums}, "f"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAGPIE_UPDATE_FEED", "http://127.0.0.1:1")
	if err := attestedFile(context.Background(), Asset{SHA256: attestedSums, attest: true}, "f"); err != nil {
		t.Fatal(err)
	}
	// the site alone never asks GitHub
	t.Setenv("MAGPIE_UPDATE_FEED", "")
	before := settings.Load()
	if err := settings.Save(settings.Settings{UpdateSource: settings.UpdateSourceSite}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { settings.Save(before) })
	logged(t)
	if err := attestedFile(context.Background(), Asset{SHA256: attestedSums, attest: true}, "f"); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(asks); n != 0 {
		t.Fatalf("asked GitHub %d times", n)
	}
}

// Release.verify marks the files of releases that carry attestations.
func TestVerifyMarksAttestedReleases(t *testing.T) {
	rel := signedRelease(t, firstAttested, map[string]string{"a": "1"})
	if err := rel.verify(); err != nil || !rel.Assets["a"].attest {
		t.Fatalf("%v %+v", err, rel.Assets["a"])
	}
	rel = signedRelease(t, "0.1.3890", map[string]string{"a": "1"})
	if err := rel.verify(); err != nil || rel.Assets["a"].attest {
		t.Fatalf("%v %+v", err, rel.Assets["a"])
	}
}

// A download of a file GitHub has no attestation for fetches nothing.
func TestDownloadRefusedWithoutAttestation(t *testing.T) {
	attestServer(t, 404, nil)
	var fetched int32
	file := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fetched, 1)
		w.Write([]byte("x"))
	}))
	defer file.Close()
	a := Asset{URL: file.URL + "/f", SHA256: attestedSums, attest: true}
	err := download(context.Background(), a, filepath.Join(t.TempDir(), "f"))
	if !errors.Is(err, errUnattested) || atomic.LoadInt32(&fetched) != 0 {
		t.Fatalf("err %v, fetched %d", err, fetched)
	}
}
