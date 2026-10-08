package sessions

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/provider"
)

// WorkBuddy's calls, read from the same lines its session summary is
// (workbuddy.go): a reply's usage is in providerData.usage of its assistant
// message (or its last function call), named by that reply's
// providerData.messageId. One line per reply carries it.
//
// As with dsh and ZCode, the Sessions page read these all along and the
// usage ledger did not, so WorkBuddy's tokens never reached the Usage page.
//
// A call WorkBuddy made to magpie's own gateway is not read here: the
// gateway logs it itself, with the provider and account that really
// answered, and this file has neither to pair it with (workbuddyGateway).

// workbuddyCallFiles are WorkBuddy's sessions as call sources. The subagent
// files under a session's subagents/ are its own calls to keep, as dsh's are
// (dshCallFiles): a call carries its id whatever file it was written in.
// Nothing in this path reads a file's main — that is the Sessions page's own
// mark for a session's file rather than a subagent's — so it is left as
// workbuddyFiles set it.
func workbuddyCallFiles() []file {
	return workbuddyFiles()
}

// wbGateway are the ids WorkBuddy has of magpie's gateway, kept while
// models.json is as it was, as zcodeCallFiles keeps its listing.
var wbGateway struct {
	sync.Mutex
	stamp string
	ids   map[string]bool
}

// workbuddyGateway is whether a model id is one WorkBuddy asks magpie's
// gateway for. A routing group's id says so on its own: "group/<id>" is a
// name only a magpie answers to (provider.GroupPrefix), so a call asked for
// under it went to a gateway whichever machine that was, and this file has
// nothing to pair it with. The rest are read from models.json, whose entries
// for magpie (internal/agent's workbuddy.go writes them: vendor "magpie",
// url this gateway's) are the ones WorkBuddy shows in its picker as
// "custom-local:<id>". An entry the user put there for another vendor keeps
// its own usage, so the vendor is read too and not only the prefix.
//
// models.json is the user's own configuration, not a record of the call: take
// magpie's models out of it and the calls made while they were in stop being
// read as the gateway's here, and are counted as the agent's own beside the
// gateway's rows for the same calls. It is the one shape this cannot tell
// from the file alone — WorkBuddy names no provider of its own, as dsh, ZCode
// and OpenCode do — so it is said here and pinned by a test.
func workbuddyGateway(id string) bool {
	const local = "custom-local:"
	if !strings.HasPrefix(id, local) {
		return false
	}
	id = id[len(local):]
	return strings.HasPrefix(id, provider.GroupPrefix) || workbuddyGatewayIDs()[id]
}

// wbEntry is what is read of a models.json entry: its id and whose it is.
type wbEntry struct {
	ID     string `json:"id"`
	Vendor string `json:"vendor"`
}

// workbuddyGatewayIDs are the ids of the models WorkBuddy's models.json names
// as magpie's, an empty set when it names none (or cannot be read).
func workbuddyGatewayIDs() map[string]bool {
	path := filepath.Join(WorkBuddyDir(), "models.json")
	stamp := ocStamp(path)
	wbGateway.Lock()
	defer wbGateway.Unlock()
	if stamp == wbGateway.stamp {
		return wbGateway.ids
	}
	ids := map[string]bool{}
	// a bare list of entries, or an object with a "models" list, as
	// workbuddyRead in internal/agent reads both; that package cannot be
	// imported from here (it imports this one)
	b, err := os.ReadFile(path)
	if err == nil {
		var list []wbEntry
		var doc struct{ Models []wbEntry }
		if json.Unmarshal(b, &list) == nil {
			for _, e := range list {
				if e.Vendor == "magpie" && e.ID != "" {
					ids[e.ID] = true
				}
			}
		} else if json.Unmarshal(b, &doc) == nil {
			for _, e := range doc.Models {
				if e.Vendor == "magpie" && e.ID != "" {
					ids[e.ID] = true
				}
			}
		}
	}
	wbGateway.stamp, wbGateway.ids = stamp, ids
	return ids
}

// readWorkBuddyCalls reads a session's calls whole: its usage lines repeat
// the same messageId as a reply goes on, so the last line for a reply is the
// one that counts. A reply WorkBuddy brought over from its older history
// names no messageId at all, and each of those is a call of its own, as the
// summary reader counts them (workbuddyLine replaces the call before it only
// when the id is the same non-empty one).
func readWorkBuddyCalls(f file) (*callFile, bool) {
	r, err := openLines(f.path)
	if err != nil {
		return &callFile{Path: f.path, Agent: "workbuddy"}, false
	}
	defer r.Close()
	st := &callFile{Agent: "workbuddy", Path: f.path, Session: strings.TrimPrefix(f.key, "workbuddy:")}
	br := bufio.NewReaderSize(r, 1<<20)
	last := "" // the messageId the call before this line carried
	for {
		b, err := br.ReadBytes('\n')
		if err != nil {
			if !errors.Is(err, io.EOF) || len(bytes.TrimSpace(b)) == 0 {
				break
			}
		}
		b = bytes.TrimSpace(b)
		if len(b) > 0 && len(b) <= maxLine && bytes.Contains(b, wbUsage) {
			var l wbLine
			if json.Unmarshal(b, &l) == nil && l.ProviderData.Usage != nil {
				// the id this line carried, as the summary reader keeps it:
				// a zero-token line is a call the same, and the reply after
				// it is a new one, so both readers end on the same call
				id := st.str(l.ProviderData.MessageID)
				if c, ok := workbuddyCall(st, l, b); ok {
					if c.Msg != "" && c.Msg == last {
						st.Calls[len(st.Calls)-1] = c // another line of the reply before it
					} else {
						st.Calls = append(st.Calls, c)
					}
				}
				last = id
			}
		}
		if err != nil {
			break
		}
	}
	return st, true
}

// workbuddyCall is a line's providerData.usage as a call: the reply's model,
// its tokens (input with the cache read and written taken out of it, as its
// summary counts them) and the time it was written.
func workbuddyCall(st *callFile, l wbLine, b []byte) (Call, bool) {
	// a call made to magpie's gateway is the gateway's record to keep, not
	// this one's: see workbuddyGateway
	if workbuddyGateway(l.ProviderData.RequestModel) {
		return Call{}, false
	}
	u := l.ProviderData.Usage
	if u == nil {
		return Call{}, false
	}
	t := numTS(b)
	if t.IsZero() {
		return Call{}, false
	}
	in, out := cmp.Or(u.Input, u.OldInput), cmp.Or(u.Output, u.OldOutput)
	read := u.InputDetails.sum("cached_tokens") + u.OldDetails.sum("cached_tokens")
	write := cmp.Or(u.CacheWrite, u.OldCacheWrite)
	c := Call{
		Agent:   "workbuddy",
		Time:    t,
		Session: st.Session,
		Model:   st.str(cmp.Or(l.ProviderData.Model, l.ProviderData.RequestModel)),
		Msg:     st.str(l.ProviderData.MessageID),
		Tokens:  Tokens{Input: max(0, in-read-write), Output: out, CacheRead: read, CacheWrite: write},
	}
	if c.Session == "" {
		c.Session = st.str(l.SessionID)
	}
	if c.Tokens.zero() {
		return Call{}, false
	}
	return c, true
}
