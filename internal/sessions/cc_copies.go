package sessions

import (
	"bytes"
	"cmp"
	"maps"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
)

// Claude Code copies a conversation into a new session file when it forks
// one (`--resume --fork-session`, /branch) or moves it to a background
// session: every message of it again, under the same uuids, with the new
// session's id. A client that forks a session for each turn it runs leaves
// one file per fork, and each was listed as a session of its own: the same
// title, the same messages and size, ten rows of one conversation (Neo,
// #feedback). A file whose last message another file of the same agent
// holds is such a copy: everything it says is in that file too, so it is
// not a row of its own. A fork that went on with a turn of its own has a
// last message no other file holds, and is a branch (ccFamily). A copy
// made after a compaction (/compact, or
// /rewind's summarize) holds only the compaction's summary and what
// follows it, so it starts with the summary, not with the conversation's
// first message; it is judged with the files that hold that summary.

// ccCopies is, for each session (by key) that is a copy, the key of the
// session it is a copy of, which no other holds. Only the files cache has
// read are judged: what has not been read is no one's copy.
func ccCopies(files []file) map[string]string {
	copies, _ := ccFamily(files)
	return copies
}

// A fork that went on is still the conversation it was forked from: a
// client that forks a compacted session for each task it runs (`claude -p
// --resume <id> --fork-session`, the Agent SDK's forkSession) leaves a
// file per task that starts with the compaction's summary and ends with
// that task's turn, and each was a row of the same title that opened on
// the summary and one turn (Neo, #feedback, after v0.1.1809). Such a file
// is a branch of the session that holds the conversation from its start:
// it is listed under that session (ParentSession, Role "branch"), not
// beside it, and reads from the conversation's first line (TranscriptOf).

// ccFamily is ccCopies, and for each session (by key) that is a branch,
// the key of the session that holds its conversation from the start, the
// one of its files no copy and no branch: the one that holds the others'
// first message as they don't hold its (it was compacted after they
// started; they start from its summary), else the one a branch names in
// forkedFrom (/branch), else the earliest, then the lowest key.
func ccFamily(files []file) (copies, branches map[string]string) {
	// files start alike when they start with the same message, or one
	// starts with a compaction's summary the other holds: a copy made
	// after /compact holds only what follows the summary
	// a file is held by its place in files, not copied: ~200 bytes for
	// each Claude Code file on each poll, and a group made for each of
	// them though only one of two or more is judged, were ~140 MB in 80s
	// of polls (#2278)
	var all []ccOne
	up := []int{} // each file's group, by its index in all (union-find)
	find := func(i int) int {
		for up[i] != i {
			up[i] = up[up[i]]
			i = up[i]
		}
		return i
	}
	at := map[string]int{} // a message a file starts with or holds a summary as
	for k := range files {
		f := &files[k]
		if !f.main || !parserFor(f.agent).claude {
			continue
		}
		st := cache[f.path]
		if st == nil || st.more().Root == "" || st.more().Leaf == "" {
			continue
		}
		i := len(all)
		all = append(all, ccOne{f, st})
		up = append(up, i)
		in := f.agent + "\x00" + f.m().wsl + "\x00"
		for _, u := range append([]string{st.more().Root}, st.more().Compacts...) {
			if j, ok := at[in+u]; ok {
				up[find(i)] = find(j)
			} else {
				at[in+u] = i
			}
		}
	}
	size := make([]int, len(all))
	for i := range all {
		size[find(i)]++
	}
	groups := map[int][]ccOne{}
	for i, o := range all {
		if r := find(i); size[r] > 1 {
			if groups[r] == nil {
				groups[r] = make([]ccOne, 0, size[r])
			}
			groups[r] = append(groups[r], o)
		}
	}
	var out map[string]string
	used := map[string]bool{}
	ccFamiliesLoad()
	for _, g := range groups {
		if len(g) < 2 {
			continue
		}
		sig := ccGroupSig(g)
		if kept := ccFamilyKept(sig); kept != nil {
			if len(kept.Copies) > 0 {
				if out == nil {
					out = map[string]string{}
				}
				maps.Copy(out, kept.Copies)
			}
			if len(kept.Branches) > 0 {
				if branches == nil {
					branches = map[string]string{}
				}
				maps.Copy(branches, kept.Branches)
			}
			continue
		}
		var gc, gb map[string]string // what this group comes to
		holds := func(b ccOne, uuid string) bool {
			used[b.f.path] = true
			return b.st.more().Leaf == uuid || ccUUIDsOf(*b.f).has(uuid)
		}
		// before reports whether b is kept over a: the one that holds
		// the other's first message too (a copy made after a compaction
		// starts with its summary, and the conversation holds what came
		// before), then the newer file, then the later key, so of two
		// files that hold each other one stays
		before := func(b, a ccOne) bool {
			if ab, ba := holds(a, b.st.more().Root), holds(b, a.st.more().Root); ab != ba {
				return ba
			}
			if c := b.f.mod.Compare(a.f.mod); c != 0 {
				return c > 0
			}
			return cmp.Compare(b.f.key, a.f.key) > 0
		}
		hidden := make([]bool, len(g))
		for i, a := range g {
			for j, b := range g {
				// a file that holds what came before the other's start (a
				// compaction the fork started after) is not that one's
				// copy: the conversation's file a fork went on from
				if i == j || a.f.key == b.f.key || !holds(b, a.st.more().Leaf) || !holds(b, a.st.more().Root) {
					continue
				}
				if !holds(a, b.st.more().Leaf) || before(b, a) {
					hidden[i] = true
					break
				}
			}
		}
		for i, a := range g {
			if !hidden[i] {
				continue
			}
			// the file it is kept in: one that is not a copy itself and
			// holds its last message, as the longest of them does
			for j, b := range g {
				if !hidden[j] && i != j && holds(b, a.st.more().Leaf) {
					if gc == nil {
						gc = map[string]string{}
					}
					gc[a.f.key] = b.f.key
					break
				}
			}
		}
		// what is left went on apart: one is the conversation, and the
		// others are its branches
		head := -1
		for i, a := range g {
			if hidden[i] {
				continue
			}
			if head < 0 || headFirst(a, g[head], holds) {
				head = i
			}
		}
		for i, a := range g {
			if hidden[i] || i == head || a.f.key == g[head].f.key {
				continue
			}
			if gb == nil {
				gb = map[string]string{}
			}
			gb[a.f.key] = g[head].f.key
		}
		ccFamilyKeepOut(sig, gc, gb)
		if len(gc) > 0 {
			if out == nil {
				out = map[string]string{}
			}
			maps.Copy(out, gc)
		}
		if len(gb) > 0 {
			if branches == nil {
				branches = map[string]string{}
			}
			maps.Copy(branches, gb)
		}
	}
	ccFamiliesSave()
	// a file judged here that no longer starts like another lets its
	// uuids go; another caller's files are left as they are
	for _, o := range all {
		if !used[o.f.path] {
			delete(ccUUIDMemo, o.f.path)
		}
	}
	return out, branches
}

// ccOne is a Claude-format main file and its parse, as ccFamily judges it.
type ccOne struct {
	f  *file // in the files ccFamily was given
	st *state
}

// headFirst reports whether a, not b, is the conversation two files that
// went on apart are of (ccFamily).
func headFirst(a, b ccOne, holds func(ccOne, string) bool) bool {
	if ab, ba := holds(a, b.st.more().Root), holds(b, a.st.more().Root); ab != ba {
		return ab
	}
	ida, idb := strings.TrimPrefix(a.f.key, a.f.agent+":"), strings.TrimPrefix(b.f.key, b.f.agent+":")
	if fa, fb := ccForkedFrom(*a.f), ccForkedFrom(*b.f); (fb == ida) != (fa == idb) {
		return fb == ida
	}
	if !a.st.Start.Equal(b.st.Start) && !a.st.Start.IsZero() && !b.st.Start.IsZero() {
		return a.st.Start.Before(b.st.Start)
	}
	return a.f.key < b.f.key
}

// ccUUIDMemo is each file's message uuids as ccUUIDsOf read them, kept
// while the file is one of a pair that start alike. Under mu.
var ccUUIDMemo = map[string]*ccUUIDs{}

type ccUUIDs struct {
	size, mod, off int64
	set            uuidSet
	// forked is the session its messages say they were copied from
	// (forkedFrom, which /branch writes on each), "" for none
	forked string
}

// ccForkedFrom is the session /branch copied f's conversation from, as
// its messages name it; "" when they name none (--fork-session doesn't).
func ccForkedFrom(f file) string {
	ccUUIDsOf(f)
	if m := ccUUIDMemo[f.path]; m != nil {
		return m.forked
	}
	return ""
}

var ccForked = []byte(`"forkedFrom":{`)

// ccUUIDsOf is the uuids of the messages in f, read on from where the last
// read stopped while the file only grew.
func ccUUIDsOf(f file) uuidSet {
	m := ccUUIDMemo[f.path]
	if m == nil || f.size < m.size || f.size == m.size && f.mod.UnixNano() != m.mod {
		m = &ccUUIDs{set: uuidSet{}}
		ccUUIDMemo[f.path] = m
	}
	if m.size == f.size && m.mod == f.mod.UnixNano() {
		return m.set
	}
	off, err := scanThin(f.path, m.off, nil, thinClaude, func(b []byte, _, _ int64) bool {
		if !bytes.Contains(b, ccUser) && !bytes.Contains(b, ccAssistant) {
			return true
		}
		r := gjson.GetManyBytes(b, "type", "uuid")
		if t := r[0].String(); (t == "user" || t == "assistant") && r[1].Type == gjson.String {
			m.set.add(r[1].Str)
			if m.forked == "" && bytes.Contains(b, ccForked) {
				m.forked = gjson.GetBytes(b, "forkedFrom.sessionId").String()
			}
		}
		return true
	})
	if err == nil {
		m.off = off
	}
	m.size, m.mod = f.size, f.mod.UnixNano()
	return m.set
}

// ccJoin is copies and branches (ccFamily) in one map, each by the key of
// the session whose file holds what it shares: what ccCopyStates reads.
func ccJoin(copies, branches map[string]string) map[string]string {
	if len(branches) == 0 {
		return copies
	}
	out := make(map[string]string, len(copies)+len(branches))
	maps.Copy(out, copies)
	maps.Copy(out, branches)
	return out
}

// copiesByKeeper is the ids of the copies kept in each session, by its key.
func copiesByKeeper(copies map[string]string, groups map[string][]file) map[string][]string {
	if len(copies) == 0 {
		return nil
	}
	out := map[string][]string{}
	for k, keeper := range copies {
		if g := groups[k]; len(g) > 0 {
			out[keeper] = append(out[keeper], sessionID(g))
		}
	}
	for _, ids := range out {
		slices.Sort(ids)
	}
	return out
}

// ccRestMemo is, for each copy's own file, what it holds that the session
// it is kept in doesn't (ccRest), while neither file has changed. Under mu.
var ccRestMemo = map[string]*ccRestOf{}

type ccRestOf struct {
	size, mod   int64
	keeper      string
	ksize, kmod int64
	st          *state
}

// ccRest is the copy's file read for its messages the keeper's file
// doesn't hold. A copy holds the conversation along its last message
// only: what came before a compaction it started after, what /rewind
// left on another branch and a subagent's messages written in the file
// are in no other file, and were spent all the same. Copies that are
// copies through and through come out empty.
func ccRest(copyFile, keeper file) *state {
	m := ccRestMemo[copyFile.path]
	if m != nil && m.size == copyFile.size && m.mod == copyFile.mod.UnixNano() &&
		m.keeper == keeper.path && m.ksize == keeper.size && m.kmod == keeper.mod.UnixNano() {
		return m.st
	}
	held := ccUUIDsOf(keeper)
	line := parserFor(copyFile.agent).line
	s := &state{}
	_, err := scanThin(copyFile.path, 0, nil, thinClaude, func(b []byte, _, _ int64) bool {
		if !bytes.Contains(b, ccUser) && !bytes.Contains(b, ccAssistant) {
			return true
		}
		r := gjson.GetManyBytes(b, "type", "uuid")
		if t := r[0].String(); t != "user" && t != "assistant" || r[1].Type != gjson.String || held.has(r[1].Str) {
			return true
		}
		line(s, b, copyFile.main)
		return true
	})
	if err != nil {
		// unread is not empty: the copy is counted whole, as before it was
		// known to be one, until it reads
		return cache[copyFile.path]
	}
	ccRestMemo[copyFile.path] = &ccRestOf{copyFile.size, copyFile.mod.UnixNano(), keeper.path, keeper.size, keeper.mod.UnixNano(), s}
	ccRestKept.dirty = true
	return s
}

// ccCopyStates is what each file of a copy adds to the session it is kept
// in: a subagent's file whole, as Claude Code leaves it in the folder of
// the session it ran in and copies none of it, and the copy's own file for
// what the keeper doesn't hold (ccRest). By the file's path.
func ccCopyStates(files []file, copies map[string]string) map[string]*state {
	ccRestLoad()
	defer ccRestSave()
	if len(copies) == 0 {
		if len(ccRestMemo) > 0 {
			clear(ccRestMemo)
			ccRestKept.dirty = true
		}
		return nil
	}
	mains := map[string]file{}
	for _, f := range files {
		if f.main {
			mains[f.key] = f
		}
	}
	out := map[string]*state{}
	for _, f := range files {
		keeper := copies[f.key]
		if keeper == "" {
			continue
		}
		if !f.main {
			if st := cache[f.path]; st != nil {
				out[f.path] = st
			}
			continue
		}
		k, ok := mains[keeper]
		if !ok {
			continue
		}
		if st := ccRest(f, k); st != nil {
			out[f.path] = st
		}
	}
	for p := range ccRestMemo {
		if out[p] == nil {
			delete(ccRestMemo, p)
			ccRestKept.dirty = true
		}
	}
	return out
}
