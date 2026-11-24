package artifact

import (
	"bytes"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/lazyre"
)

// Assets (docs/subsystems/artifacts.md, "Assets"): the files a page
// uploads through claude.use("assets"), as Claude's runtime contract
// 0.2.99 has them. They are the artifact's, not a version's: every
// version reaches them, a publish keeps them, and they go with the
// artifact. Each is <id>/assets/<asset id>, listed in <id>/assets.json,
// and served at the root, BlobPath<asset id>, which a page reaches from
// every version at the same address.

// BlobPath is where an asset is served, by its id alone.
const BlobPath = "/_blob/"

const (
	assetsDir   = "assets"
	assetsIndex = "assets.json"
	// MaxAsset is the most an asset may be; an SVG MaxSVGAsset, a
	// stylesheet or script MaxCodeAsset
	MaxAsset     = 20 << 20
	MaxSVGAsset  = 2 << 20
	MaxCodeAsset = 16 << 20
	// MaxAssets and MaxAssetBytes are an artifact's budget
	MaxAssets     = 1000
	MaxAssetBytes = 1 << 30
)

// AssetTypes are the types an asset may be, exactly as spelled: no
// parameters, no aliases.
var AssetTypes = []string{
	"image/png", "image/jpeg", "image/gif", "image/webp", "image/svg+xml",
	"video/mp4", "video/webm", "application/pdf",
	"font/woff2", "font/woff", "font/ttf", "font/otf",
	"text/csv", "text/markdown", "application/json", "text/plain", "text/css", "text/javascript",
}

// the asset refusals, by the code the page reads (ErrTooLarge is
// too_large)
var (
	ErrAssetRequest = errors.New("not an asset")
	ErrAssetType    = errors.New("not a type an asset may be")
	ErrAssetQuota   = errors.New("the artifact's assets are full")
)

// Asset is one stored asset, as assets.json keeps it.
type Asset struct {
	ID          string    `json:"id"`
	ContentType string    `json:"contentType"`
	Size        int64     `json:"sizeBytes"`
	Created     time.Time `json:"createdAt"`
}

// AssetUsage is how much of an artifact's budget its assets take.
type AssetUsage struct {
	Files    int   `json:"files"`
	Bytes    int64 `json:"bytes"`
	MaxFiles int   `json:"maxFiles"`
	MaxBytes int64 `json:"maxBytes"`
}

var assetIDOK = lazyre.New(`^[a-z2-7]{32}$`)

// ValidAssetID is whether s is one an asset can have.
func ValidAssetID(s string) bool { return assetIDOK.MatchString(s) }

// newAssetID is 160 random bits, 32 characters of lowercase base32.
func newAssetID() string {
	b := make([]byte, 20)
	rand.Read(b)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

func assetPath(id, aid string) string { return filepath.Join(folder(id), assetsDir, aid) }

// readAssets is the artifact's index, oldest first; none is empty.
func readAssets(id string) ([]Asset, error) {
	b, err := os.ReadFile(filepath.Join(folder(id), assetsIndex))
	if errors.Is(err, fs.ErrNotExist) {
		return []Asset{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Asset
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(folder(id), assetsIndex), err)
	}
	return out, nil
}

func usage(list []Asset) AssetUsage {
	u := AssetUsage{Files: len(list), MaxFiles: MaxAssets, MaxBytes: MaxAssetBytes}
	for _, a := range list {
		u.Bytes += a.Size
	}
	return u
}

// AssetLimit is the most an asset of type t may be.
func AssetLimit(t string) int {
	switch t {
	case "image/svg+xml":
		return MaxSVGAsset
	case "text/css", "text/javascript":
		return MaxCodeAsset
	}
	return MaxAsset
}

// CheckAsset is body as it is stored under type t (an SVG sanitized),
// or why it can't be.
func CheckAsset(body []byte, t string) ([]byte, error) {
	if !slices.Contains(AssetTypes, t) {
		if strings.Contains(t, ";") {
			return nil, refuse(ErrAssetType, fmt.Errorf("the type is %q exactly, with no parameters", strings.TrimSpace(strings.SplitN(t, ";", 2)[0])))
		}
		return nil, refuse(ErrAssetType, fmt.Errorf("%q isn't a type an asset may be; they are %s", t, strings.Join(AssetTypes, ", ")))
	}
	if len(body) == 0 {
		return nil, refuse(ErrAssetRequest, errors.New("the file is empty"))
	}
	if most := AssetLimit(t); len(body) > most {
		return nil, refuse(ErrTooLarge, fmt.Errorf("a %s asset is at most %d MiB", t, most>>20))
	}
	lead := bytes.TrimLeft(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")), " \t\r\n\f")
	switch t {
	case "image/svg+xml":
		return SanitizeSVG(body)
	case "text/csv", "text/markdown", "application/json", "text/plain", "text/css", "text/javascript":
		if !utf8.Valid(body) {
			return nil, refuse(ErrAssetRequest, errors.New("a text file is UTF-8: re-encode it before uploading"))
		}
		if t == "text/css" || t == "text/javascript" {
			if bytes.HasPrefix(lead, []byte("<")) {
				return nil, refuse(ErrAssetType, errors.New("a stylesheet or script doesn't start with markup"))
			}
			if bytes.IndexByte(body, 0) >= 0 {
				return nil, refuse(ErrAssetType, errors.New("a stylesheet or script is text"))
			}
		}
	default:
		if bytes.HasPrefix(lead, []byte("<")) {
			return nil, refuse(ErrAssetType, fmt.Errorf("this starts with markup, so it isn't %s: markup uploads only as image/svg+xml", t))
		}
	}
	return body, nil
}

// UploadAsset stores body, checked (CheckAsset), as one of artifact id's
// assets.
func UploadAsset(id string, body []byte, t string) (*Asset, error) {
	body, err := CheckAsset(body, t)
	if err != nil {
		return nil, err
	}
	if _, err := readMeta(id); err != nil {
		return nil, err
	}
	aid := newAssetID()
	dir := filepath.Join(folder(id), assetsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// the bytes are written before the lock is taken, and taken back
	// if the budget hasn't room for them
	if err := edit.WriteAtomic(assetPath(id, aid), body); err != nil {
		return nil, err
	}
	kept := false
	defer func() {
		if !kept {
			os.Remove(assetPath(id, aid))
		}
	}()
	defer lock()()
	if _, err := readMeta(id); err != nil {
		if errors.Is(err, ErrNotFound) {
			// deleted while the bytes were written: what they made goes too
			os.RemoveAll(folder(id))
		}
		return nil, err
	}
	list, err := readAssets(id)
	if err != nil {
		return nil, err
	}
	u := usage(list)
	if u.Files+1 > MaxAssets {
		return nil, refuse(ErrAssetQuota, fmt.Errorf("an artifact holds at most %d assets: delete some nothing points at", MaxAssets))
	}
	if u.Bytes+int64(len(body)) > MaxAssetBytes {
		return nil, refuse(ErrAssetQuota, fmt.Errorf("an artifact's assets are at most %d MiB together: delete some nothing points at", MaxAssetBytes>>20))
	}
	a := Asset{ID: aid, ContentType: t, Size: int64(len(body)), Created: time.Now().UTC().Truncate(time.Second)}
	if err := writeAssets(id, append(list, a)); err != nil {
		return nil, err
	}
	kept = true
	blobs.Store(aid, id)
	return &a, nil
}

func writeAssets(id string, list []Asset) error {
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return edit.WriteAtomic(filepath.Join(folder(id), assetsIndex), b)
}

// Assets is artifact id's assets, oldest first, and its budget.
func Assets(id string) ([]Asset, AssetUsage, error) {
	if _, err := readMeta(id); err != nil {
		return nil, AssetUsage{}, err
	}
	list, err := readAssets(id)
	if err != nil {
		return nil, AssetUsage{}, err
	}
	return list, usage(list), nil
}

// DeleteAsset takes asset aid away from artifact id for good; deleted
// is false when it holds none by that id.
func DeleteAsset(id, aid string) (deleted bool, err error) {
	if !ValidAssetID(aid) {
		return false, refuse(ErrAssetRequest, errors.New("that isn't an asset's id or url"))
	}
	defer lock()()
	if _, err := readMeta(id); err != nil {
		return false, err
	}
	list, err := readAssets(id)
	if err != nil {
		return false, err
	}
	i := slices.IndexFunc(list, func(a Asset) bool { return a.ID == aid })
	if i < 0 {
		return false, nil
	}
	if err := writeAssets(id, slices.Delete(list, i, i+1)); err != nil {
		return false, err
	}
	blobs.Delete(aid)
	if err := os.Remove(assetPath(id, aid)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return true, err
	}
	return true, nil
}

// blobs remembers which artifact holds an asset, as BlobFile found it.
var blobs sync.Map

// BlobFile is asset aid's file and record, whichever artifact holds
// it: an asset's address names it alone.
func BlobFile(aid string) (string, *Asset, error) {
	if !ValidAssetID(aid) {
		return "", nil, ErrNotFound
	}
	if id, ok := blobs.Load(aid); ok {
		if p, a, err := blobIn(id.(string), aid); err == nil {
			return p, a, nil
		}
		blobs.Delete(aid)
	}
	ents, err := os.ReadDir(Dir())
	if err != nil {
		return "", nil, ErrNotFound
	}
	for _, e := range ents {
		if !e.IsDir() || !ValidID(e.Name()) {
			continue
		}
		if _, err := os.Stat(assetPath(e.Name(), aid)); err != nil {
			continue
		}
		if p, a, err := blobIn(e.Name(), aid); err == nil {
			blobs.Store(aid, e.Name())
			return p, a, nil
		}
	}
	return "", nil, ErrNotFound
}

// blobIn is asset aid in artifact id, if its index lists it and its
// file is there.
func blobIn(id, aid string) (string, *Asset, error) {
	list, err := readAssets(id)
	if err != nil {
		return "", nil, err
	}
	i := slices.IndexFunc(list, func(a Asset) bool { return a.ID == aid })
	if i < 0 {
		return "", nil, ErrNotFound
	}
	p := assetPath(id, aid)
	if _, err := os.Stat(p); err != nil {
		return "", nil, ErrNotFound
	}
	return p, &list[i], nil
}
