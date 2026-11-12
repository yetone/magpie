package update

import (
	"context"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
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

// firstAttested is the first release whose files carry an attestation
// (yetone/magpie-releases 9213fbd).
const firstAttested = "0.1.3891"

// attestRepo is where the attestations are stored and what they must name.
const attestRepo = "yetone/magpie-releases"

//go:embed attest_root.json
var attestRootJSON []byte

// attestAPI is the address of the attestation API; a var for tests.
var attestAPI = "https://api.github.com"

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
	err := attestation(ctx, a.SHA256)
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

func attestation(ctx context.Context, digest string) error {
	if os.Getenv("MAGPIE_UPDATE_FEED") != "" {
		return nil
	}
	if Source() == settings.UpdateSourceSite {
		return errUnknown{errors.New("Settings' update source is the site alone, which never asks GitHub")}
	}
	sum, err := hex.DecodeString(digest)
	if err != nil || len(sum) != 32 {
		return fmt.Errorf("%w: the checksum %q is not a SHA-256", errUnattested, digest)
	}
	raws, err := fetchAttestations(ctx, strings.ToLower(digest))
	if err != nil {
		return err
	}
	var last error
	for _, raw := range raws {
		if last = verifyBundle(raw, sum); last == nil {
			return nil
		}
	}
	return fmt.Errorf("%w: %v", errUnattested, last)
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
