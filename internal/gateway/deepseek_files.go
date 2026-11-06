package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/steady"
)

// DeepSeek's official API is stateless, so an agent sends its whole
// history on every turn, and an image sent inline (a data: URL) goes up
// again each time (#5407). DeepSeek's Files API takes an image once and
// lets a request name it by file_id on every API family
// (api-docs.deepseek.com guides/files_api, guides/vision,
// guides/responses_api, guides/anthropic_api):
//
//   - POST /files, multipart: file, purpose=user_data,
//     expires_after[anchor]=created_at, expires_after[seconds] (3600 to
//     2592000); the reply's id is file-api-…, with expires_at.
//   - Chat: {"type":"file","file_id":"file-api-…"}, flat, not OpenAI's
//     nested {"file":{"file_id"}}.
//   - Responses: input_image with file_id in place of image_url (the two
//     are exclusive), in a message or a function_call_output's output.
//   - Anthropic: an image's source {"type":"file","file_id":"…"}, which
//     the anthropic-beta files-api-2025-04-14 must come with.
//
// So for a model that sees images (deepseek-flash) on api.deepseek.com
// itself, never a relay or another host, each inline image is uploaded
// once per key and named by its id after. Anything that goes wrong leaves
// the image inline, as it was sent before: the turn never fails for it.

const (
	// dsFileTTL is how long DeepSeek keeps an uploaded image: a day, not
	// the API's default of for ever, as the image is the user's.
	dsFileTTL = 24 * time.Hour
	// dsFileFresh: a file with less than this left is uploaded again
	// rather than named, so it doesn't expire while a turn is under way.
	dsFileFresh = time.Hour
	// dsFilesBreak is how long a key's uploads are left alone after one
	// failed, its images inline meanwhile, so that every turn doesn't
	// wait on an upload that fails.
	dsFilesBreak = 10 * time.Minute
	// dsFilesAtOnce bounds the uploads one request makes at a time.
	dsFilesAtOnce = 4
	// dsFilesKept bounds the images remembered per file, the least
	// lately used let go first. DeepSeek keeps 10000 a user.
	dsFilesKept = 4000
	// dsFilesMax is the largest image the Files API takes.
	dsFilesMax = 64 << 20
	// dsFilesBeta is the beta DeepSeek's Anthropic endpoint wants beside
	// an image whose source is a file.
	dsFilesBeta = "files-api-2025-04-14"
)

// dsFilesOn is whether a request to p on to, at path, has its inline
// images sent as DeepSeek file references: DeepSeek's own host, with a
// key, a model that sees images, a request that asks a reply, and the
// provider not set image-files=off.
func dsFilesOn(p provider.Provider, to provider.Protocol, path string, body []byte) bool {
	if p.ImageFilesOff || p.Key == "" || countsTokens(path) || !p.ImageFilesAt(to) {
		return false
	}
	// deepseek-flash sees images; deepseek-v4-pro reads each as the text
	// "[Unsupported Image]" (guides/anthropic_api), so nothing is gained
	// uploading one for it
	return strings.Contains(strings.ToLower(modelOf(body)), "flash")
}

// dsFileRef is an image DeepSeek holds for a key.
type dsFileRef struct {
	ID      string    `json:"id"`
	Expires time.Time `json:"expires"`
	Used    time.Time `json:"used"`
}

// dsFileStore is the images uploaded for each key, by the key's hash and
// the image's, kept in deepseek-files.json beside providers.json so a
// restart doesn't upload them again.
type dsFileStore struct {
	mu      sync.Mutex
	wmu     sync.Mutex // held while the file is written
	path    string
	refs    map[string]dsFileRef
	flights map[string]*dsFlight
	broken  map[string]time.Time // a key's hash: when its uploads are tried again
}

// dsFlight is an upload under way, which a request wanting the same
// image for the same key waits for rather than uploading it again.
type dsFlight struct {
	done chan struct{}
	ref  dsFileRef
	err  error
}

var dsStores = struct {
	sync.Mutex
	m map[string]*dsFileStore
}{m: map[string]*dsFileStore{}}

// dsNow is the clock the store reads; a test moves it.
var dsNow = time.Now

// dsWaitMax is the longest a request waits on its uploads before it
// sends the images not up yet inline; a test shortens it.
var dsWaitMax = 8 * time.Second

// dsJoined counts the requests that waited on another's upload, for a
// test to wait on.
var dsJoined atomic.Int64

// dsFilesPath is the file the uploaded images are kept in.
func dsFilesPath() string {
	return filepath.Join(filepath.Dir(provider.Path()), "deepseek-files.json")
}

// dsStore is the store for this config folder, read from its file once.
// A file that can't be parsed is moved aside and the store starts empty:
// every image is uploaded again, as on a first run.
func dsStore() *dsFileStore {
	path := dsFilesPath()
	dsStores.Lock()
	defer dsStores.Unlock()
	if st := dsStores.m[path]; st != nil {
		return st
	}
	st := &dsFileStore{path: path, refs: map[string]dsFileRef{}, flights: map[string]*dsFlight{}, broken: map[string]time.Time{}}
	if b, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(b, &st.refs) != nil {
			st.refs = map[string]dsFileRef{}
			os.Rename(path, path+".bad")
		}
	}
	dsStores.m[path] = st
	return st
}

// dsKeyHash names a key in the store without keeping the key.
func dsKeyHash(key string) string {
	h := sha256.Sum256([]byte("deepseek-files\x00" + key))
	return hex.EncodeToString(h[:8])
}

// save writes the store through a file renamed over the old one. The
// write runs outside mu, which requests take to look an image up, under
// wmu, which keeps one save's file from going over a later one's.
func (st *dsFileStore) save() {
	st.wmu.Lock()
	defer st.wmu.Unlock()
	st.mu.Lock()
	now := dsNow()
	for k, r := range st.refs {
		if !r.Expires.After(now) {
			delete(st.refs, k)
		}
	}
	if len(st.refs) > dsFilesKept {
		keys := make([]string, 0, len(st.refs))
		for k := range st.refs {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(a, b string) int { return st.refs[a].Used.Compare(st.refs[b].Used) })
		for _, k := range keys[:len(keys)-dsFilesKept] {
			delete(st.refs, k)
		}
	}
	b, err := json.Marshal(st.refs)
	st.mu.Unlock()
	if err != nil || os.MkdirAll(filepath.Dir(st.path), 0o700) != nil {
		return
	}
	target, err := edit.Target(st.path)
	if err != nil {
		return
	}
	tmp := target + ".magpie-tmp"
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	if steady.Rename(tmp, target) != nil {
		os.Remove(tmp)
	}
}

// forget lets the images keys named go, after DeepSeek said it has no
// such file.
func (st *dsFileStore) forget(keys []string) {
	st.mu.Lock()
	for _, k := range keys {
		delete(st.refs, k)
	}
	st.mu.Unlock()
	st.save()
}

// breakFor leaves the key's uploads alone for dsFilesBreak.
func (st *dsFileStore) breakFor(acct string) {
	st.mu.Lock()
	st.broken[acct] = dsNow().Add(dsFilesBreak)
	st.mu.Unlock()
}

// dsImage is an inline image of a request: its hash and the base64 it
// came as.
type dsImage struct {
	key  string // the key's hash, ":", the image's
	mime string
	b64  string
}

// dsSent is what a try named by file, kept in its context for forward:
// the store's keys of the images, so a reply that says DeepSeek has no
// such file lets them go, and whether this try goes inline instead.
type dsSent struct {
	keys   []string
	inline bool
}

type dsSentKey struct{}

func dsSentOf(ctx context.Context) *dsSent {
	s, _ := ctx.Value(dsSentKey{}).(*dsSent)
	return s
}

// dsInlineImage reads an image block's data: URL, when it is one the
// Files API can take in its place: base64, not asked at detail low (a
// file is read at full size, which costs more tokens), not over the
// API's size.
func dsInlineImage(to provider.Protocol, im imageAt) (mime, b64 string, ok bool) {
	rest, found := strings.CutPrefix(im.Src, "data:")
	if !found {
		return "", "", false
	}
	meta, data, found := strings.Cut(rest, ",")
	if !found || !strings.HasSuffix(meta, ";base64") || data == "" || len(data)/4*3 > dsFilesMax {
		return "", "", false
	}
	if to != provider.Anthropic {
		var block struct {
			Detail   string          `json:"detail"`
			ImageURL json.RawMessage `json:"image_url"`
		}
		decodeJSON(im.Block, &block)
		detail := block.Detail
		if to == provider.Chat {
			var u struct {
				Detail string `json:"detail"`
			}
			decodeJSON(block.ImageURL, &u)
			detail = u.Detail
		}
		if detail == "low" {
			return "", "", false
		}
	}
	return strings.TrimSuffix(meta, ";base64"), data, true
}

// withDeepSeekFiles is body with every inline image it can name by file
// sent so, each uploaded first for p's key when DeepSeek doesn't hold it
// yet, and whether any was. An image whose upload failed stays inline.
func (s *Server) withDeepSeekFiles(ctx context.Context, p provider.Provider, to provider.Protocol, path string, body []byte) ([]byte, bool) {
	sent := dsSentOf(ctx)
	if sent != nil && sent.inline || !dsFilesOn(p, to, path, body) {
		return body, false
	}
	acct := dsKeyHash(p.Key)
	var images []dsImage
	seen := map[string]bool{}
	walkImages(to, body, func(im imageAt) (json.RawMessage, error) {
		mime, b64, ok := dsInlineImage(to, im)
		if !ok {
			return nil, nil
		}
		h := sha256.Sum256([]byte(b64))
		key := acct + ":" + hex.EncodeToString(h[:])
		if !seen[key] {
			seen[key] = true
			images = append(images, dsImage{key: key, mime: mime, b64: b64})
		}
		return nil, nil
	})
	if len(images) == 0 {
		return body, false
	}
	ids := s.dsResolve(ctx, p, to, acct, images)
	if len(ids) == 0 {
		return body, false
	}
	out, err := walkImages(to, body, func(im imageAt) (json.RawMessage, error) {
		_, b64, ok := dsInlineImage(to, im)
		if !ok {
			return nil, nil
		}
		h := sha256.Sum256([]byte(b64))
		id := ids[acct+":"+hex.EncodeToString(h[:])]
		if id == "" {
			return nil, nil
		}
		return dsFileBlock(to, im.Block, id), nil
	})
	if err != nil || bytes.Equal(out, body) {
		return body, false
	}
	if sent != nil {
		for k := range ids {
			sent.keys = append(sent.keys, k)
		}
	}
	return out, true
}

// dsFileBlock is an image's block naming the file id in its place, in
// to's shape; the block's other fields (Anthropic's cache_control) stay.
func dsFileBlock(to provider.Protocol, raw json.RawMessage, id string) json.RawMessage {
	idJSON, _ := json.Marshal(id)
	if to == provider.Chat {
		return json.RawMessage(`{"type":"file","file_id":` + string(idJSON) + `}`)
	}
	var block map[string]json.RawMessage
	if decodeJSON(raw, &block) != nil {
		return nil
	}
	if to == provider.Responses {
		delete(block, "image_url")
		block["file_id"] = idJSON
	} else {
		block["source"] = json.RawMessage(`{"type":"file","file_id":` + string(idJSON) + `}`)
	}
	b, err := json.Marshal(block)
	if err != nil {
		return nil
	}
	return b
}

// dsResolve is the file id of each image DeepSeek holds, or now holds,
// for the key: those known and not about to expire as they are, the
// others uploaded, a few at a time, one upload for an image however many
// requests want it at once. An image whose upload failed has none, and a
// failure leaves the key's uploads alone for a while.
func (s *Server) dsResolve(ctx context.Context, p provider.Provider, to provider.Protocol, acct string, images []dsImage) map[string]string {
	st := dsStore()
	now := dsNow()
	ids := map[string]string{}
	var waits []*dsFlight
	var waitKeys []string
	var starts []dsImage
	st.mu.Lock()
	broken := st.broken[acct].After(now)
	for _, im := range images {
		if r, ok := st.refs[im.key]; ok && r.Expires.Sub(now) > dsFileFresh {
			r.Used = now
			st.refs[im.key] = r
			ids[im.key] = r.ID
			continue
		}
		if f := st.flights[im.key]; f != nil {
			waits, waitKeys = append(waits, f), append(waitKeys, im.key)
			dsJoined.Add(1)
			continue
		}
		if broken {
			continue
		}
		f := &dsFlight{done: make(chan struct{})}
		st.flights[im.key] = f
		waits, waitKeys = append(waits, f), append(waitKeys, im.key)
		starts = append(starts, im)
	}
	st.mu.Unlock()
	if len(starts) > 0 {
		// the uploads outlive a request that is cancelled, for the
		// others waiting on them; each has its own deadline
		up := context.WithoutCancel(p.Via(ctx))
		slots := make(chan struct{}, dsFilesAtOnce)
		for _, im := range starts {
			go func() {
				slots <- struct{}{}
				ref, err := s.dsUpload(up, p, to, im)
				<-slots
				st.mu.Lock()
				f := st.flights[im.key]
				delete(st.flights, im.key)
				if err == nil {
					ref.Used = dsNow()
					st.refs[im.key] = ref
				} else {
					st.broken[acct] = dsNow().Add(dsFilesBreak)
				}
				st.mu.Unlock()
				if err == nil {
					st.save()
				}
				if f != nil {
					f.ref, f.err = ref, err
					close(f.done)
				}
			}()
		}
	}
	// a turn waits a little for its uploads, not for a slow line: what
	// isn't up by then goes inline this once and by file next turn
	wait := time.NewTimer(dsWaitMax)
	defer wait.Stop()
	for i, f := range waits {
		select {
		case <-f.done:
			if f.err == nil && f.ref.ID != "" {
				ids[waitKeys[i]] = f.ref.ID
			}
		case <-wait.C:
			return ids
		case <-ctx.Done():
			return ids
		}
	}
	if n := len(starts); n > 0 {
		// the uploads went to the vendor on the request's behalf (#4456)
		var up int
		for _, im := range starts {
			up += len(im.b64) / 4 * 3
		}
		if u := upBytesOf(ctx); u != nil {
			u.wire += up
		}
	}
	return ids
}

// dsUpload sends one image to DeepSeek's Files API for p's key, to expire
// in dsFileTTL. Its deadline grows with the image: a slow line takes a
// while, but an upload that hangs doesn't hold the turn for ever.
func (s *Server) dsUpload(ctx context.Context, p provider.Provider, to provider.Protocol, im dsImage) (dsFileRef, error) {
	data, err := base64.StdEncoding.DecodeString(im.b64)
	if err != nil {
		if data, err = base64.RawStdEncoding.DecodeString(im.b64); err != nil {
			return dsFileRef{}, err
		}
	}
	base, err := url.Parse(p.Base(to))
	if err != nil || base.Host == "" {
		return dsFileRef{}, errors.New("no DeepSeek host")
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("purpose", "user_data")
	mw.WriteField("expires_after[anchor]", "created_at")
	mw.WriteField("expires_after[seconds]", strconv.Itoa(int(dsFileTTL/time.Second)))
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="image%s"`, dsExt(im.mime)))
	if im.mime != "" {
		h.Set("Content-Type", im.mime)
	}
	fw, err := mw.CreatePart(h)
	if err != nil {
		return dsFileRef{}, err
	}
	fw.Write(data)
	mw.Close()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second+time.Duration(len(data)/(64<<10))*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.Scheme+"://"+base.Host+"/files", bytes.NewReader(buf.Bytes()))
	if err != nil {
		return dsFileRef{}, err
	}
	// the key as a Chat request carries it (Authorization: Bearer)
	if err := p.Sign(ctx, req, provider.Chat, nil); err != nil {
		return dsFileRef{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgentFor(p, ""))
	res, err := p.Do(s.client, req)
	if err != nil {
		return dsFileRef{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode/100 != 2 {
		return dsFileRef{}, fmt.Errorf("upload: %d %s", res.StatusCode, b)
	}
	var got struct {
		ID        string `json:"id"`
		CreatedAt int64  `json:"created_at"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if json.Unmarshal(b, &got) != nil || got.ID == "" {
		return dsFileRef{}, fmt.Errorf("upload: no file id in %s", b)
	}
	ref := dsFileRef{ID: got.ID, Expires: dsNow().Add(dsFileTTL)}
	if got.ExpiresAt > 0 {
		ref.Expires = time.Unix(got.ExpiresAt, 0)
	}
	return ref, nil
}

// dsExt is a file name's extension for an image's media type. DeepSeek
// reads the format from the bytes; the name is only a name.
func dsExt(mime string) string {
	switch mime {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	}
	return ".png"
}

// dsFileGone is whether DeepSeek's refusal of a request that named
// files says one of them is gone: expired, deleted, or never this key's
// account's. Its exact words aren't documented, so a 4xx that names a
// file id sent, or a file and its being missing, counts.
func dsFileGone(code int, body []byte) bool {
	if code < 400 || code >= 500 || code == http.StatusTooManyRequests {
		return false
	}
	low := strings.ToLower(string(body))
	if strings.Contains(low, "file-api-") {
		return true
	}
	if !strings.Contains(low, "file") {
		return false
	}
	for _, w := range []string{"not found", "not exist", "expired", "no such", "deleted", "invalid file", "unknown file"} {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}

// dsBeta is betas with the Files API's added, for an Anthropic request
// that names an image by file.
func dsBeta(betas []string) []string {
	if slices.Contains(betas, dsFilesBeta) {
		return betas
	}
	return append(slices.Clone(betas), dsFilesBeta)
}

// dsRetryGone asks again a request DeepSeek refused because a file it
// named is gone (expired early, deleted, or the store out of date): the
// images are let go and uploaded again, once; refused again so, the
// request goes with them inline, and the key's uploads rest a while.
func (s *Server) dsRetryGone(ctx context.Context, p provider.Provider, to provider.Protocol, path string, body []byte, in http.Header, res *http.Response) (*http.Response, error) {
	sent := dsSentOf(ctx)
	for try := 0; sent != nil && len(sent.keys) > 0 && !sent.inline && try < 2; try++ {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(b))
		if !dsFileGone(res.StatusCode, b) {
			return res, nil
		}
		st := dsStore()
		st.forget(sent.keys)
		sent.keys = nil
		if try == 1 {
			st.breakFor(dsKeyHash(p.Key))
			sent.inline = true
		}
		var err error
		if res, err = s.forwardOnce(ctx, p, to, path, body, in); err != nil || res.StatusCode < 400 {
			return res, err
		}
	}
	return res, nil
}
