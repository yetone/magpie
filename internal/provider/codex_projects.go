package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/appdir"
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
// app is closed magpie gives every identity, those of its saved Codex
// logins and those the file has, one list (shareCodexProjects): each
// identity's projects together, a folder once, less what the user
// removed. A project a look saw on an identity and the next look doesn't
// was removed there, and goes from every identity rather than coming
// back from another (codex-projects.json keeps what was seen). The app
// then loads the list on its next switch to any account. Projects added
// or removed while the app runs reach the other accounts once it is
// closed. With no account at all (magpie's takeover, when Codex's
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

// codexProjectsRecordPath is what shareCodexProjects last saw of each
// identity's projects, so a project the user removed is told apart from
// one an account never had.
func codexProjectsRecordPath() string {
	return appdir.Join(filepath.Dir(Path()), "codex-projects.json")
}

// codexProjectsRecord is that file: per identity hash, each of its local
// projects' id and the folders it is (codexRootKey).
type codexProjectsRecord struct {
	Identities map[string]map[string]string `json:"identities"`
}

// readCodexProjectsRecord is the record at p; none yet is an empty one,
// and one that can't be read or parsed is an error: what was removed is
// unknown then, so nothing is written.
func readCodexProjectsRecord(p string) (map[string]map[string]string, []byte, error) {
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]map[string]string{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var r codexProjectsRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", p, err)
	}
	if r.Identities == nil {
		r.Identities = map[string]map[string]string{}
	}
	return r.Identities, b, nil
}

// codexRootKey is the folders a local project is, "" for none: two
// projects of one folder are one project to the user, whatever their ids
// (the app gives a new project a random id on each account).
func codexRootKey(p json.RawMessage) string {
	var v struct {
		RootPaths []string `json:"rootPaths"`
	}
	if json.Unmarshal(p, &v) != nil || len(v.RootPaths) == 0 {
		return ""
	}
	rs := slices.Clone(v.RootPaths)
	for i, r := range rs {
		rs[i] = filepath.Clean(r)
	}
	slices.Sort(rs)
	return strings.Join(rs, "\x00")
}

// codexLocalProjects is the local projects of globals g by id, the
// ChatGPT projects' mirrors left out; has is whether g has a list at all.
func codexLocalProjects(g map[string]json.RawMessage) (lp map[string]json.RawMessage, has bool, err error) {
	v, ok := g["local-projects"]
	if !ok {
		return nil, false, nil
	}
	var all map[string]json.RawMessage
	if json.Unmarshal(v, &all) != nil {
		return nil, true, errCodexStateUnknown
	}
	lp = map[string]json.RawMessage{}
	for id, p := range all {
		if !strings.HasPrefix(id, codexChatGPTProject) {
			lp[id] = p
		}
	}
	return lp, true, nil
}

// shareCodexProjects gives every Codex identity in the Codex app's state
// file at home, and every one of auths (auth.json bytes), one list of
// projects: every identity's, less those the user removed. A project the
// record (at recordPath) saw on an identity that no longer has it was
// removed there, and goes from every identity, as do other projects of
// its folders, unless the user added it again on some identity since. It
// says how many identities it changed. The app must be closed: it writes
// its own over the file. A file that can't be read or parsed, or has no
// per-identity state, is left as it is, as is everything when the record
// can't be read.
func shareCodexProjects(home string, auths [][]byte, recordPath string) (int, error) {
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
	prev, prevRaw, err := readCodexProjectsRecord(recordPath)
	if err != nil {
		return 0, err
	}
	owner := ""
	if r.Owner != nil {
		owner = *r.Owner
	}
	snaps := map[string]map[string]json.RawMessage{} // hash → globals
	parked := map[string]map[string]json.RawMessage{}
	for h, v := range r.Inactive {
		var s map[string]json.RawMessage
		if json.Unmarshal(v, &s) != nil || s == nil {
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
	// every identity, the one signed in first: its copy of a project wins
	ident := map[string]codexIdentity{}
	var order []string
	if owner != "" {
		order = append(order, owner)
	}
	for _, h := range slices.Sorted(maps.Keys(parked)) {
		if h != owner {
			order = append(order, h)
		}
	}
	for _, a := range auths {
		h := codexRendererOwner(a)
		if h == "" {
			continue
		}
		if _, ok := ident[h]; !ok {
			ident[h] = codexIdentityOf(a)
		}
		if !slices.Contains(order, h) {
			order = append(order, h)
		}
	}

	// what the user removed since the record, and added
	cur := map[string]map[string]json.RawMessage{}
	for _, h := range order {
		lp, has, err := codexLocalProjects(snaps[h])
		if err != nil {
			return 0, err
		}
		if has {
			cur[h] = lp
		}
	}
	removedIDs, removedRoots := map[string]bool{}, map[string]bool{}
	addedIDs, addedRoots := map[string]bool{}, map[string]bool{}
	for h, lp := range cur {
		was, ok := prev[h]
		if !ok {
			continue // never seen: nothing it has was added or removed
		}
		for id, root := range was {
			if _, still := lp[id]; !still {
				removedIDs[id] = true
				if root != "" {
					removedRoots[root] = true
				}
			}
		}
		for id, p := range lp {
			if _, had := was[id]; !had {
				addedIDs[id] = true
				if k := codexRootKey(p); k != "" {
					addedRoots[k] = true
				}
			}
		}
	}
	gone := func(id string, p json.RawMessage) bool {
		if addedIDs[id] {
			return false // added again since: the user's latest word
		}
		k := codexRootKey(p)
		if k != "" && addedRoots[k] {
			return false
		}
		return removedIDs[id] || (k != "" && removedRoots[k])
	}

	// the shared list, in the order the identities show them
	union := map[string]json.RawMessage{}
	var seq []string
	for _, h := range order {
		lp := cur[h]
		var po []string
		_ = json.Unmarshal(snaps[h]["project-order"], &po)
		ids := slices.Clone(po)
		ids = append(ids, slices.Sorted(maps.Keys(lp))...)
		for _, id := range ids {
			p, ok := lp[id]
			if !ok || gone(id, p) {
				continue
			}
			if _, ok := union[id]; !ok {
				union[id] = p
				seq = append(seq, id)
			}
		}
	}

	changed := 0
	after := map[string]map[string]string{} // the record to keep
	for _, h := range order {
		if h == "" {
			continue
		}
		g := snaps[h]
		ng, did, err := sharedGlobals(g, h, ident[h], union, seq, gone, order, snaps)
		if err != nil {
			return 0, err
		}
		if lp, has, _ := codexLocalProjects(ng); has {
			rec := map[string]string{}
			for id, p := range lp {
				rec[id] = codexRootKey(p)
			}
			after[h] = rec
		}
		if !did {
			continue
		}
		changed++
		if h == owner {
			maps.Copy(top, ng)
			for k := range top {
				if _, ok := ng[k]; !ok && slices.Contains(codexProjectKeys, k) {
					delete(top, k)
				}
			}
			continue
		}
		s := parked[h]
		if s == nil {
			s = map[string]json.RawMessage{"atoms": json.RawMessage("{}")}
		}
		gb, err := json.Marshal(ng)
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
	saveRecord := func() error {
		b, err := json.Marshal(codexProjectsRecord{Identities: after})
		if err != nil || bytes.Equal(b, prevRaw) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(recordPath), 0o700); err != nil {
			return err
		}
		return steady.WriteFile(recordPath, b, 0o600)
	}
	if changed == 0 {
		return 0, saveRecord()
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
	// the app opened meanwhile: its own state wins, and the record waits
	// for the next look
	if now, err := os.ReadFile(target); err != nil || !bytes.Equal(now, raw) {
		return 0, nil
	}
	if err := steady.WriteFile(target, out, mode); err != nil {
		return 0, err
	}
	return changed, saveRecord()
}

// sharedGlobals is globals g of identity h (who: its ChatGPT identity,
// zero when magpie has no login for it) with the shared list: its own
// projects that are gone dropped with every tie to them, and each project
// of union it hasn't got added with the ties the other identities have
// to it. A project of a folder it has already is that one. did is whether
// anything changed; g itself is not.
func sharedGlobals(g map[string]json.RawMessage, h string, who codexIdentity, union map[string]json.RawMessage, seq []string, gone func(string, json.RawMessage) bool, order []string, snaps map[string]map[string]json.RawMessage) (map[string]json.RawMessage, bool, error) {
	var all map[string]json.RawMessage // its own, ChatGPT mirrors too
	if v, ok := g["local-projects"]; ok && json.Unmarshal(v, &all) != nil {
		return nil, false, errCodexStateUnknown
	}
	if all == nil {
		all = map[string]json.RawMessage{}
	}
	removed := map[string]bool{}
	roots := map[string]string{} // folders → its project
	for id, p := range all {
		if strings.HasPrefix(id, codexChatGPTProject) {
			continue
		}
		if gone(id, p) {
			removed[id] = true
			delete(all, id)
			continue
		}
		if k := codexRootKey(p); k != "" {
			roots[k] = id
		}
	}
	as := map[string]string{} // another identity's project id → its own here
	for _, id := range seq {
		if _, ok := all[id]; ok {
			continue
		}
		p := union[id]
		if k := codexRootKey(p); k != "" {
			if mine, ok := roots[k]; ok {
				as[id] = mine
				continue
			}
			roots[k] = id
		}
		all[id] = projectFor(p, who)
		as[id] = id
	}
	added := 0
	for id, to := range as {
		if id == to {
			added++
		}
	}
	if len(removed) == 0 && added == 0 {
		return g, false, nil
	}
	out := maps.Clone(g)
	if out == nil {
		out = map[string]json.RawMessage{}
	}
	lb, err := json.Marshal(all)
	if err != nil {
		return nil, false, err
	}
	out["local-projects"] = lb
	for _, k := range codexProjectKeys {
		if k == "local-projects" {
			continue
		}
		var v any
		if raw, ok := g[k]; ok && json.Unmarshal(raw, &v) != nil {
			continue // a value this doesn't read stays as it is
		}
		had := v != nil
		v = codexPrune(v, removed, 0)
		for _, s := range order {
			if s == h {
				continue
			}
			raw, ok := snaps[s][k]
			if !ok {
				continue
			}
			var sv any
			if json.Unmarshal(withoutChatGPTProjects(raw), &sv) != nil {
				continue
			}
			v = codexGraft(v, sv, as, 0)
		}
		switch {
		case v == nil && had:
			delete(out, k)
		case v != nil:
			b, err := json.Marshal(v)
			if err != nil {
				return nil, false, err
			}
			out[k] = b
		}
	}
	return out, true, nil
}

// codexBackings are a local project's ties to a ChatGPT account's project.
var codexBackings = []string{"chatGptBacking", "retiredChatGptBacking", "chatGptBackingCreation"}

// projectFor is project p as identity id gets it: a tie to another
// ChatGPT account's own project dropped (the app refuses one: "belongs to
// a different ChatGPT account").
func projectFor(p json.RawMessage, id codexIdentity) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(p, &m) != nil {
		return p
	}
	dropped := false
	for _, b := range codexBackings {
		var who struct {
			AccountID string `json:"accountId"`
			UserID    string `json:"userId"`
		}
		if bv, ok := m[b]; ok && (json.Unmarshal(bv, &who) != nil || who.AccountID == "" || who.AccountID != id.account || who.UserID != id.user) {
			delete(m, b)
			dropped = true
		}
	}
	if !dropped {
		return p
	}
	delete(m, "chatGptBackingRecoveryPending")
	b, err := json.Marshal(m)
	if err != nil {
		return p
	}
	return b
}

// codexPrune drops every mention of a removed project from v: an id in a
// list, an entry keyed by one or naming one, one level down too, and v
// itself when it names one.
func codexPrune(v any, removed map[string]bool, depth int) any {
	if len(removed) == 0 {
		return v
	}
	switch t := v.(type) {
	case string:
		if removed[t] {
			return nil
		}
	case []any:
		out := []any{}
		for _, e := range t {
			if s, ok := e.(string); ok && removed[s] {
				continue
			}
			out = append(out, e)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, e := range t {
			if s, ok := e.(string); removed[k] || (ok && removed[s]) {
				continue
			}
			if depth == 0 {
				if m, ok := e.(map[string]any); ok {
					e = codexPrune(m, removed, 1)
				}
			}
			out[k] = e
		}
		return out
	}
	return v
}

// codexGraft adds to v what src says of the projects in as (another
// identity's id → this one's): an id in a list, an entry keyed by one or
// naming one, one level down too. What v says already stays.
func codexGraft(v, src any, as map[string]string, depth int) any {
	switch s := src.(type) {
	case []any:
		d, ok := v.([]any)
		if v != nil && !ok {
			return v
		}
		for _, e := range s {
			id, ok := e.(string)
			to, mapped := as[id]
			if !ok || !mapped || slices.Contains(d, any(to)) {
				continue
			}
			d = append(d, to)
		}
		if d == nil {
			return v
		}
		return d
	case map[string]any:
		d, ok := v.(map[string]any)
		if v != nil && !ok {
			return v
		}
		out := maps.Clone(d)
		if out == nil {
			out = map[string]any{}
		}
		for k, e := range s {
			if to, ok := as[k]; ok {
				if _, has := out[to]; !has {
					out[to] = codexRename(e, as)
				}
				continue
			}
			if id, ok := e.(string); ok {
				if to, ok := as[id]; ok {
					if _, has := out[k]; !has {
						out[k] = to
					}
				}
				continue
			}
			if m, ok := e.(map[string]any); ok && depth == 0 {
				sub, _ := out[k].(map[string]any)
				if _, has := out[k]; has && sub == nil {
					continue
				}
				if g, _ := codexGraft(sub, m, as, 1).(map[string]any); len(g) > 0 {
					out[k] = g
				}
			}
		}
		if v == nil && len(out) == 0 {
			return nil
		}
		return out
	}
	return v
}

// codexRename is e with a project id in as, or a list of them, named as
// this identity names it.
func codexRename(e any, as map[string]string) any {
	switch t := e.(type) {
	case string:
		if to, ok := as[t]; ok {
			return to
		}
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = codexRename(x, as)
		}
		return out
	}
	return e
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

// CodexAppClosed gives all of Codex's accounts one list of projects in
// the Codex app (shareCodexProjects); the caller knows the app is closed:
// the agent package between its quit and its open.
func CodexAppClosed() {
	n, err := shareCodexProjects(codexHome(), codexProjectAuths(), codexProjectsRecordPath())
	switch {
	case err != nil:
		log.Printf("codex: sharing the Codex app's projects between its accounts: %v", err)
	case n > 0:
		log.Printf("codex: shared the Codex app's projects with %d of Codex's accounts", n)
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
