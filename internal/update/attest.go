package update

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/source"
)

// A release file is also checked against its GitHub build attestation
// (ArcticLampyrid on X: a key of our own is a weaker root of trust than a
// platform's signature). Release CI (yetone/magpie-releases, release.yml)
// attests every file with actions/attest-build-provenance: a SLSA
// provenance statement, signed through Sigstore's public good instance with
// the run's own GitHub identity, which GitHub stores and serves at
// api.github.com/repos/<repo>/attestations/sha256:<digest>. It is a check on
// top of the ed25519 signature over SHA256SUMS (signed.go), never instead of
// it: that one works offline and from the R2 mirror, and a release must pass
// it first. The digest asked about is the one that signed list gives, so the
// attestation speaks of exactly the bytes the download is checked against.
//
// What the bundle must show, checked by sigstore-go against the Sigstore
// trusted root embedded in this binary (attest_root.json, from
// `gh attestation trusted-root`; the bundle carries its own Rekor entry and
// certificate timestamp, so nothing but the API call goes to the network):
//   - a certificate chained to Sigstore's Fulcio, with a signed certificate
//     timestamp and a Rekor entry, so it was valid when it signed;
//   - the issuer https://token.actions.githubusercontent.com, a subject of
//     release.yml in yetone/magpie-releases on its main branch or a v* tag,
//     and that repository as the certificate's source (a workflow of
//     another repo, or another file, is refused);
//   - a statement whose subject is the file's digest.
//
// What happens when it can't be had decides whether an outage of GitHub
// stops updates, and the code standards say a failed read is "unknown", not
// "fine":
//   - A bundle that is there and fails (signature, identity, digest, or a
//     chain that doesn't reach the trusted root) is a refusal: the file is
//     not installed.
//   - GitHub answering 404, or listing none, is a refusal for a release from
//     firstAttested on: it has been asked and says nobody built these bytes.
//   - Whatever leaves it unknown (the API unreachable, rate limited (60 an
//     hour anonymously), 5xx, an answer that isn't JSON, Settings' update
//     source being the site alone, which never goes to GitHub) does not stop
//     the update, since the ed25519 check has passed and an outage of GitHub
//     must not take updates away. It is not silent: the log says the file
//     was installed unattested, and why.
//
// Someone who can only drop the API call downgrades an update to the
// ed25519 check, never below it.
//
// Where the bundles come from (ArcticLampyrid on X): a file the feed names
// on the site (usemagpie.ai/files/<tag>/<file>, a copy on R2) has its
// release's bundles asked of the same place first,
// /files/<tag>/attestations.jsonl, a bundle to a line, which the site's
// cron copies from this API (site/files.js). The site is only transport:
// its bundles go through the same verifyBundle, against the same embedded
// root, identity and signed digest, so it can't make a bad one pass.
//   - The site has none (a tag pruned from R2 redirects to GitHub, whose
//     release has no such asset; a redirect is not followed), can't be
//     reached, or sends no bundle: a miss, and the API is asked as before.
//   - The site's bundles fail: the API is asked too, and a valid bundle
//     there is enough. When the API can't be read, or Settings' update
//     source is the site alone, that is a refusal, not unknown: a bundle
//     was had and failed.
//   - Settings' update source is GitHub: the file comes from github.com,
//     and so do its bundles, from the API alone.

// firstAttested is the first release whose files carry an attestation
// (yetone/magpie-releases 9213fbd).
const firstAttested = "0.1.3891"

// attestRepo is where the attestations are stored and what they must name.
const attestRepo = "yetone/magpie-releases"

//go:embed attest_root.json
var attestRootJSON []byte

// attestAPI is the address of the attestation API; a var for tests.
var attestAPI = "https://api.github.com"

// attestSite is where a file the feed names on Site has its release's
// bundles asked first; a var for tests.
var attestSite = Site

// siteBundles is the name of a release's bundles on the site
// (site/files.js BUNDLES).
const siteBundles = "attestations.jsonl"

// attestID is the identity a bundle must show; a var for tests.
var attestID = struct{ SAN, Issuer, Repo string }{
	SAN:    `^https://github\.com/` + regexp.QuoteMeta(attestRepo) + `/\.github/workflows/release\.yml@refs/(heads/main|tags/v[0-9A-Za-z.+-]+)$`,
	Issuer: "https://token.actions.githubusercontent.com",
	Repo:   "https://github.com/" + attestRepo,
}

// errUnattested is a refusal: the attestation is there and wrong, or GitHub
// says there is none.
var errUnattested = errors.New("not attested")

// errUnknown is what could not be asked or read.
type errUnknown struct{ err error }

func (e errUnknown) Error() string { return e.err.Error() }
func (e errUnknown) Unwrap() error { return e.err }

// attestedFile checks the attestation of the file whose signed SHA-256 is
// a.SHA256. It returns nil when the file is attested, or when that is
// unknown (said in the log), and an error that stops the install otherwise.
func attestedFile(ctx context.Context, a Asset, name string) error {
	if !a.attest {
		return nil
	}
	err := attestation(ctx, a)
	if err == nil {
		return nil
	}
	var u errUnknown
	if errors.As(err, &u) {
		log.Printf("update: %s goes on without its GitHub build attestation checked (%v); its ed25519-signed checksum held", name, u.err)
		return nil
	}
	return fmt.Errorf("%s: %w; not installed", name, err)
}

func attestation(ctx context.Context, a Asset) error {
	if os.Getenv("MAGPIE_UPDATE_FEED") != "" {
		return nil
	}
	sum, err := hex.DecodeString(a.SHA256)
	if err != nil || len(sum) != 32 {
		return fmt.Errorf("%w: the checksum %q is not a SHA-256", errUnattested, a.SHA256)
	}
	// the site's copy of the release's bundles, when the file comes from it
	var siteErr error
	if u := siteBundlesURL(a); u != "" {
		if raws := fetchSiteBundles(ctx, u); len(raws) > 0 {
			if siteErr = verifyAny(raws, sum); siteErr == nil {
				return nil
			}
			siteErr = fmt.Errorf("the bundles from %s fail: %v", hostOf(u), siteErr)
		}
	}
	if Source() == settings.UpdateSourceSite {
		if siteErr != nil {
			return fmt.Errorf("%w: %v", errUnattested, siteErr)
		}
		return errUnknown{errors.New("Settings' update source is the site alone, which never asks GitHub, and the site has no bundles for it")}
	}
	raws, err := fetchAttestations(ctx, strings.ToLower(a.SHA256))
	if err != nil {
		var u errUnknown
		if siteErr != nil && errors.As(err, &u) {
			return fmt.Errorf("%w: %v, and GitHub's couldn't be read (%v)", errUnattested, siteErr, u.err)
		}
		return err
	}
	if last := verifyAny(raws, sum); last != nil {
		return fmt.Errorf("%w: %v", errUnattested, last)
	}
	if siteErr != nil {
		log.Printf("update: %s; GitHub's held", siteErr)
	}
	return nil
}

// verifyAny is nil when one of raws attests sum, else the last failure.
func verifyAny(raws []json.RawMessage, sum []byte) error {
	var last error
	for _, raw := range raws {
		if last = verifyBundle(raw, sum); last == nil {
			return nil
		}
	}
	return last
}

// siteBundlesURL is the address of the bundles of a's release on the site,
// "" when a isn't downloaded from there.
func siteBundlesURL(a Asset) string {
	if Source() == settings.UpdateSourceGitHub {
		return ""
	}
	rest, ok := strings.CutPrefix(a.URL, Site+"/files/")
	if !ok {
		return ""
	}
	tag, _, ok := strings.Cut(rest, "/")
	if !ok || tag == "" {
		return ""
	}
	return attestSite + "/files/" + url.PathEscape(tag) + "/" + siteBundles
}

// fetchSiteBundles reads the site's attestations.jsonl at u, nil on any
// miss. A tag the site no longer keeps is a redirect to GitHub's release,
// which has no such file; it is not followed, and the API is asked instead.
func fetchSiteBundles(ctx context.Context, u string) []json.RawMessage {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := c.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil
	}
	var raws []json.RawMessage
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) > 0 && line[0] == '{' && json.Valid(line) {
			raws = append(raws, json.RawMessage(line))
		}
	}
	return raws
}

// fetchAttestations asks GitHub for the bundles attesting digest. The
// bundles check themselves, so a mirror of the API (source.Do) can't make a
// bad one pass.
func fetchAttestations(ctx context.Context, digest string) ([]json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", attestAPI+"/repos/"+attestRepo+"/attestations/sha256:"+digest, nil)
	if err != nil {
		return nil, errUnknown{err}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := source.Do(client, req)
	if err != nil {
		return nil, errUnknown{fmt.Errorf("GitHub's attestation API: %w", err)}
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w: GitHub has no build attestation for sha256:%s in %s", errUnattested, digest, attestRepo)
	case res.StatusCode != http.StatusOK:
		return nil, errUnknown{fmt.Errorf("GitHub's attestation API answered %s", res.Status)}
	}
	var out struct {
		Attestations []struct {
			Bundle json.RawMessage `json:"bundle"`
		} `json:"attestations"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&out); err != nil {
		return nil, errUnknown{fmt.Errorf("GitHub's attestation API: %w", err)}
	}
	var raws []json.RawMessage
	for _, x := range out.Attestations {
		if len(x.Bundle) > 0 {
			raws = append(raws, x.Bundle)
		}
	}
	if len(raws) == 0 {
		return nil, fmt.Errorf("%w: GitHub lists no build attestation for sha256:%s", errUnattested, digest)
	}
	return raws, nil
}

// verifyBundle checks one Sigstore bundle against the embedded trusted root
// and the identity above, for an artifact of digest sum.
func verifyBundle(raw []byte, sum []byte) error {
	tr, err := root.NewTrustedRootFromJSON(attestRootJSON)
	if err != nil {
		return fmt.Errorf("the embedded Sigstore root: %w", err)
	}
	var b bundle.Bundle
	if err := b.UnmarshalJSON(raw); err != nil {
		return fmt.Errorf("the bundle: %w", err)
	}
	v, err := verify.NewVerifier(tr,
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1))
	if err != nil {
		return err
	}
	san, err := verify.NewSANMatcher("", attestID.SAN)
	if err != nil {
		return err
	}
	iss, err := verify.NewIssuerMatcher(attestID.Issuer, "")
	if err != nil {
		return err
	}
	id, err := verify.NewCertificateIdentity(san, iss, certificate.Extensions{SourceRepositoryURI: attestID.Repo})
	if err != nil {
		return err
	}
	_, err = v.Verify(&b, verify.NewPolicy(verify.WithArtifactDigest("sha256", sum), verify.WithCertificateIdentity(id)))
	return err
}
