package artifact

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"

	"github.com/yetone/magpie/internal/lazyre"
)

// runtimeJS is the page runtime: window.claude.use, for a page written
// for claude.ai's artifact runtime (runtime.js).
//
//go:embed runtime.js
var runtimeJS []byte

// RuntimeConfig is what the page runtime is given about its page.
type RuntimeConfig struct {
	// API is where the page's store calls go: <page's folder>_api
	API   string `json:"api"`
	Token string `json:"token"`
	// Caps are the served capabilities the version declares, by name,
	// with their declarations
	Caps   map[string]any `json:"caps"`
	Viewer struct {
		ID string `json:"id"`
	} `json:"viewer"`
	// Version is the version the page is; Pinned, that its address names
	// it (<id>@<n>/), so a newer one doesn't reload it. Home is the
	// artifact's own address, where a publish from a pinned view goes.
	Version int    `json:"version,omitempty"`
	Pinned  bool   `json:"pinned,omitempty"`
	Home    string `json:"home,omitempty"`
	// Origin is magpie's origin, as the browser names it: the viewer
	// shell's, which the page's runtime speaks to (shell.js)
	Origin string `json:"origin,omitempty"`
	// Shared: the page is a shared copy, on usemagpie.ai. What it
	// declares of SharedCaps is served there (site/live.js), once the
	// link's page hands the frame a session; the rest resolves null.
	Shared bool `json:"shared,omitempty"`
}

// SharedCaps are the capabilities a shared copy is served on
// usemagpie.ai: its data, its room, and who is viewing.
var SharedCaps = []string{"db", "room", "user"}

// SharedRuntime is the runtime a shared copy of version v runs (v nil: a
// page that declares nothing): window.claude is there, so a page written
// for it runs; what it declares of SharedCaps waits for the link's
// session, and every other capability is null, so the page shows what it
// shows without them.
func SharedRuntime(v *Version) *RuntimeConfig {
	c := &RuntimeConfig{Caps: map[string]any{}, Shared: true}
	if v != nil {
		for _, name := range SharedCaps {
			if raw, ok := v.Capabilities[name]; ok {
				var decl any = map[string]any{}
				json.Unmarshal(raw, &decl)
				c.Caps[name] = decl
			}
		}
	}
	return c
}

// PageRuntime is the runtime config for artifact a's version n (0: the
// newest), its store calls going to api.
func PageRuntime(a *Artifact, n int, api string) (*RuntimeConfig, error) {
	tok, err := PageToken(a.ID)
	if err != nil {
		return nil, err
	}
	c := &RuntimeConfig{API: api, Token: tok, Caps: map[string]any{}}
	v := a.Version(n)
	if v != nil {
		c.Version, c.Pinned = v.N, n > 0
		for _, name := range ServedCaps {
			raw, ok := v.Capabilities[name]
			if !ok {
				continue
			}
			var decl any = map[string]any{}
			json.Unmarshal(raw, &decl)
			c.Caps[name] = decl
		}
	}
	if c.Viewer.ID, err = viewerID(); err != nil {
		return nil, err
	}
	return c, nil
}

// viewerID is the user's id on every artifact here: user.id() on the
// page, and the <id> of its data/users/<id>/.
func viewerID() (string, error) {
	k, err := pageSecret()
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, k)
	m.Write([]byte("artifact-viewer"))
	return "u_" + hex.EncodeToString(m.Sum(nil))[:24], nil
}

// LocalViewer is the person at this computer, as the store knows them:
// every artifact's owner.
func LocalViewer() (Viewer, error) {
	id, err := viewerID()
	return Viewer{ID: id, Level: LevelOwner}, err
}

var (
	headRe = lazyre.New(`(?i)<head(?:\s[^>]*)?>`)
	htmlRe = lazyre.New(`(?i)<html(?:\s[^>]*)?>`)
	docRe  = lazyre.New(`(?i)^\s*<!doctype[^>]*>`)
)

// WithRuntime is the served page with the runtime in it, ahead of the
// page's own scripts: right after <head>, else <html>, else the
// doctype, else first.
func WithRuntime(page []byte, c *RuntimeConfig) []byte {
	cfg, _ := json.Marshal(c) // escapes < > &, so no </script> ends it early
	var tag bytes.Buffer
	tag.WriteString("<script>")
	tag.Write(bytes.TrimRight(runtimeJS, "\n"))
	tag.WriteString("(")
	tag.Write(cfg)
	tag.WriteString(");</script>")
	at := 0
	for _, re := range []*lazyre.RE{headRe, htmlRe, docRe} {
		if loc := re.Regexp().FindIndex(page); loc != nil {
			at = loc[1]
			break
		}
	}
	out := make([]byte, 0, len(page)+tag.Len())
	out = append(out, page[:at]...)
	out = append(out, tag.Bytes()...)
	return append(out, page[at:]...)
}
