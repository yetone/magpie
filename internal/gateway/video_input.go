package gateway

import (
	"cmp"
	"fmt"
	"mime"
	"path"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// A video a client gives the model (#6135, #4101) is a File part whose
// media type is video/*: Messages' video block (MiniMax's Anthropic
// endpoint takes one), Chat's video_url, a Chat file or Responses
// input_file of a video, and Gemini's inlineData or fileData of one. It
// goes to the upstream in that API's own shape: a video block on
// Messages, video_url on Chat, inlineData on Gemini's API. Responses has
// none, so a video in the latest turn translated there is turned away
// (unportableVideo), as is one for a model whose source says it takes no
// video (videoInput) or a group with no member that takes one. A video of
// an earlier turn to such a model is told of in words (attachmentText), as
// an image of an earlier turn is to a text-only model.
//
// Before, Messages' video block and Chat's video_url had no case in the
// parsers and were left out: the vendor answered 200 for the words alone
// and the model said it saw no video, or made one up.

// isVideo is whether p is a video the client gave.
func isVideo(p Part) bool {
	return p.Kind == File && strings.HasPrefix(strings.ToLower(p.MediaType), "video/")
}

// videoPart is a video by its data: URL, bare base64 or address, with the
// media type the client named, else the one its data: URL or its name's
// extension says, else video/mp4; false for one with nothing in it.
func videoPart(src, mediaType, name string) (Part, bool) {
	if src == "" {
		return Part{}, false
	}
	if strings.HasPrefix(src, "data:") {
		p := imagePart(src)
		if p.Data == "" {
			return Part{}, false
		}
		return Part{Kind: File, MediaType: cmp.Or(mediaType, p.MediaType, videoByName(name), "video/mp4"), Data: p.Data}, true
	}
	if strings.Contains(src, "://") {
		at, _, _ := strings.Cut(src, "?")
		return Part{Kind: File, MediaType: cmp.Or(mediaType, videoByName(cmp.Or(name, at)), "video/mp4"), URL: src}, true
	}
	return Part{Kind: File, MediaType: cmp.Or(mediaType, videoByName(name), "video/mp4"), Data: src}, true
}

// videoByName is the video media type a file's name says, "" for none.
func videoByName(name string) string {
	mt, _, _ := strings.Cut(mime.TypeByExtension(strings.ToLower(path.Ext(name))), ";")
	if strings.HasPrefix(mt, "video/") {
		return mt
	}
	return ""
}

// videoInput is whether the model p serves as model takes video, from the
// kinds its source listed (Entry.Inputs: a plugin's capabilities, a model
// list's input_modalities, models.dev); nil when it listed none.
func videoInput(id, model string) *bool {
	for _, e := range provider.Served() {
		if e.Group == "" && e.Provider.ID == id && e.Model == model {
			if e.Inputs == nil {
				return nil
			}
			takes := slices.Contains(e.Inputs, "video")
			return &takes
		}
	}
	return nil
}

// noVideo is whether the model is known to take no video.
func noVideo(id, model string) bool {
	v := videoInput(id, model)
	return v != nil && !*v
}

// currentVideo is whether the request's latest user message gives the
// model a video, the one it is asked about now.
func currentVideo(r *Request) bool {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if m := r.Messages[i]; m.Role == "user" {
			return slices.ContainsFunc(m.Parts, isVideo)
		}
	}
	return false
}

// hasCurrentVideo is currentVideo of a body as the client sent it. Most
// bodies name no video at all and are not parsed.
func hasCurrentVideo(from provider.Protocol, body []byte) bool {
	if !bodyHas(body, "video") {
		return false
	}
	req, err := parse(from, body)
	return err == nil && currentVideo(req)
}

// videoRefused is what the agent is told of a video for a model, or a
// group, that takes none.
func videoRefused(model, group string) string {
	if group != "" {
		return fmt.Sprintf("model %q does not support video input: no model of the routing group %s takes video. Add a member that takes video, or send frames as images", model, group)
	}
	return fmt.Sprintf("model %q does not support video input: its model list doesn't say it takes video. Send frames as images, or pick a model that takes video", model)
}

// unportableVideo is the 400 for a video in the latest turn translated to
// an API that has no way to carry one; "" when it can be carried.
func unportableVideo(to provider.Protocol, r *Request) string {
	if to != provider.Responses || !currentVideo(r) {
		return ""
	}
	return "this request gives the model a video, which the Responses API has no way to carry; send it on Chat, Messages or Gemini's API, or send frames as images"
}

// chatVideo is a video as Chat's video_url part, the shape the Chat APIs
// that take video read (DashScope's Qwen, Kimi, Zhipu, OpenRouter, vLLM).
func chatVideo(p Part) map[string]any {
	return map[string]any{"type": "video_url", "video_url": map[string]any{"url": dataURL(p)}}
}

// videoBlock is a video as a Messages video block, the image block's shape
// under another type: MiniMax's Anthropic endpoint reads it.
func videoBlock(p Part) aBlock {
	b := imageBlock(p)
	b.Type = "video"
	return b
}

// videoFileType is the video media type of a Responses input_file, by its
// data: URL's type or its name; "" for a file that is not a video.
func videoFileType(src, name string) string {
	if rest, ok := strings.CutPrefix(src, "data:"); ok {
		mt, _, _ := strings.Cut(rest, ";")
		mt, _, _ = strings.Cut(mt, ",")
		if strings.HasPrefix(strings.ToLower(mt), "video/") {
			return strings.ToLower(mt)
		}
		if mt != "" {
			return ""
		}
	}
	at, _, _ := strings.Cut(src, "?")
	if strings.Contains(src, "://") {
		return cmp.Or(videoByName(name), videoByName(at))
	}
	return videoByName(name)
}

// anyVideo is whether any message of the request gives a video.
func anyVideo(r *Request) bool {
	for _, m := range r.Messages {
		if slices.ContainsFunc(m.Parts, isVideo) {
			return true
		}
	}
	return false
}
