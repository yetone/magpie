package gateway

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"strconv"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
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
// by a short text, as many as bring it to the cap, and the account's
// later requests to that model are fitted the same way before they are
// sent (forwardOnce). The latest user message is kept whole; when its own
// images are over the cap, nothing is left out and the refusal goes on.
// A request under the cap, and every other vendor's, goes as it came.

// imageCapWords are Copilot's words for a request over a model's image
// cap, with the cap.
var imageCapWords = lazyre.New(`(?i)too many images: maximum allowed for model \S+ is (\d+)`)

// imageOverCap stands where an image over the cap was left out. It goes
// to the vendor, so it names neither magpie nor the vendor.
const imageOverCap = "[An earlier image was omitted here to keep this request within the model's image limit.]"

// imageCapped is whether p's refusals of too many images are learned:
// Copilot's alone say so.
func imageCapped(p provider.Provider) bool {
	return p.Account != nil && p.Account.Agent == "copilot"
}

// imageCapKey is where the cap of p's model is kept.
func imageCapKey(p provider.Provider, body []byte) string {
	return p.ID + "\x00" + modelOf(body)
}

// withinImageCap is body as it goes to p: its oldest images left out
// down to the cap p's model refused more images over, when one is known
// and body is over it.
func (s *Server) withinImageCap(p provider.Provider, to provider.Protocol, path string, body []byte) []byte {
	if !imageCapped(p) || countsTokens(path) {
		return body
	}
	n := s.imageCaps.of(imageCapKey(p, body))
	if n <= 0 {
		return body
	}
	out, left, ok := withoutImagesOver(to, body, n)
	if ok && left > 0 {
		log.Printf("%s: the request has more images than the %d %s takes; the %d oldest left out", p.Name, n, modelOf(body), left)
		return out
	}
	return body
}

// withoutImagesOver is body with its oldest history images replaced by
// imageOverCap, as many as bring it to most images; the rest of it byte
// for byte as it came. ok is false, and body as it came, when leaving
// every earlier one out doesn't bring it there.
func withoutImagesOver(to provider.Protocol, body []byte, most int) (out []byte, left int, ok bool) {
	over := len(bodyImages(to, body, 0, true, imageOverCap)) - most
	if over <= 0 {
		return body, 0, true
	}
	imgs := bodyImages(to, body, 0, false, imageOverCap)
	if len(imgs) < over {
		return body, 0, false
	}
	var b bytes.Buffer
	b.Grow(len(body))
	last := 0
	for _, im := range imgs[:over] {
		b.Write(body[last:im.at])
		b.WriteString(im.with)
		last = im.end
	}
	b.Write(body[last:])
	return b.Bytes(), over, true
}

// refitImageCap is forward's answer to res, Copilot's refusal of body for
// too many images: the cap learned, and the request sent again with its
// oldest images left out, when that brings it to the cap.
func (s *Server) refitImageCap(ctx context.Context, p provider.Provider, to provider.Protocol, path string, body []byte, in http.Header, res *http.Response) (*http.Response, error) {
	if res.StatusCode != http.StatusBadRequest || !imageCapped(p) || countsTokens(path) {
		return res, nil
	}
	b := peekError(res)
	m := imageCapWords.FindStringSubmatch(string(wordsOf(b)))
	if m == nil {
		return res, nil
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return res, nil
	}
	n = s.imageCaps.learn(imageCapKey(p, body), n)
	if _, left, ok := withoutImagesOver(to, body, n); !ok || left == 0 {
		return res, nil
	}
	log.Printf("%s refused a request to %s for its images (HTTP %d: %s); asking again with no more than %d", p.Name, modelOf(body), res.StatusCode, provider.APIError(b, ""), n)
	res.Body.Close()
	// forwardOnce fits it to the cap just learned
	return s.forwardOnce(ctx, p, to, path, body, in)
}
