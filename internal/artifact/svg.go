package artifact

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// SanitizeSVG is an SVG upload as it is stored, as claude.ai's assets
// keep one: scripts, event handlers, <foreignObject>, <style> and
// <link>, animation, a <use> of another file, and javascript: and data:
// URLs are taken out, and what is left is written out again. It is
// served as an image, so nothing is left in it that runs or reaches
// out once it is opened as a document.
func SanitizeSVG(body []byte) ([]byte, error) {
	d := xml.NewDecoder(bytes.NewReader(body))
	d.Strict = true
	d.Entity = xml.HTMLEntity
	var out bytes.Buffer
	var open []string // the elements written and not yet closed
	skip := 0         // how deep inside an element taken out
	roots := 0
	for {
		tok, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, refuse(ErrAssetType, fmt.Errorf("the SVG isn't well-formed XML: %w", err))
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if skip > 0 {
				skip++
				continue
			}
			if len(open) == 0 {
				roots++
				if roots > 1 || !strings.EqualFold(t.Name.Local, "svg") {
					return nil, refuse(ErrAssetType, errors.New("an SVG asset is one <svg> document"))
				}
			}
			if svgDropped(t.Name.Local) {
				skip = 1
				continue
			}
			name := svgName(t.Name)
			out.WriteString("<" + name)
			for _, a := range t.Attr {
				if svgAttrDropped(t.Name.Local, a) {
					continue
				}
				out.WriteString(" " + svgName(a.Name) + `="`)
				xml.EscapeText(&out, []byte(a.Value))
				out.WriteString(`"`)
			}
			out.WriteString(">")
			open = append(open, name)
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			name := svgName(t.Name)
			if len(open) == 0 || open[len(open)-1] != name {
				return nil, refuse(ErrAssetType, fmt.Errorf("the SVG's </%s> closes nothing open", name))
			}
			open = open[:len(open)-1]
			out.WriteString("</" + name + ">")
		case xml.CharData:
			if skip > 0 || len(open) == 0 {
				continue
			}
			xml.EscapeText(&out, t)
		}
		// comments, processing instructions and a DOCTYPE are left out
	}
	if roots == 0 {
		return nil, refuse(ErrAssetType, errors.New("an SVG asset is one <svg> document"))
	}
	if len(open) > 0 || skip > 0 {
		return nil, refuse(ErrAssetType, errors.New("the SVG ends with elements still open"))
	}
	return out.Bytes(), nil
}

func svgName(n xml.Name) string {
	if n.Space == "" {
		return n.Local
	}
	return n.Space + ":" + n.Local
}

// the elements taken out, with all they hold
var svgDroppedNames = []string{"script", "foreignobject", "style", "link", "animate", "animatemotion", "animatetransform", "animatecolor", "set", "discard", "iframe", "embed", "object", "audio", "video", "handler", "listener", "meta", "base"}

func svgDropped(local string) bool {
	l := strings.ToLower(local)
	for _, n := range svgDroppedNames {
		if l == n {
			return true
		}
	}
	return false
}

// svgAttrDropped: an event handler, a link that runs or carries a file
// inside it, or a <use> of something outside this file.
func svgAttrDropped(elem string, a xml.Attr) bool {
	l := strings.ToLower(a.Name.Local)
	if strings.HasPrefix(l, "on") {
		return true
	}
	v := strings.ToLower(strings.Map(func(r rune) rune {
		// a browser reads "java\tscript:" and "&#10;javascript:" as the scheme
		if r <= ' ' || r == 0x7f {
			return -1
		}
		return r
	}, a.Value))
	if strings.Contains(v, "javascript:") || strings.Contains(v, "vbscript:") {
		return true
	}
	if l == "href" || l == "src" {
		if strings.HasPrefix(v, "data:") {
			return true
		}
		if strings.EqualFold(elem, "use") && !strings.HasPrefix(v, "#") {
			return true
		}
	}
	// url(data:…) in a style or a presentation attribute
	if strings.Contains(v, "url(data:") || strings.Contains(v, "url('data:") || strings.Contains(v, `url("data:`) {
		return true
	}
	return false
}
