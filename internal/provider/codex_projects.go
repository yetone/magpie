package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/steady"
)

// The Codex desktop app (26.1103 and on) keeps its sidebar's projects per
// signed-in ChatGPT identity, in $CODEX_HOME/.codex-global-state.json under
// electron-account-renderer-state-v1: {owner, inactive: {<hash>: {atoms,
// globals}}}. The owner's projects sit at the file's top level; each other
// identity's are parked under inactive, keyed by the sha256 hex of
// JSON.stringify([chatgpt_user_id, chatgpt_account_id]). When the app's
// account changes (magpie's switch: auth.json, then the app-server's
// restart) it parks the old identity's projects and loads the new one's,
// and an identity that never had the app open has none: the user's
// projects "disappear" on every switch to another account
// (Michael25651150 on X; #5497, #5602, #5702, #5874).
//
// The app reads the file only as it starts and holds it in memory, so a
// write while it runs is lost (it writes its own over it). So while the
// app is closed magpie gives each identity of its saved Codex logins that
// has no projects of its own the projects of the one that has the most
// (carryCodexProjects); the app then loads them on its next switch to
// that account. An identity that has projects, an emptied list included,
// is left alone. With no account at all (magpie's takeover, when Codex's
// provider takes no ChatGPT account) the app shows no projects, by its
// own rule; they come back with the account.

// codexRendererKey is the file's key for the per-identity state.
const codexRendererKey = "electron-account-renderer-state-v1"

// codexProjectKeys are the account-scoped globals that make up the
// projects the sidebar shows and what hangs off them, copied to an
// identity that has none. Accounts' own things (remote hosts, pins of
// ChatGPT projects, follow-ups) are not.
var codexProjectKeys = []string{
	"local-projects",
	"project-order",
	"selected-project",
	"project-appearances",
	"project-writable-roots",
	"project-files",
	"pinned-project-ids",
	"pinned-thread-ids",
	"sidebar-project-thread-orders",
	"sidebar-thread-metadata",
	"thread-project-assignments",
	"thread-project-membership-host-ids",
	"thread-writable-roots",
	"thread-workspace-root-hints",
	"thread-projectless-output-directories",
	"projectless-thread-ids",
	"active-workspace-roots",
	"electron-saved-workspace-roots",
	"electron-workspace-root-labels",
	"app-server-project-id-by-legacy-project-id-by-host",
	"app-server-projects-migration-by-host",
	"app-server-pending-project-deletions-by-host",
	"app-server-migrated-pinned-thread-ids-by-host",
}

// codexChatGPTProject is the id prefix of a project the app mirrors from
// a ChatGPT account's own projects: that account's, never carried.
const codexChatGPTProject = "g-p-"

// codexRendererOwner is the app's key for the identity an auth.json is
// signed in to, "" when it has no ChatGPT identity.
func codexRendererOwner(auth []byte) string {
	var a codexAuth
	if json.Unmarshal(auth, &a) != nil || a.AuthMode == "apikey" {
		return ""
	}
	claims := jwtClaims(a.Tokens.IDToken)
	user := claimString(claims, "https://api.openai.com/auth", "chatgpt_user_id")
	account := claimString(claims, "https://api.openai.com/auth", "chatgpt_account_id")
	if account == "" {
		account = a.Tokens.AccountID
	}
	if user == "" || account == "" {
		return ""
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // JSON.stringify's bytes
	if enc.Encode([]string{user, account}) != nil {
		return ""
	}
	sum := sha256.Sum256(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
	return hex.EncodeToString(sum[:])
}

// codexIdentity is what a backing in a project names.
type codexIdentity struct{ user, account string }

func codexIdentityOf(auth []byte) codexIdentity {
	var a codexAuth
	if json.Unmarshal(auth, &a) != nil {
		return codexIdentity{}
	}
	claims := jwtClaims(a.Tokens.IDToken)
	acc := claimString(claims, "https://api.openai.com/auth", "chatgpt_account_id")
	if acc == "" {
		acc = a.Tokens.AccountID
	}
	return codexIdentity{claimString(claims, "https://api.openai.com/auth", "chatgpt_user_id"), acc}
}

// codexGlobalStatePath is the Codex app's state file in home.
func codexGlobalStatePath(home string) string {
	return filepath.Join(home, ".codex-global-state.json")
}

// errCodexStateUnknown is a state file that isn't the shape this reads:
// nothing is written to it.
var errCodexStateUnknown = errors.New("the Codex app's state file isn't the shape magpie knows")

// carryCodexProjects gives every identity in auths (auth.json bytes) that
// has no projects in the Codex app's state file at home the projects of
// the identity that has the most, and says how many it gave them to. The
// app must be closed: it writes its own over the file. A file that can't
// be read or parsed, or has no per-identity state, is left as it is.
func carryCodexProjects(home string, auths [][]byte) (int, error) {
	path := codexGlobalStatePath(home)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil || top == nil {
		return 0, errCodexStateUnknown
	}
	rs, ok := top[codexRendererKey]
	if !ok {
		return 0, nil // an app before per-identity projects
	}
	var r struct {
		Owner    *string                    `json:"owner"`
		Inactive map[string]json.RawMessage `json:"inactive"`
	}
	if json.Unmarshal(rs, &r) != nil {
		return 0, errCodexStateUnknown
	}
	var keep map[string]json.RawMessage // the whole renderer object, unknown fields kept
	if json.Unmarshal(rs, &keep) != nil {
		return 0, errCodexStateUnknown
	}
	owner := ""
	if r.Owner != nil {
		owner = *r.Owner
	}
	snaps := map[string]map[string]json.RawMessage{} // hash → globals
	parked := map[string]map[string]json.RawMessage{}
	for h, v := range r.Inactive {
		var s map[string]json.RawMessage
		if json.Unmarshal(v, &s) != nil {
			return 0, errCodexStateUnknown
		}
		var g map[string]json.RawMessage
		if gs, ok := s["globals"]; ok && json.Unmarshal(gs, &g) != nil {
			return 0, errCodexStateUnknown
		}
		parked[h], snaps[h] = s, g
	}
	if owner != "" {
		snaps[owner] = top // the owner's globals are the file's own
	}
	// the source: the identity with the most projects, the owner first
	src, most := "", 0
	count := func(g map[string]json.RawMessage) int {
		var lp map[string]json.RawMessage
		if json.Unmarshal(g["local-projects"], &lp) != nil {
			return 0
		}
		n := 0
		for id := range lp {
			if !strings.HasPrefix(id, codexChatGPTProject) {
				n++
			}
		}
		return n
	}
	if owner != "" {
		src, most = owner, count(top)
	}
	hashes := slices.Sorted(maps.Keys(parked))
	for _, h := range hashes {
		if n := count(snaps[h]); n > most {
			src, most = h, n
		}
	}
	if most == 0 {
		return 0, nil
	}
	given := 0
	seen := map[string]bool{}
	for _, auth := range auths {
		h := codexRendererOwner(auth)
		if h == "" || seen[h] || h == src {
			continue
		}
		seen[h] = true
		g := snaps[h]
		if _, has := g["local-projects"]; has {
			continue // its own list, an emptied one too
		}
		carried := carriedProjects(snaps[src], codexIdentityOf(auth))
		if len(carried) == 0 {
			continue
		}
		if h == owner {
			for k, v := range carried {
				top[k] = v
			}
		} else {
			s := parked[h]
			if s == nil {
				s = map[string]json.RawMessage{"atoms": json.RawMessage("{}")}
			}
			if g == nil {
				g = map[string]json.RawMessage{}
			}
			for k, v := range carried {
				g[k] = v
			}
			gb, err := json.Marshal(g)
			if err != nil {
				return 0, err
			}
			s["globals"] = gb
			sb, err := json.Marshal(s)
			if err != nil {
				return 0, err
			}
			if r.Inactive == nil {
				r.Inactive = map[string]json.RawMessage{}
			}
			r.Inactive[h] = sb
		}
		given++
	}
	if given == 0 {
		return 0, nil
	}
	ib, err := json.Marshal(r.Inactive)
	if err != nil {
		return 0, err
	}
	keep["inactive"] = ib
	kb, err := json.Marshal(keep)
	if err != nil {
		return 0, err
	}
	top[codexRendererKey] = kb
	out, err := json.Marshal(top)
	if err != nil {
		return 0, err
	}
	target, err := edit.Target(path) // a symlink stays, its target written
	if err != nil {
		return 0, err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(target); err == nil {
		mode = fi.Mode().Perm()
	}
	// the file as it was, once, beside it: magpie's write can be undone
	backup := target + ".magpie-before-projects"
	if _, err := os.Lstat(backup); errors.Is(err, os.ErrNotExist) {
		if err := steady.WriteFile(backup, raw, mode); err != nil {
			return 0, err
		}
	}
	// the app opened meanwhile: its own state wins
	if now, err := os.ReadFile(target); err != nil || !bytes.Equal(now, raw) {
		return 0, nil
	}
	if err := steady.WriteFile(target, out, mode); err != nil {
		return 0, err
	}
	return given, nil
}

// carriedProjects is the project keys of globals g as an identity id gets
// them: a ChatGPT project's mirror and every tie to one left out, and a
// project's ties to another ChatGPT account's own project dropped.
func carriedProjects(g map[string]json.RawMessage, id codexIdentity) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, k := range codexProjectKeys {
		v, ok := g[k]
		if !ok {
			continue
		}
		if k == "local-projects" {
			v = ownLocalProjects(v, id)
		} else {
			v = withoutChatGPTProjects(v)
		}
		if v != nil {
			out[k] = v
		}
	}
	if _, ok := out["local-projects"]; !ok {
		return nil
	}
	return out
}

// codexBackings are a local project's ties to a ChatGPT account's project.
var codexBackings = []string{"chatGptBacking", "retiredChatGptBacking", "chatGptBackingCreation"}

func ownLocalProjects(v json.RawMessage, id codexIdentity) json.RawMessage {
	var lp map[string]map[string]json.RawMessage
	if json.Unmarshal(v, &lp) != nil {
		return nil
	}
	for pid, p := range lp {
		if strings.HasPrefix(pid, codexChatGPTProject) {
			delete(lp, pid)
			continue
		}
		dropped := false
		for _, b := range codexBackings {
			var who struct {
				AccountID string `json:"accountId"`
				UserID    string `json:"userId"`
			}
			if bv, ok := p[b]; ok && (json.Unmarshal(bv, &who) != nil || who.AccountID != id.account || who.UserID != id.user) {
				delete(p, b)
				dropped = true
			}
		}
		if dropped {
			delete(p, "chatGptBackingRecoveryPending")
		}
	}
	if len(lp) == 0 {
		return nil
	}
	b, err := json.Marshal(lp)
	if err != nil {
		return nil
	}
	return b
}

// withoutChatGPTProjects drops a ChatGPT project's id from a list, and an
// entry keyed by one or naming one from a record, one level down.
func withoutChatGPTProjects(v json.RawMessage) json.RawMessage {
	isGP := func(r json.RawMessage) bool {
		var s string
		return json.Unmarshal(r, &s) == nil && strings.HasPrefix(s, codexChatGPTProject)
	}
	var list []json.RawMessage
	if json.Unmarshal(v, &list) == nil {
		out := list[:0]
		for _, e := range list {
			if !isGP(e) {
				out = append(out, e)
			}
		}
		b, _ := json.Marshal(out)
		return b
	}
	var rec map[string]json.RawMessage
	if json.Unmarshal(v, &rec) == nil && rec != nil {
		for k, e := range rec {
			if strings.HasPrefix(k, codexChatGPTProject) || isGP(e) {
				delete(rec, k)
			}
		}
		b, _ := json.Marshal(rec)
		return b
	}
	if isGP(v) {
		return nil
	}
	return v
}

// codexProjectAuths is the auth.json of every Codex identity magpie knows
// on this computer: the one signed in now and each saved login's.
func codexProjectAuths() [][]byte {
	var out [][]byte
	if b, err := readAccountFile(codexAuthPath()); err == nil {
		out = append(out, b)
	}
	for _, l := range sharedLogins() {
		if l.Agent == "codex" && len(l.Auth) > 0 {
			out = append(out, l.Auth)
		}
	}
	return out
}

// CodexAppClosed carries the user's projects to each of Codex's accounts
// that has none in the Codex app (carryCodexProjects); the caller knows
// the app is closed: the agent package between its quit and its open.
func CodexAppClosed() {
	n, err := carryCodexProjects(codexHome(), codexProjectAuths())
	switch {
	case err != nil:
		log.Printf("codex: carrying the Codex app's projects to its other accounts: %v", err)
	case n > 0:
		log.Printf("codex: gave %d of Codex's accounts the Codex app's projects", n)
	}
	codexProjects.Lock()
	codexProjects.last = codexProjectsMark()
	codexProjects.Unlock()
}

// codexProjects is what codexProjectsStep last carried at: the state
// file's and the logins' change times and sizes, so an unchanged pair
// isn't read again nor the app's processes listed every look.
var codexProjects struct {
	sync.Mutex
	last  string
	tried time.Time
}

// codexProjectsEvery is how often a change is looked at while the app may
// be open: whether it is lists processes.
var codexProjectsEvery = 30 * time.Second

// codexProjectsMark names the state file and the logins as they are now.
func codexProjectsMark() string {
	var b strings.Builder
	for _, p := range []string{codexGlobalStatePath(codexHome()), codexAuthPath(), loginsPath()} {
		if fi, err := os.Stat(p); err == nil {
			b.WriteString(fi.ModTime().Format(time.RFC3339Nano))
			b.WriteByte('/')
			b.WriteString(fi.Name())
			b.WriteByte(' ')
		}
		b.WriteByte(';')
	}
	return b.String()
}

// codexProjectsStep is codexAppStep's carry: once the state file or the
// logins changed, and only while the Codex app is closed.
func codexProjectsStep(ctx context.Context) {
	codexProjects.Lock()
	mark := codexProjectsMark()
	skip := mark == codexProjects.last || time.Since(codexProjects.tried) < codexProjectsEvery
	if !skip {
		codexProjects.tried = time.Now()
	}
	codexProjects.Unlock()
	if skip {
		return
	}
	if open, known := codexAppOpenNow(ctx); !known || open {
		return // carried once it is closed: what it has open it writes over
	}
	CodexAppClosed()
}
