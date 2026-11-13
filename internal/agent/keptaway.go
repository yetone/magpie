package agent

import (
	"bytes"
	"net/url"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
)

// synced runs an agent's sync, which brings magpie's models in its files up
// to date, not where they are reached: a file whose magpie the user pointed
// at a magpie on another machine (a NAS's, or this one's LAN address, coeo91
// on Discord) keeps that address and its key, as ZCode's and WorkBuddy's
// writers do on their own (zcodeAddress). Without it, every start and
// update of magpie (SyncCatalog) put the agent back on 127.0.0.1.
//
// What the filter (kept.away) needs of this magpie (the gateway's address, the LAN key)
// is read here, before the filter is on, never by the filter: every file
// written through edit while it is on passes through it, on whichever
// goroutine and under whichever lock wrote it. settings.Save, the window's
// size kept after a resize, writes under the lock settings.Load takes, and
// a filter reading the settings waited on it for good (ylorn on Discord).
func synced(sync func() error) error {
	return edit.Filtered(keeping().away, sync)
}

// kept is what kept.away knows of this magpie, read before a sync's writes:
// the gateway's address (gateway.URL) and the LAN sharing key.
type kept struct{ gateway, lan string }

func keeping() kept { return kept{gateway.URL(), access.LANSecret()} }

// ours says v, with any "Bearer " before it, is a key magpie gives an
// agent: gateway.Token, one that names the agent (gateway.TokenFor), or the
// LAN sharing key (ourKey's, as read before).
func (k kept) ours(v string) bool {
	v = strings.TrimPrefix(v, "Bearer ")
	return v == gateway.Token || v != "" && v == k.lan || strings.HasPrefix(v, gateway.Token+"-")
}

// magpieFile says path, a file edit is about to write (a link's target,
// edit.Target), is in magpie's own folder, as it is named or where its
// links lead: with ~/.config a link into a dotfiles repo, settings.json is
// written at the repo's path (ylorn on Discord). A file not there yet has
// its folder's links followed.
func magpieFile(path string) bool {
	sep := string(filepath.Separator)
	dir := filepath.Dir(provider.Path())
	dirs := []string{dir}
	if r, err := filepath.EvalSymlinks(dir); err == nil && r != dir {
		dirs = append(dirs, r)
	}
	paths := []string{path}
	if r, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		paths = append(paths, filepath.Join(r, filepath.Base(path)))
	}
	for _, d := range dirs {
		for _, p := range paths {
			if strings.HasPrefix(p, d+sep) {
				return true
			}
		}
	}
	return false
}

// originRE is an http(s) URL's scheme and host, as it is written in a file.
var originRE = lazyre.New(`https?://[A-Za-z0-9.\-_\[\]:%]+`)

// keyLineRE is a key and its value as a JSON, JSONC, YAML, TOML or .env
// file writes it: "apiKey": "x", api_key = "x", apiKey: x, KEY=x; not one
// whose value is an object or a list. The value is group 4 when quoted with
// ", 5 with ', 6 bare.
var keyLineRE = lazyre.New(`(["']?)([A-Za-z0-9_\-]+)(["']?)[ \t]*[:=][ \t]*(?:"((?:[^"\\\n]|\\.)*)"|'([^'\n]*)'|([^\s"',{}\[\]#][^\n"',}\]#]*))`)

// keyName says a key of that name holds what an agent sends the gateway to
// be let in: apiKey, api_key, MAGPIE_QWEN_API_KEY, experimental_bearer_token,
// Authorization (not max_tokens).
func keyName(name string) bool {
	n := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(name))
	return n == "key" || strings.HasSuffix(n, "apikey") || strings.HasSuffix(n, "token") || strings.HasSuffix(n, "authorization")
}

type keyValue struct {
	start, end int    // the value's bytes, quotes left out
	value      string // as written
}

// keyValues are the values of the keys keyName takes in b.
func keyValues(b []byte) []keyValue {
	var out []keyValue
	for _, m := range keyLineRE.FindAllSubmatchIndex(b, -1) {
		if string(b[m[2]:m[3]]) != string(b[m[6]:m[7]]) || !keyName(string(b[m[4]:m[5]])) {
			continue
		}
		for g := 8; g <= 12; g += 2 {
			if m[g] >= 0 {
				v := strings.TrimRight(string(b[m[g]:m[g+1]]), " \t\r")
				out = append(out, keyValue{m[g], m[g] + len(v), v})
				break
			}
		}
	}
	return out
}

// away is data, a sync's write of the file at path, with magpie kept
// where the file had it on another machine. That is a file without the
// gateway's own address (gateway.URL), with an address on another machine
// and none a WSL distro was seen at (a distro's way to Windows is magpie's
// to move, wslMovedFrom). Where the write brings the gateway's address in
// place of one on another machine, that one stays; where it brings magpie's
// own key in place of another, in a file that had none of magpie's keys,
// the other stays, with the path every URL there had before magpie's (a
// reverse proxy's /magpie). An address on this machine's loopback
// (another port, localhost) is magpie's own and follows the gateway, but
// for a local proxy's route, one with a path of its own where nothing but
// magpie has answered (#1985); with more than one
// address or key gone, which went where can't be told, and the write is
// left as it is. magpie's own files are never the user's pointing
// (magpieFile). It takes no lock a writer may hold: k is read before.
func (k kept) away(path string, data []byte) []byte {
	if magpieFile(path) {
		return data
	}
	g := k.gateway
	was, err := edit.Read(path)
	if err != nil || len(was) == 0 {
		return data
	}
	if p := chainPrefix(was, g); p != "" {
		// behind a local proxy that names the gateway in its path
		// (billion-context's /bili/, chainsToGateway): kept in front of it
		pg := []byte(p + g)
		return bytes.ReplaceAll(bytes.ReplaceAll(data, pg, []byte(g)), []byte(g), pg)
	}
	if bytes.Contains(was, []byte(g)) {
		return data
	}
	elsewhere, local := false, false
	for _, o := range originRE.FindAll(was, -1) {
		if wslHost(string(o)) {
			return data
		}
		elsewhere = elsewhere || onAnotherMachine(string(o))
	}
	for _, u := range urlRE.FindAll(was, -1) {
		local = local || loopbackWithPath(string(u))
	}
	if !elsewhere && !local {
		return data
	}
	out := data
	if bytes.Contains(data, []byte(g)) {
		away := ""
		for _, o := range originRE.FindAll(was, -1) {
			if s := string(o); bytes.Count(data, o) < bytes.Count(was, o) && s != away {
				if away != "" {
					return data
				}
				away = s
			}
		}
		p := awayPath(was, data, g, away)
		// on this machine, a local proxy's route (#1985): one with a path
		// of its own before magpie's, where magpie answers or hasn't been
		// asked yet; without one it is magpie's own on an older port
		if !onAnotherMachine(away) {
			if !loopbackWithPath(away + p) {
				return data
			}
			if ok, known := magpieAt(away + p); known && !ok {
				return data
			}
		}
		out = replaceOrigin(data, g, away+p)
	} else if !elsewhere {
		return data
	}

	// the key: the one that went with the address, in place of magpie's
	theirs, before, after := "", keyCounts(was), keyCounts(out)
	for v, n := range before {
		if k.ours(v) {
			return out
		}
		if after[v] < n {
			if theirs != "" {
				return out
			}
			theirs = v
		}
	}
	if theirs == "" {
		return out
	}
	kvs := keyValues(out)
	for i := len(kvs) - 1; i >= 0; i-- {
		kv := kvs[i]
		if !k.ours(kv.value) {
			continue
		}
		v := theirs
		if strings.HasPrefix(kv.value, "Bearer ") {
			v = "Bearer " + theirs
		}
		out = append(out[:kv.start:kv.start], append([]byte(v), out[kv.end:]...)...)
	}
	return out
}

// chainPrefix is what goes before the gateway's address g at every place
// was has it, when that is one local proxy naming g as its upstream
// (chainsToGateway: http://127.0.0.1:18787/bili/), "" otherwise: g
// somewhere on its own, or behind two different proxies.
func chainPrefix(was []byte, g string) string {
	prefix := ""
	n := 0
	for _, u := range urlRE.FindAll(was, -1) {
		s := string(u)
		i := strings.Index(s, "/"+g)
		if i < 0 {
			continue
		}
		p := s[:i+1]
		if !chainsToGateway(p+g, g) || prefix != "" && p != prefix {
			return ""
		}
		prefix = p
		n++
	}
	if prefix == "" || bytes.Count(was, []byte(g)) != bytes.Count(was, []byte(prefix+g)) || n == 0 {
		return ""
	}
	return prefix
}

// urlRE is an http(s) URL with its path, as it is written in a file.
var urlRE = lazyre.New(`https?://[^\s"'<>,{}\[\]\\]+`)

// awayPath is the path before magpie's own that the address away keeps in
// was, the file before a sync wrote data with the gateway's address g: the
// /magpie of https://ai.corp.example/magpie/v1, a reverse proxy's (#1985),
// where data has g/v1. It is the shortest that every one of was's URLs at
// away starts with; "" when one is at away itself, or none fits.
func awayPath(was, data []byte, g, away string) string {
	var tails []string
	for _, d := range urlRE.FindAll(data, -1) {
		if t, ok := strings.CutPrefix(strings.TrimSuffix(string(d), "/"), g); ok {
			tails = append(tails, t)
		}
	}
	var ws []string
	for _, w := range urlRE.FindAll(was, -1) {
		if s := strings.TrimSuffix(string(w), "/"); s == away || strings.HasPrefix(s, away+"/") {
			ws = append(ws, s)
		}
	}
	best, found := "", false
	for _, w := range ws {
		for _, t := range tails {
			p, ok := strings.CutSuffix(strings.TrimPrefix(w, away), t)
			if !ok || found && len(p) >= len(best) {
				continue
			}
			all := true
			for _, o := range ws {
				all = all && (o == away+p || strings.HasPrefix(o, away+p+"/"))
			}
			if all {
				best, found = p, true
			}
		}
	}
	return best
}

// keyCounts are the values of the keys keyName takes in b, "Bearer " left
// out, and how many times each is there.
func keyCounts(b []byte) map[string]int {
	n := map[string]int{}
	for _, kv := range keyValues(b) {
		n[strings.TrimPrefix(kv.value, "Bearer ")]++
	}
	return n
}

// replaceOrigin is b with each from (an origin) that isn't part of a longer
// one (http://127.0.0.1:3425 in http://127.0.0.1:34250) replaced by to.
func replaceOrigin(b []byte, from, to string) []byte {
	var out []byte
	for {
		i := bytes.Index(b, []byte(from))
		if i < 0 {
			return append(out, b...)
		}
		j := i + len(from)
		out = append(out, b[:i]...)
		if j < len(b) && (b[j] >= '0' && b[j] <= '9' || b[j] == '.' || b[j] == '-') {
			out = append(out, from...)
		} else {
			out = append(out, to...)
		}
		b = b[j:]
	}
}

// wslHost says base's host is one a WSL distro reached Windows at, now or
// before.
func wslHost(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	h := u.Hostname()
	wsl.Lock()
	defer wsl.Unlock()
	for _, d := range wsl.seen {
		if d != nil && (d.Gateway == h || d.host() == h || slices.Contains(d.Was, h)) {
			return true
		}
	}
	return false
}
