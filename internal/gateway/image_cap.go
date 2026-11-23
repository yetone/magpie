package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A model's cap on the images of one request.
//
// Copilot's Gemini models take at most 10 images in a request, the whole
// conversation's counted: past them every turn is refused 400 "too many
// images: maximum allowed for model gemini-4-carbon is 10, got 12", and a
// conversation that has shown the model ten screenshots can't go on
// (yuushabu_buyinn on X). Copilot's /models lists a max_prompt_images
// for every model, but only Gemini's is held to (probed live: gpt-4.1,
// listed at 1, took 3; claude-sonnet-5, listed at 5, took 7), so the cap
// is learned from the refusal, which names it, by account and model.
//
// The refused request is sent again with its oldest images each replaced
// by a short text, as many as bring it to the cap (an image it carries
// again further on goes first, where it came earlier), and the account's
// later requests to that model are fitted the same way before they are
// sent (forwardOnce). The latest user message is kept whole; when its own
// images are over the cap, nothing is left out and the refusal goes on.
//
// Other vendors hold a request to a cap of their own and say so in their
// own words (#6849): 100milionseek "At most 8 images per request
// (MAX_IMAGES)." and Coarena "A request may carry at most 8 images.", the
// vendor's name first. Those are learned the same way, by provider and
// model, whatever id the user gave the provider: the words name the
// vendor. A cap the user sets on a provider (Provider.MaxImages, for all
// its models or for one) needs no refusal to know, and is fitted before
// the first request is sent; a refusal naming a smaller one still lowers
// it, since a request over it can't be served.
//
// A request under the cap, and one to a provider with none set or
// learned, goes as it came.

// imageCapRefusal is a vendor's words for a request over a model's image
// cap, with the cap; copilot is whether only a Copilot account's are
// read, its words naming no vendor.
type imageCapRefusal struct {
	words   *lazyre.RE
	copilot bool
}

// imageCapWords are the refusals a cap is learned from, as each vendor
// says them (the bytes are quoted above).
var imageCapWords = []imageCapRefusal{
	{lazyre.New(`(?i)too many images: maximum allowed for model \S+ is (\d+)`), true},
	{lazyre.New(`100milionseek: At most (\d+) images per request \(MAX_IMAGES\)`), false},
	{lazyre.New(`Coarena: A request may carry at most (\d+) images\.`), false},
}

// imageOverCap stands where an image over the cap was left out. It goes
// to the vendor, so it names neither magpie nor the vendor.
const imageOverCap = "[An earlier image was omitted here to keep this request within the model's image limit.]"

// copilotAccount is whether p is a Copilot account, whose refusal of too
// many images names no vendor.
func copilotAccount(p provider.Provider) bool {
	return p.Account != nil && p.Account.Agent == "copilot"
}

// learnedImageCap is the cap res's body b names for a request over it, as
// p's vendor words it, 0 for none.
func learnedImageCap(p provider.Provider, b []byte) int {
	words := string(wordsOf(b))
	for _, r := range imageCapWords {
		if r.copilot && !copilotAccount(p) {
			continue
		}
		if m := r.words.FindStringSubmatch(words); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

// under is whether a cap is kept under a key that starts with prefix.
func (c *bodyCaps) under(prefix string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.m {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

// imageCapKey is where the cap of p's model is kept.
func imageCapKey(p provider.Provider, body []byte) string {
	return p.ID + "\x00" + modelOf(body)
}

// userImageCap is the cap the user set on p for the model body asks
// for, 0 for none. The body names the vendor's id for it, and the user
// set it by the id magpie lists it under, which a model's own upstream
// name (SetModelID) can tell apart.
func userImageCap(p provider.Provider, body []byte) int {
	if len(p.MaxImages) == 0 {
		return 0
	}
	model := modelOf(body)
	if n := p.MaxImages[model]; n > 0 {
		return n
	}
	wires := settings.Shared().Wires()
	for id, n := range p.MaxImages {
		if id != "*" && n > 0 && provider.UpstreamNameIn(wires, p.ID, id) == model {
			return n
		}
	}
	return p.MaxImages["*"]
}

// imageCap is the most images one request to p may carry: the smaller of
// the user's own MaxImages and the cap a refusal taught that model, 0
// for neither.
func (s *Server) imageCap(p provider.Provider, body []byte) int {
	if len(p.MaxImages) == 0 && !s.imageCaps.under(p.ID+"\x00") {
		// most requests: nothing read of the body
		return 0
	}
	own, learned := userImageCap(p, body), s.imageCaps.of(imageCapKey(p, body))
	if own > 0 && (learned <= 0 || own < learned) {
		return own
	}
	return learned
}

// withinImageCap is body as it goes to p: its oldest images left out down
// to the cap p's model takes, when one is known and body is over it.
func (s *Server) withinImageCap(p provider.Provider, to provider.Protocol, path string, body []byte) []byte {
	if countsTokens(path) {
		return body
	}
	n := s.imageCap(p, body)
	if n <= 0 {
		return body
	}
	out, left, ok := withoutImagesOver(to, body, n)
	if ok && left > 0 {
		log.Printf("%s: the request has more images than the %d %s takes; %d earlier ones left out", p.Name, n, modelOf(body), left)
		return out
	}
	return body
}

// imageShownLater stands where an image was left out that the request
// carries again further on, byte for byte. It goes to the vendor, so it
// names neither magpie nor the vendor.
const imageShownLater = "[This image is omitted here; the same image appears again later in this conversation.]"

// withoutImagesOver is body brought to most images, the rest of it byte
// for byte as it came: first an image the conversation carries again
// later, byte for byte (a screenshot read twice, an image attached again),
// is left out where it came earlier for imageShownLater, and then, while
// still over, the oldest history images are left out for imageOverCap,
// with every earlier copy of one. The latest user message is kept whole.
// ok is false, and body as it came, when leaving every earlier one out
// doesn't bring it there.
func withoutImagesOver(to provider.Protocol, body []byte, most int) (out []byte, left int, ok bool) {
	all := bodyImages(to, body, 0, true, imageOverCap)
	if len(all) <= most {
		return body, 0, true
	}
	history, gone := map[int]bool{}, map[int]string{}
	for _, im := range bodyImages(to, body, 0, false, imageOverCap) {
		history[im.at], gone[im.at] = true, im.with
	}
	later := map[int]string{}
	for _, im := range bodyImages(to, body, 0, true, imageShownLater) {
		later[im.at] = im.with
	}
	// each image once, by its bytes, at the last place it is shown: an
	// image of the latest user message there, which is kept whole, and
	// each of those its own
	hashes := make([][32]byte, len(all))
	latest := 0
	inLatest := map[[32]byte]bool{}
	copiesOf := map[[32]byte][]int{}
	for i, im := range all {
		h := sha256.Sum256(body[im.at:im.end])
		hashes[i] = h
		if history[im.at] {
			copiesOf[h] = append(copiesOf[h], im.at)
		} else {
			latest++
			inLatest[h] = true
		}
	}
	// the history's images, oldest first by the last place each is shown
	var hist [][]int
	for i, im := range all {
		h := hashes[i]
		if c := copiesOf[h]; history[im.at] && !inLatest[h] && c[len(c)-1] == im.at {
			hist = append(hist, c)
		}
	}
	over := latest + len(hist) - most
	if over > len(hist) {
		return body, 0, false
	}
	with := map[int]string{}
	for h, c := range copiesOf {
		if inLatest[h] {
			for _, at := range c {
				with[at] = later[at]
			}
		}
	}
	for i, c := range hist {
		for j, at := range c {
			switch {
			case i < over:
				with[at] = gone[at]
			case j < len(c)-1:
				with[at] = later[at]
			}
		}
	}
	var b bytes.Buffer
	b.Grow(len(body))
	last := 0
	for _, im := range all {
		if w, ok := with[im.at]; ok {
			b.Write(body[last:im.at])
			b.WriteString(w)
			last = im.end
		}
	}
	b.Write(body[last:])
	return b.Bytes(), len(with), true
}

// refitImageCap is forward's answer to res, a refusal of body for too
// many images that names the cap (imageCapWords): the cap learned, and
// the request sent again with its oldest images left out, when that
// brings it under the cap it was sent at.
func (s *Server) refitImageCap(ctx context.Context, p provider.Provider, to provider.Protocol, path string, body []byte, in http.Header, res *http.Response) (*http.Response, error) {
	if res.StatusCode != http.StatusBadRequest || countsTokens(path) {
		return res, nil
	}
	b := peekError(res)
	n := learnedImageCap(p, b)
	if n <= 0 {
		return res, nil
	}
	was := s.imageCap(p, body)
	s.imageCaps.learn(imageCapKey(p, body), n)
	if n = s.imageCap(p, body); n == was {
		// sent fitted to it already: the refusal is about the latest
		// message's own images, and asking again changes nothing
		return res, nil
	}
	if _, left, ok := withoutImagesOver(to, body, n); !ok || left == 0 {
		return res, nil
	}
	log.Printf("%s refused a request to %s for its images (HTTP %d: %s); asking again with no more than %d", p.Name, modelOf(body), res.StatusCode, provider.APIError(b, ""), n)
	res.Body.Close()
	// forwardOnce fits it to the cap just learned
	return s.forwardOnce(ctx, p, to, path, body, in)
}
