package gateway

// A resolution tier (1K, 2K, 4K) is how large an image is asked to be
// drawn, apart from its shape (skingstyle48487 on X: a 4K asked through the
// image MCP came back at 2K). Settings › Images › Resolution sets the one
// the image MCP asks when the agent names none (settings.ImageResolution),
// and an agent can name one in generate_image's resolution, or in size
// ("4K"). The images API takes it as resolution, OpenRouter's name for it,
// and each vendor is asked for it in its own field:
//
//   - Gemini's generateContent: imageConfig.imageSize "1K", "2K" or "4K"
//     (the Gemini 3 image models; a 2.x or Lite one draws at 1K alone);
//   - chat completions (OpenRouter, a relay's Gemini): image_config's
//     image_size beside its aspect_ratio, for a Gemini image model;
//   - OpenRouter's Image API: resolution;
//   - Ark's Seedream: size as the tier, or the doc's WIDTHxHEIGHT for the
//     ratio asked at that tier (each model takes the tiers its doc lists:
//     https://docs.volcengine.com/docs/82379/1541523);
//   - xAI's Imagine: resolution "1k" or "2k", its largest;
//   - MiniMax's H3-Image: area;
//   - an OpenAI-shaped images API (GPT Image 2 and later, a relay's
//     models): size as the WIDTHxHEIGHT the tier is at the ratio asked,
//     within GPT Image 2's 3840px edge and 8,294,400 pixels.
//
// A model whose sizes stop short of a tier (GPT Image 1, DALL·E, a 2.x
// Gemini) is asked at its own size; and a vendor that turns the tier away
// with a 400 is asked once more without it (drawTiered), so a tier set in
// Settings never stops an image being drawn. An explicit WIDTHxHEIGHT from
// the agent is the size, over any tier.

import (
	"bytes"
	"fmt"
	"image"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// tierOf is s as a resolution tier, "1K", "2K" or "4K", in any case; "" for
// anything else.
func tierOf(s string) string {
	switch t := strings.ToUpper(strings.TrimSpace(s)); t {
	case "1K", "2K", "4K":
		return t
	}
	return ""
}

// isPixels is whether size is WIDTHxHEIGHT.
func isPixels(size string) bool {
	w, h, ok := strings.Cut(strings.ToLower(strings.TrimSpace(size)), "x")
	if !ok {
		return false
	}
	x, err1 := strconv.Atoi(strings.TrimSpace(w))
	y, err2 := strconv.Atoi(strings.TrimSpace(h))
	return err1 == nil && err2 == nil && x > 0 && y > 0
}

// settleTier reads a drawing's tier: resolution as given, or a tier given
// as size ("4K"; d.shape() leaves it out where the tier is sent). A
// WIDTHxHEIGHT size is the size asked, and no tier goes beside it. A
// resolution that isn't a tier (or auto) is an error.
func (d *drawing) settleTier(resolution string) error {
	resolution = strings.TrimSpace(resolution)
	t := tierOf(resolution)
	if t == "" && resolution != "" && !strings.EqualFold(resolution, "auto") {
		return fmt.Errorf("resolution is 1K, 2K or 4K (or auto for the model's own), not %q", resolution)
	}
	if st := tierOf(d.Size); st != "" && t == "" && !strings.EqualFold(resolution, "auto") {
		t = st
	}
	if isPixels(d.Size) {
		t = ""
	}
	d.Tier = t
	return nil
}

// ImageTier is the resolution the image MCP asks the images API for: the
// one the agent named (arg: 1K, 2K, 4K, or auto for none), else, unless
// the agent gave a size of its own (WIDTHxHEIGHT, or a tier as size),
// Settings › Images › Resolution's. "" asks for none.
func ImageTier(arg, size string) (string, error) {
	arg = strings.TrimSpace(arg)
	switch {
	case strings.EqualFold(arg, "auto"):
		return "", nil
	case tierOf(arg) != "":
		return tierOf(arg), nil
	case arg != "":
		return "", fmt.Errorf("resolution is 1K, 2K or 4K (or auto for the model's own), not %q", arg)
	}
	if size = strings.TrimSpace(size); size != "" && !strings.EqualFold(size, "auto") && !strings.Contains(size, ":") {
		return "", nil
	}
	return tierOf(settings.Load().ImageResolution), nil
}

// tierEdge is the long side, in pixels, a tier names.
func tierEdge(t string) int {
	switch t {
	case "1K":
		return 1024
	case "2K":
		return 2048
	case "4K":
		return 4096
	}
	return 0
}

// bareModel is a model id as its maker names it: without models/ or a
// relay's vendor/ before it, in lower case.
func bareModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return m
}

// geminiImage is whether model is a Gemini image model (Nano Banana) that
// takes an imageSize past 1K.
func geminiImage(model string) bool {
	m := bareModel(model)
	return (strings.HasPrefix(m, "gemini-") || strings.HasPrefix(m, "nano-banana")) && takesImageSize(model)
}

// fixedSizes is whether model draws only at sizes of its own, none of
// them past 1536px: DALL·E's and GPT Image 1's (1, 1.5, mini).
func fixedSizes(model string) bool {
	m := bareModel(model)
	return strings.HasPrefix(m, "dall-e") || strings.HasPrefix(m, "gpt-image-1") || strings.HasPrefix(m, "chatgpt-image")
}

// gptImageAny is whether model is GPT Image 2 or later, which draws at any
// WIDTHxHEIGHT within its limits.
func gptImageAny(model string) bool {
	m := bareModel(model)
	rest, ok := strings.CutPrefix(m, "gpt-image-")
	if !ok {
		return false
	}
	n := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
	if n < 0 {
		n = len(rest)
	}
	v, err := strconv.Atoi(rest[:n])
	return err == nil && v >= 2
}

// ratioParts is a ratio "16:9" as its two sides; 1:1 for none.
func ratioParts(r string) (float64, float64) {
	a, b, ok := strings.Cut(strings.ReplaceAll(r, " ", ""), ":")
	x, err1 := strconv.ParseFloat(a, 64)
	y, err2 := strconv.ParseFloat(b, 64)
	if !ok || err1 != nil || err2 != nil || x <= 0 || y <= 0 {
		return 1, 1
	}
	return x, y
}

// tierPixels is WIDTHxHEIGHT for tier t at ratio r (1:1 for none): its long
// side the tier's, each side a multiple of 16. gpt holds it to GPT Image
// 2's limits: an edge of 3840 at most, 655,360 to 8,294,400 pixels.
func tierPixels(t, r string, gpt bool) string {
	a, b := ratioParts(r)
	edge := float64(tierEdge(t))
	if gpt {
		edge = min(edge, 3840)
	}
	w, h := edge, edge*b/a
	if b > a {
		w, h = edge*a/b, edge
	}
	round := math.Round
	if gpt {
		const most, least = 8294400, 655360
		if area := w * h; area > most {
			f := math.Sqrt(most / area)
			w, h, round = w*f, h*f, math.Floor
		} else if area < least {
			f := math.Sqrt(least / area)
			w, h, round = w*f, h*f, math.Ceil
		}
	}
	px := func(v float64) int { return max(16, int(round(v/16))*16) }
	return fmt.Sprintf("%dx%d", px(w), px(h))
}

// seedreamTiers are the tiers each Seedream model's size takes, as Ark's
// doc lists them, and arkTierSizes the WIDTHxHEIGHT its tables give each
// ratio at a tier: Seedream 4.0's for 1K, 4.0, 4.5 and 5.0 lite's for 2K
// and 4K, and 5.x pro and flash's own.
var (
	seedream40Tiers   = []string{"1K", "2K", "4K"}
	seedream45Tiers   = []string{"2K", "4K"}
	seedream5LiteTier = []string{"2K", "3K", "4K"}
	seedream5Tiers    = []string{"1K", "1.5K", "2K"}
)

var arkTierSizes = map[string]map[string]string{
	"1K": {"1:1": "1024x1024", "4:3": "1152x864", "3:4": "864x1152", "16:9": "1280x720", "9:16": "720x1280", "3:2": "1248x832", "2:3": "832x1248", "21:9": "1512x648"},
	"2K": arkRatioSizes,
	"4K": {"1:1": "4096x4096", "4:3": "4704x3520", "3:4": "3520x4704", "16:9": "5504x3040", "9:16": "3040x5504", "3:2": "4992x3328", "2:3": "3328x4992", "21:9": "6240x2656"},
}

// seedream5Sizes are Seedream 5.x pro and flash's sizes (5.5's too).
var seedream5Sizes = map[string]map[string]string{
	"1K": {"1:1": "1024x1024", "4:3": "1152x864", "3:4": "864x1152", "16:9": "1424x800", "9:16": "800x1424", "3:2": "1248x832", "2:3": "832x1248", "21:9": "1568x672"},
	"2K": {"1:1": "2048x2048", "4:3": "2368x1776", "3:4": "1776x2368", "16:9": "2816x1584", "9:16": "1584x2816", "3:2": "2496x1664", "2:3": "1664x2496", "21:9": "3136x1344"},
}

// seedreamTiersOf is the tiers a Seedream model takes; nil for one magpie
// doesn't know, which is asked the tier as given.
func seedreamTiersOf(model string) []string {
	m := strings.ReplaceAll(bareModel(model), ".", "-")
	switch {
	case !strings.Contains(m, "seedream"):
		return nil
	case strings.Contains(m, "seedream-4-5"):
		return seedream45Tiers
	case strings.Contains(m, "seedream-4"):
		return seedream40Tiers
	case strings.Contains(m, "seedream-5-0-lite") || strings.Contains(m, "seedream-5-lite"):
		return seedream5LiteTier
	case strings.Contains(m, "seedream-5"):
		return seedream5Tiers
	}
	return nil
}

// tierValue is a tier's size in K, for nearest.
func tierValue(t string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSuffix(t, "K"), 64)
	return v
}

// nearestTier is the largest of tiers at or under t, else the smallest.
func nearestTier(t string, tiers []string) string {
	if len(tiers) == 0 || slices.Contains(tiers, t) {
		return t
	}
	best := ""
	for _, x := range tiers {
		if tierValue(x) <= tierValue(t) && (best == "" || tierValue(x) > tierValue(best)) {
			best = x
		}
	}
	if best == "" {
		best = tiers[0]
	}
	return best
}

// isSeedream is whether model is a Seedream.
func isSeedream(model string) bool {
	return strings.Contains(bareModel(model), "seedream")
}

// seedreamSize is size and tier t as a Seedream model takes size: the tier
// it takes nearest t, as the tier with no ratio asked, or as the doc's
// WIDTHxHEIGHT for the ratio at that tier. size alone is arkSize's.
func seedreamSize(model, size, t string) (string, error) {
	if t == "" {
		return arkSize(size)
	}
	t = nearestTier(t, seedreamTiersOf(model))
	size = strings.ReplaceAll(strings.TrimSpace(size), " ", "")
	if size == "" || strings.EqualFold(size, "auto") || !strings.Contains(size, ":") {
		return t, nil
	}
	table := arkTierSizes
	if slices.Equal(seedreamTiersOf(model), seedream5Tiers) {
		table = seedream5Sizes
	}
	if px, ok := table[t][size]; ok {
		return px, nil
	}
	if _, ok := table["2K"][size]; ok {
		// a tier with no table of its own (1.5K, 3K): asked as the tier,
		// the ratio in the prompt as the doc says
		return t, nil
	}
	return arkSize(size)
}

// imagesAPISize is the size an OpenAI-shaped images API is sent for d:
// d's own, or with a tier, the WIDTHxHEIGHT the tier is at d's ratio
// for a model that takes it.
func imagesAPISize(p provider.Provider, model string, d drawing) string {
	if d.Tier == "" || p.IsRemoteMagpie() {
		return d.Size
	}
	m := strings.ToLower(model)
	switch {
	case isSeedream(model):
		if s, err := seedreamSize(model, d.shape(), d.Tier); err == nil {
			return s
		}
		return d.shape()
	case fixedSizes(model) || senseNovaDrawer(m):
		return d.shape()
	}
	r := aspectOf(d.shape())
	return tierPixels(d.Tier, r, gptImageAny(model) || drawsCodex(p) || drawsCopilot(p))
}

// grokImagesReq is d as xAI's Imagine API takes it, but its images: an
// aspect ratio and a resolution, not a size or a quality.
func grokImagesReq(model string, d drawing) map[string]any {
	req := map[string]any{"model": model, "prompt": d.Prompt, "n": d.N, "response_format": "b64_json"}
	if ar := aspectAmong(d.shape(), grokAspects); ar != "" {
		req["aspect_ratio"] = ar
	}
	if t := grokResolution(d.Tier); t != "" {
		req["resolution"] = t
	}
	return req
}

// grokResolution is tier t as xAI's Imagine takes resolution: 1k or 2k,
// its largest; "" for none.
func grokResolution(t string) string {
	switch t {
	case "1K":
		return "1k"
	case "2K", "4K":
		return "2k"
	}
	return ""
}

// chatImageConfig is the image_config a chat model is asked to draw with:
// the ratio, and for a Gemini image model the image_size (a tier, or what
// a pixel size asks past 1K), as OpenRouter's and relays' chat completions
// take them. nil for none.
func chatImageConfig(model string, d drawing) map[string]string {
	c := map[string]string{}
	if r := aspectOf(d.shape()); r != "" {
		c["aspect_ratio"] = r
	}
	t := d.Tier
	if t == "" {
		t = imageSizeOf(d.Size)
	}
	if t != "" && geminiImage(model) {
		c["image_size"] = t
	}
	if len(c) == 0 {
		return nil
	}
	return c
}

// tierRefused is whether a vendor's refusal of a drawing asked at a tier
// may be of the tier: a 400 or 422, which is asked once more at the
// model's own size.
func tierRefused(d drawing, code int, err error) bool {
	return d.Tier != "" && err != nil && (code == 400 || code == 422)
}

// PixelsOf is an image's WIDTHxHEIGHT, read from its header; "" when it
// can't be read.
func PixelsOf(img []byte) string {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(img))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return ""
	}
	return fmt.Sprintf("%dx%d", cfg.Width, cfg.Height)
}

// PixelsSaid is the line a tool result says the images' sizes in, so what
// came back can be checked against the resolution asked (tier): which
// sizes, and when one is well under the tier, that the model drew it at
// its own. "" for no sizes.
func PixelsSaid(sizes []string, tier string) string {
	var known []string
	small := false
	for _, px := range sizes {
		if px == "" {
			continue
		}
		known = append(known, px)
		w, h, _ := strings.Cut(px, "x")
		x, _ := strconv.Atoi(w)
		y, _ := strconv.Atoi(h)
		if e := tierEdge(tier); e > 0 && max(x, y) < e*3/4 {
			small = true
		}
	}
	if len(known) == 0 {
		return ""
	}
	what := "The image is"
	if len(sizes) > 1 {
		what = "The images are"
	}
	line := fmt.Sprintf("%s %s pixels", what, strings.Join(known, ", "))
	if small {
		line += fmt.Sprintf(", under the %s asked: the model drew at the largest size it gives", tier)
	} else if tier != "" {
		line += fmt.Sprintf(" (%s asked)", tier)
	}
	return line + ".\n"
}

// AskedTier is the tier an image MCP asks a drawing at, for PixelsSaid:
// the one sent as resolution, else one given as size ("4K").
func AskedTier(tier, size string) string {
	if tier != "" {
		return tier
	}
	return tierOf(size)
}
