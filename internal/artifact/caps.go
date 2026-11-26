package artifact

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
)

// Capabilities (docs/subsystems/artifacts.md, "Page runtime"): what a page
// may do beyond static HTML, declared at publish as Claude's Artifact tool
// declares them (`capabilities: {db: {}, user: {}}`), so a page written
// for a Claude artifact runs here as written. A version keeps the
// declaration it was published with: a publish that leaves it out carries
// the one before forward, an empty object clears it, and any other object
// is the whole set.

// Capability names as Claude's runtime contract 0.2.99 has them. Those in
// ServedCaps are what this magpie serves; the rest are kept as declared and
// resolve null on the page, as an unserved capability does on claude.ai.
var capabilityNames = []string{"artifact", "assets", "comments", "db", "dictation", "downloads", "files", "mcp", "room", "sample", "self", "tts", "user"}

// ServedCaps are the capabilities the page runtime gives a page that declares
// them; permissions is built in and never declared. self is artifact's
// former name, and resolves the same namespace.
var ServedCaps = []string{"artifact", "assets", "comments", "db", "dictation", "downloads", "files", "mcp", "room", "sample", "self", "tts", "user"}

// Capabilities is a version's declaration, by name.
type Capabilities map[string]json.RawMessage

// Has is whether the declaration names capability name.
func (c Capabilities) Has(name string) bool { _, ok := c[name]; return ok }

// ParseCapabilities reads a declaration as a publish gives it: nil for
// none given (the one before is kept), an empty map for {} (cleared).
func ParseCapabilities(raw json.RawMessage) (Capabilities, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, errors.New("capabilities is an object of capability names, e.g. {\"db\": {}, \"user\": {}}")
	}
	c := Capabilities{}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		v := bytes.TrimSpace(m[name])
		if name == "permissions" {
			return nil, errors.New("permissions is built in: call it on the page, never declare it")
		}
		if !slices.Contains(capabilityNames, name) {
			return nil, fmt.Errorf("%q isn't a capability; they are %s", name, strings.Join(capabilityNames, ", "))
		}
		// downloads is declared as true; the rest as an object
		if string(v) == "true" || string(v) == "null" || len(v) == 0 {
			v = json.RawMessage("{}")
		}
		if v[0] != '{' {
			return nil, fmt.Errorf("capability %s is declared as an object, e.g. {\"%s\": {}}", name, name)
		}
		c[name] = append(json.RawMessage(nil), v...)
	}
	if v, ok := c["db"]; ok {
		if _, err := ParseDBConfig(v); err != nil {
			return nil, fmt.Errorf("capability db: %w", err)
		}
	}
	if v, ok := c["user"]; ok {
		if _, err := ParseUserConfig(v); err != nil {
			return nil, fmt.Errorf("capability user: %w", err)
		}
	}
	if v, ok := c["assets"]; ok {
		if _, err := ParseAssetsConfig(v); err != nil {
			return nil, fmt.Errorf("capability assets: %w", err)
		}
	}
	if v, ok := c["comments"]; ok {
		if _, err := ParseCommentsConfig(v); err != nil {
			return nil, fmt.Errorf("capability comments: %w", err)
		}
	}
	if v, ok := c["sample"]; ok {
		if _, err := ParseSampleConfig(v); err != nil {
			return nil, fmt.Errorf("capability sample: %w", err)
		}
	}
	if v, ok := c["mcp"]; ok {
		if _, err := ParseMCPConfig(v); err != nil {
			return nil, fmt.Errorf("capability mcp: %w", err)
		}
	}
	if v, ok := c["files"]; ok {
		if _, err := ParseFilesConfig(v); err != nil {
			return nil, fmt.Errorf("capability files: %w", err)
		}
	}
	if v, ok := c["room"]; ok {
		if _, err := ParseRoomConfig(v); err != nil {
			return nil, fmt.Errorf("capability room: %w", err)
		}
	}
	for _, name := range []string{"artifact", "self"} {
		if v, ok := c[name]; ok {
			if _, err := ParseArtifactConfig(v); err != nil {
				return nil, fmt.Errorf("capability %s: %w", name, err)
			}
		}
	}
	return c, nil
}

// UserConfig is the user capability's declaration.
type UserConfig struct {
	Scopes []string `json:"scopes,omitempty"`
}

// ParseUserConfig reads the user capability's declaration.
func ParseUserConfig(raw json.RawMessage) (*UserConfig, error) {
	u := &UserConfig{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, u); err != nil {
			return nil, errors.New("it is {} or {\"scopes\": [\"profile\", \"email\"]}")
		}
	}
	for _, s := range u.Scopes {
		if s != "profile" && s != "email" {
			return nil, fmt.Errorf("scope %q isn't one: they are profile and email", s)
		}
	}
	return u, nil
}

// AssetsConfig is the assets capability's declaration: upload "interact"
// lets people who can't edit upload too, on claude.ai; here the page's
// one viewer is its owner, who always may.
type AssetsConfig struct {
	Upload string `json:"upload,omitempty"`
}

// ParseAssetsConfig reads the assets capability's declaration.
func ParseAssetsConfig(raw json.RawMessage) (*AssetsConfig, error) {
	a := &AssetsConfig{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, a); err != nil {
			return nil, errors.New("it is {} or {\"upload\": \"interact\"}")
		}
	}
	if a.Upload != "" && a.Upload != "admin" && a.Upload != "interact" {
		return nil, fmt.Errorf("upload is \"interact\" or \"admin\", not %q", a.Upload)
	}
	return a, nil
}

// ArtifactConfig is the artifact capability's declaration (and self's,
// its former name): {publish: false} keeps the page from saving itself.
type ArtifactConfig struct {
	Publish *bool `json:"publish,omitempty"`
}

// ParseArtifactConfig reads the artifact capability's declaration.
func ParseArtifactConfig(raw json.RawMessage) (*ArtifactConfig, error) {
	a := &ArtifactConfig{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, a); err != nil {
			return nil, errors.New("it is {} or {\"publish\": false}")
		}
	}
	return a, nil
}

// CommentsConfig is the comments capability's declaration:
// composer_only grants only openComposer and anchorFor, which write
// nothing; customAnchors adds customAnchors().
type CommentsConfig struct {
	ComposerOnly  bool `json:"composer_only,omitempty"`
	CustomAnchors bool `json:"customAnchors,omitempty"`
}

// ParseCommentsConfig reads the comments capability's declaration.
func ParseCommentsConfig(raw json.RawMessage) (*CommentsConfig, error) {
	c := &CommentsConfig{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, c); err != nil {
			return nil, errors.New("it is {}, {\"composer_only\": true} or {\"customAnchors\": true}")
		}
	}
	return c, nil
}

// CommentsComposerOnly is whether the version's page declares comments
// as composer_only: it opens the composer and writes nothing.
func CommentsComposerOnly(c Capabilities) bool {
	raw, ok := c["comments"]
	if !ok {
		return false
	}
	cc, err := ParseCommentsConfig(raw)
	return err != nil || cc.ComposerOnly
}

// SampleConfig is the sample capability's declaration: {images: true}
// lets the page send images with its calls.
type SampleConfig struct {
	Images bool `json:"images,omitempty"`
}

// RoomConfig is the room capability's declaration: the event topics
// opened below Editors (admin), each to "interact" or "admin". Presence
// is anyone's in the room.
type RoomConfig struct {
	Topics map[string]string `json:"topics,omitempty"`
}

var (
	roomTopicRe = lazyre.New(`^[a-z][a-z0-9_.-]{0,47}$`)
	roomLevels  = []string{"interact", "admin", "owner"}
)

// ParseRoomConfig reads the room capability's declaration.
func ParseRoomConfig(raw json.RawMessage) (*RoomConfig, error) {
	c := &RoomConfig{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, c); err != nil {
			return nil, errors.New(`it is {} or {"topics": {"move": "interact"}}`)
		}
	}
	if len(c.Topics) > 64 {
		return nil, errors.New("it opens 64 topics at most")
	}
	for t, lvl := range c.Topics {
		if !roomTopicRe.MatchString(t) {
			return nil, fmt.Errorf("topic %q: a topic is ^[a-z][a-z0-9_.-]{0,47}$", t)
		}
		if !slices.Contains(roomLevels, lvl) {
			return nil, fmt.Errorf("topic %s: its level is \"interact\" or \"admin\", never %q (Viewers don't send)", t, lvl)
		}
	}
	return c, nil
}

// ParseSampleConfig reads the sample capability's declaration.
func ParseSampleConfig(raw json.RawMessage) (*SampleConfig, error) {
	c := &SampleConfig{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, c); err != nil {
			return nil, errors.New("it is {} or {\"images\": true}")
		}
	}
	return c, nil
}

// SampleImages is whether the version's page may send images with its
// sample calls.
func (c Capabilities) SampleImages() bool {
	raw, ok := c["sample"]
	if !ok {
		return false
	}
	s, err := ParseSampleConfig(raw)
	return err == nil && s.Images
}

// MCPConfig is the mcp capability's declaration: the manifest of the
// servers, and each one's tools, the page may call. A server is a
// Library MCP server's name, or host:<name> for one on this computer,
// which in magpie is every Library server too.
type MCPConfig struct {
	Servers []MCPServerDecl `json:"servers"`
}

// MCPServerDecl is one server of the mcp manifest.
type MCPServerDecl struct {
	Server string   `json:"server"`
	Tools  []string `json:"tools"`
}

const (
	mcpMostServers = 32
	mcpMostTools   = 128
)

func mcpToolName(t string) bool {
	if len(t) == 0 || len(t) > 128 {
		return false
	}
	for _, r := range t {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-') {
			return false
		}
	}
	return true
}

// ParseMCPConfig reads the mcp capability's declaration.
func ParseMCPConfig(raw json.RawMessage) (*MCPConfig, error) {
	c := &MCPConfig{}
	shape := errors.New(`it is {"servers": [{"server": "<name>", "tools": ["<tool>", ...]}]}`)
	if len(raw) == 0 || json.Unmarshal(raw, c) != nil {
		return nil, shape
	}
	if len(c.Servers) == 0 || len(c.Servers) > mcpMostServers {
		return nil, fmt.Errorf("servers lists 1 to %d servers: %w", mcpMostServers, shape)
	}
	seen := map[string]bool{}
	for _, s := range c.Servers {
		if s.Server == "" || len(s.Server) > 200 || strings.ContainsFunc(s.Server, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return nil, fmt.Errorf("server %q isn't a server's name", s.Server)
		}
		if seen[s.Server] {
			return nil, fmt.Errorf("server %q is listed twice", s.Server)
		}
		seen[s.Server] = true
		if len(s.Tools) == 0 || len(s.Tools) > mcpMostTools {
			return nil, fmt.Errorf("server %q lists 1 to %d tools", s.Server, mcpMostTools)
		}
		tools := map[string]bool{}
		for _, t := range s.Tools {
			if !mcpToolName(t) {
				return nil, fmt.Errorf("server %q: %q isn't a tool's name", s.Server, t)
			}
			if tools[t] {
				return nil, fmt.Errorf("server %q lists tool %q twice", s.Server, t)
			}
			tools[t] = true
		}
	}
	return c, nil
}

// Tools is the manifest's tools of server, nil if it isn't listed.
func (c *MCPConfig) Tools(server string) []string {
	for _, s := range c.Servers {
		if s.Server == server {
			return s.Tools
		}
	}
	return nil
}

// Allows is whether the manifest lists tool of server.
func (c *MCPConfig) Allows(server, tool string) bool {
	return slices.Contains(c.Tools(server), tool)
}

// MCP is the version's mcp manifest, nil if it declares none.
func (c Capabilities) MCP() *MCPConfig {
	raw, ok := c["mcp"]
	if !ok {
		return nil
	}
	m, err := ParseMCPConfig(raw)
	if err != nil {
		return nil
	}
	return m
}

// FilesConfig is the files capability's declaration. Claude's project is
// a Claude Code project's id; here it is a folder on this computer, given
// as an absolute path, and anything else (Claude's "chan_..." among them)
// is the folder the publishing agent worked in.
type FilesConfig struct {
	Project string `json:"project,omitempty"`
}

// ParseFilesConfig reads the files capability's declaration.
func ParseFilesConfig(raw json.RawMessage) (*FilesConfig, error) {
	c := &FilesConfig{}
	if len(raw) == 0 || json.Unmarshal(raw, c) != nil {
		return nil, errors.New(`it is {} for the folder the agent works in, or {"project": "<absolute folder>"}`)
	}
	if len(c.Project) > 4096 || strings.ContainsRune(c.Project, 0) {
		return nil, errors.New("project isn't a folder's path")
	}
	return c, nil
}

// FilesDir is the folder a version declaring files reads: its project
// when that is an absolute path, else cwd, the folder the publishing
// agent worked in. It is refused when there is none, when it is a disk's
// root or the home folder or holds it, and when it isn't a folder.
func FilesDir(c Capabilities, cwd string) (string, error) {
	raw, ok := c["files"]
	if !ok {
		return "", nil
	}
	fc, err := ParseFilesConfig(raw)
	if err != nil {
		return "", err
	}
	dir := fc.Project
	if !filepath.IsAbs(dir) {
		dir = cwd
	}
	if dir == "" || !filepath.IsAbs(dir) {
		return "", errors.New(`files reads the folder the agent works in, and magpie wasn't told yours: declare {"files": {"project": "<absolute folder>"}}`)
	}
	dir = filepath.Clean(dir)
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s isn't a folder on this computer", dir)
	}
	if filesTooWide(fi) {
		return "", fmt.Errorf("files reads one project's folder, not %s: name the project's own folder", dir)
	}
	return dir, nil
}

// filesTooWide is whether the folder is a disk's root, or the home folder
// or one that holds it: more than one project's files. It compares the
// folders themselves, so another spelling (a symlink, /USERS on a disk
// that ignores case) is caught too, and a home folder it can't find
// leaves every folder too wide.
func filesTooWide(dir os.FileInfo) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return true
	}
	for h := filepath.Clean(home); ; h = filepath.Dir(h) {
		if hi, err := os.Stat(h); err == nil && os.SameFile(dir, hi) {
			return true
		}
		if filepath.Dir(h) == h {
			return false
		}
	}
}

// PagePublishes is whether the version's page may publish itself: it
// declares artifact (or self) without {publish: false}. declared is
// whether it declares either at all.
func (c Capabilities) PagePublishes() (publishes, declared bool) {
	for _, name := range []string{"artifact", "self"} {
		raw, ok := c[name]
		if !ok {
			continue
		}
		declared = true
		if a, err := ParseArtifactConfig(raw); err == nil && (a.Publish == nil || *a.Publish) {
			publishes = true
		}
	}
	return publishes, declared
}

// Level is how far a viewer reaches into an artifact's data.
type Level int

const (
	LevelView Level = iota
	LevelInteract
	LevelAdmin
	LevelOwner
	// LevelSelf is held by nobody, the owner included
	LevelSelf
	levelUnset Level = -1
)

var levelNames = map[string]Level{"view": LevelView, "interact": LevelInteract, "admin": LevelAdmin, "owner": LevelOwner, "self": LevelSelf}

func (l Level) String() string {
	for k, v := range levelNames {
		if v == l {
			return k
		}
	}
	return "unset"
}

// Rule is one access rule of the db capability: the least level that
// reads and that writes at Path and below.
type Rule struct {
	Path  string `json:"path"`
	Read  string `json:"read,omitempty"`
	Write string `json:"write,omitempty"`
}

// DBConfig is the db capability's declaration.
type DBConfig struct {
	Rules []Rule `json:"rules,omitempty"`
}

const maxRules = 64

// ParseDBConfig reads and checks the db capability's declaration, as
// claude.ai checks it at publish.
func ParseDBConfig(raw json.RawMessage) (*DBConfig, error) {
	c := &DBConfig{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, c); err != nil {
			return nil, errors.New("it is {} or {\"rules\": [{\"path\": \"votes\", \"read\": \"view\", \"write\": \"owner\"}]}, where read and write are levels: view, interact, admin, owner or self")
		}
	}
	if len(c.Rules) > maxRules {
		return nil, fmt.Errorf("%d rules; the most is %d", len(c.Rules), maxRules)
	}
	seen := map[string]bool{}
	selfPrefix := map[string]bool{}
	for i, r := range c.Rules {
		segs := splitPath(r.Path)
		for j, s := range segs {
			if s == "{self}" {
				if j != len(segs)-1 || j == 0 {
					return nil, fmt.Errorf("rule %d: {self} is the last segment of a path below a prefix", i)
				}
				continue
			}
			if err := checkSegment(s); err != nil {
				return nil, fmt.Errorf("rule %d: %v", i, err)
			}
		}
		if len(segs) > maxSegments {
			return nil, fmt.Errorf("rule %d: a path has at most %d segments", i, maxSegments)
		}
		key := strings.Join(segs, "/")
		if seen[key] {
			return nil, fmt.Errorf("rule %d: a second rule for %q", i, r.Path)
		}
		seen[key] = true
		if len(segs) > 0 && segs[len(segs)-1] == "{self}" {
			selfPrefix[strings.Join(segs[:len(segs)-1], "/")] = true
		}
		for _, l := range []string{r.Read, r.Write} {
			if _, ok := levelNames[l]; l != "" && !ok {
				return nil, fmt.Errorf("rule %d: %q isn't a level; they are view, interact, admin, owner and self", i, l)
			}
		}
	}
	for i, r := range c.Rules {
		segs := splitPath(r.Path)
		key := strings.Join(segs, "/")
		self := len(segs) > 0 && segs[len(segs)-1] == "{self}"
		usesSelf := r.Read == "self" || r.Write == "self"
		switch {
		case usesSelf && (self || !selfPrefix[key]):
			return nil, fmt.Errorf("rule %d: self goes only on the rule at the prefix of a {self} rule", i)
		case r.Read == "self" && r.Write != "self":
			return nil, fmt.Errorf("rule %d: read self is valid only with write self", i)
		case selfPrefix[key] && (r.Read == "" || r.Write == ""):
			return nil, fmt.Errorf("rule %d: the rule at a {self} rule's prefix sets both read and write", i)
		case r.Read != "" && r.Write != "" && levelNames[r.Write] < levelNames[r.Read]:
			return nil, fmt.Errorf("rule %d: writing implies reading, so write is never below read", i)
		}
	}
	return c, nil
}

// splitPath is a path's segments; "" is the root, with none.
func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// Viewer is who reaches into an artifact's data: the person viewing its
// page, at their level, or an agent, which reaches everything.
type Viewer struct {
	ID    string
	Level Level
	Agent bool
}

// access is the least level that reads and that writes the document or
// collection at segs, and whether that is a sibling's {self} subtree
// closed to v.
type ruleSet struct {
	rules []compiledRule
}

type compiledRule struct {
	segs        []string
	read, write Level
	self        bool // ends in {self}
}

// defaultRules are those every declaration starts from: everyone admitted
// reads, interact writes, and each viewer's data/users/<id>/ is theirs.
func compileRules(c *DBConfig) *ruleSet {
	rs := &ruleSet{}
	declared := map[string]bool{}
	if c != nil {
		for _, r := range c.Rules {
			cr := compiledRule{segs: splitPath(r.Path), read: levelUnset, write: levelUnset}
			if r.Read != "" {
				cr.read = levelNames[r.Read]
			}
			if r.Write != "" {
				cr.write = levelNames[r.Write]
			}
			cr.self = len(cr.segs) > 0 && cr.segs[len(cr.segs)-1] == "{self}"
			declared[strings.Join(cr.segs, "/")] = true
			rs.rules = append(rs.rules, cr)
		}
	}
	if !declared["data/users"] {
		rs.rules = append(rs.rules, compiledRule{segs: []string{"data", "users"}, read: LevelSelf, write: LevelSelf})
	}
	if !declared["data/users/{self}"] {
		rs.rules = append(rs.rules, compiledRule{segs: []string{"data", "users", "{self}"}, read: levelUnset, write: levelUnset, self: true})
	}
	// shallow first, so a deeper rule overrides
	sort.SliceStable(rs.rules, func(i, j int) bool { return len(rs.rules[i].segs) < len(rs.rules[j].segs) })
	return rs
}

// levels is the least level reading and writing segs take for viewer id.
func (rs *ruleSet) levels(segs []string, id string) (read, write Level) {
	read, write = LevelView, LevelInteract
	// what was in force above a {self} rule's prefix, for the own subtree
	type held struct{ read, write Level }
	above := map[string]held{}
	for _, r := range rs.rules {
		if len(r.segs) > len(segs) {
			continue
		}
		match := true
		own := false
		for i, s := range r.segs {
			if s == "{self}" {
				own = id != "" && segs[i] == id
				if !own {
					match = false
				}
				break
			}
			if segs[i] != s {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		if r.self {
			// the viewer's own subtree: the {self} rule's levels, unset
			// ones from above its prefix, or the prefix's where it gives
			// more
			prefix := strings.Join(r.segs[:len(r.segs)-1], "/")
			base, ok := above[prefix]
			if !ok {
				base = held{read, write}
			}
			sr, sw := r.read, r.write
			if sr == levelUnset {
				sr = base.read
			}
			if sw == levelUnset {
				sw = base.write
			}
			read, write = min(read, sr), min(write, sw)
			continue
		}
		above[strings.Join(r.segs, "/")] = held{read, write}
		if r.read != levelUnset {
			read = r.read
		}
		if r.write != levelUnset {
			write = r.write
		}
		if write < read {
			write = read
		}
	}
	return read, write
}

// can is whether v reads (write false) or writes the path segs.
func (rs *ruleSet) can(v Viewer, segs []string, write bool) bool {
	if v.Agent {
		return true
	}
	r, w := rs.levels(segs, v.ID)
	need := r
	if write {
		need = w
	}
	return need != LevelSelf && v.Level >= need
}
