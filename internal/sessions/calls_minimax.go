package sessions

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
)

// MiniMax Code's calls (#1605, LukeZ1988: MiniMax Code was missing from
// Usage's split by agent). Read from @minimax-ai/code 0.6.8's own files, as
// it wrote them in a sandbox: each session is a folder
// v2/sessions/<yyyy>/<mm>/<dd>/<hh-mm-ss-ms>-session_<base64 of its id>/,
// its messages in messages.jsonl, one per line. A reply is an assistant
// message naming the provider and model that answered, its usage (input
// without the cache, output, cacheRead, cacheWrite, as pi's ai package
// counts them) and the time in milliseconds:
//
//	{"message_id":"msg-…","turn_id":"turn_…","message":{"role":"assistant",
//	 "provider":"custom_provider:relay","model":"m2","usage":{"input":123,
//	 "output":7,"cacheRead":50,"cacheWrite":20,…},"timestamp":1791717525043,…}}
//
// A call MiniMax Code made through magpie's entry (custom_provider:magpie,
// which internal/agent's minimax.go writes) is the gateway's to count: it
// logged the call itself, with the provider and account that answered.

// MiniMaxDir is MiniMax Code's data folder: MINIMAX_DATA_DIR, the folder
// its desktop app moved it to, or ~/.minimax (appdir.MiniMaxDir).
func MiniMaxDir() string { return expandHome(appdir.MiniMaxDir()) }

func minimaxSessions() string { return filepath.Join(MiniMaxDir(), "v2", "sessions") }

// mcodeGateway is the provider MiniMax Code names a call to magpie's entry by.
const mcodeGateway = "custom_provider:magpie"

// minimaxCallFiles are each session's messages files: messages.jsonl, and
// any other generation of it the session's history-catalog.json keeps
// beside it (messages*.jsonl). A reply copied into another generation keeps
// its message_id, and Calls counts an id once.
func minimaxCallFiles() []file {
	if !Reads("minimax-code") {
		return nil
	}
	var out []file
	walkLinked(minimaxSessions(), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasPrefix(name, "messages") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		f := file{agent: "minimax-code", key: "minimax-code:" + minimaxSessionID(filepath.Base(filepath.Dir(p))), path: p, main: true}
		if stat(&f) {
			out = append(out, f)
		}
		return nil
	})
	return out
}

// minimaxSessionID is a session folder's session id: the base64 after
// "session_" (mvs_…), or the folder's name when it isn't that.
func minimaxSessionID(dir string) string {
	_, enc, ok := strings.Cut(dir, "-session_")
	if !ok {
		return dir
	}
	for _, e := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if b, err := e.DecodeString(enc); err == nil && len(b) > 0 {
			return string(b)
		}
	}
	return enc
}

type mcodeLine struct {
	MessageID string `json:"message_id"`
	Message   *struct {
		Role      string   `json:"role"`
		Provider  string   `json:"provider"`
		Model     string   `json:"model"`
		Usage     *piUsage `json:"usage"`
		Timestamp int64    `json:"timestamp"`
	} `json:"message"`
}

var mcodeAsst = []byte(`"role":"assistant"`)

// readMiniMaxCalls reads a session's messages whole: a file another
// generation of the history replaced is read again from its start.
func readMiniMaxCalls(f file) (*callFile, bool) {
	r, err := os.Open(f.path)
	if err != nil {
		return &callFile{Path: f.path, Agent: "minimax-code"}, false
	}
	defer r.Close()
	st := &callFile{Agent: "minimax-code", Path: f.path, Session: strings.TrimPrefix(f.key, "minimax-code:")}
	br := bufio.NewReaderSize(r, scanBuffer(r, 0))
	for {
		b, err := br.ReadBytes('\n')
		if err != nil && (!errors.Is(err, io.EOF) || len(bytes.TrimSpace(b)) == 0) {
			break
		}
		b = bytes.TrimSpace(b)
		if len(b) > 0 && len(b) <= maxLine && bytes.Contains(b, mcodeAsst) && bytes.Contains(b, piUsed) {
			var l mcodeLine
			if json.Unmarshal(b, &l) == nil {
				if c, ok := minimaxCall(st, l); ok {
					st.Calls = append(st.Calls, c)
				}
			}
		}
		if err != nil {
			break
		}
	}
	return st, true
}

// minimaxCall is an assistant message as a call.
func minimaxCall(st *callFile, l mcodeLine) (Call, bool) {
	m := l.Message
	if m == nil || m.Role != "assistant" || m.Usage == nil || m.Provider == mcodeGateway {
		return Call{}, false
	}
	at := ms(m.Timestamp)
	if at.IsZero() {
		return Call{}, false
	}
	c := Call{
		Agent:    "minimax-code",
		Time:     at,
		Session:  st.Session,
		Model:    st.str(m.Model),
		Upstream: st.str(m.Provider),
		Tokens:   m.Usage.tokens(),
	}
	if l.MessageID != "" {
		c.Msg = st.str("minimax-code:" + l.MessageID)
	}
	if c.Tokens.zero() {
		return Call{}, false
	}
	return c, true
}
