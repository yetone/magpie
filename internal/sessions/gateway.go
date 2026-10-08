package sessions

// Gateway conversations are a separate, opt-in local store, not native agent
// files or the usage ledger. IDs are hashed before they become path components.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/redact"
	"github.com/yetone/magpie/internal/settings"
)

const GatewayRetention = 7 * 24 * time.Hour
const gatewayTurnMax = 1 << 20
const gatewayReadMax = 128
const gatewayStoreMax = 256 << 20

var gatewayMu sync.Mutex

// GatewayTurn is one captured client-facing exchange. Input may include replayed
// history; Output is only this request's reply. It is not a native session.
type GatewayTurn struct {
	Agent   string    `json:"agent"`
	Session string    `json:"session"`
	Time    time.Time `json:"time"`
	Model   string    `json:"model"`
	Status  int       `json:"status"`
	Input   []Part    `json:"input"`
	Output  []Part    `json:"output"`
	Cut     bool      `json:"cut,omitempty"`
}

func gatewayDir() string { return filepath.Join(settings.Dir(), "gateway-conversations") }

func gatewayIdentity(agent, id string) string {
	b, _ := json.Marshal([]string{agent, id})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// SaveGatewayTurn keeps a bounded, scrubbed exchange. Recording is checked again
// under the same lock as disable-and-clear, so in-flight requests cannot refill it.
func SaveGatewayTurn(turn GatewayTurn) error {
	gatewayMu.Lock()
	defer gatewayMu.Unlock()
	if !settings.Load().GatewayConversations || turn.Agent == "" || turn.Session == "" {
		return nil
	}
	if turn.Time.IsZero() {
		turn.Time = time.Now()
	}
	if turn.Time.Before(time.Now().Add(-GatewayRetention)) {
		return nil
	}
	bound := func(parts []Part) []Part {
		left := 64 << 10 // Each direction gets its own budget; history cannot crowd out the reply.
		out := []Part{}
		for _, p := range parts {
			if left <= 0 {
				turn.Cut = true
				break
			}
			p.Text = string(redact.ScrubJSON([]byte(p.Text)))
			p.Name = redact.Scrub(p.Name)
			if len(p.Text) > min(left, 64<<10) {
				n := min(left, 64<<10)
				for n > 0 && !utf8.ValidString(p.Text[:n]) {
					n--
				}
				p.Cut += utf8.RuneCountInString(p.Text[n:])
				p.Text = p.Text[:n]
				turn.Cut = true
			}
			left -= len(p.Text) + len(p.Name) + 128
			out = append(out, p)
		}
		return out
	}
	turn.Input = bound(turn.Input)
	turn.Output = bound(turn.Output)
	b, err := json.Marshal(turn)
	if err != nil {
		return err
	}
	if len(b) > gatewayTurnMax {
		return errors.New("gateway conversation exceeds storage limit")
	}
	if err := trimGatewayStore(time.Now(), int64(len(b))); err != nil {
		return err
	}
	dir := filepath.Join(gatewayDir(), turn.Time.UTC().Format(time.DateOnly), gatewayIdentity(turn.Agent, turn.Session))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".pending-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	_, err = f.Write(b)
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dir, fmt.Sprintf("%020d-%s.json", turn.Time.UnixNano(), filepath.Base(name)[9:])))
}

// SetGatewayRecording changes only this machine's consent. Clear disables first.
func SetGatewayRecording(on, clear bool) error {
	gatewayMu.Lock()
	defer gatewayMu.Unlock()
	if err := settings.SetGatewayConversations(on && !clear); err != nil {
		return err
	}
	if clear {
		return os.RemoveAll(gatewayDir())
	}
	return nil
}

// PruneGatewayConversations removes expired date buckets even while recording is
// off. Reads enforce the exact cutoff too, between periodic cleanup passes.
func PruneGatewayConversations(now time.Time) error {
	gatewayMu.Lock()
	defer gatewayMu.Unlock()
	return trimGatewayStore(now, 0)
}

// Called under gatewayMu. Bound file count as well as bytes, so many tiny
// requests cannot grow the index or a directory scan without limit.
func trimGatewayStore(now time.Time, incoming int64) error {
	dirs, err := os.ReadDir(gatewayDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, d := range dirs {
		date, err := time.Parse(time.DateOnly, d.Name())
		if err == nil && d.IsDir() && !date.Add(24*time.Hour).After(now.Add(-GatewayRetention)) {
			if err := os.RemoveAll(filepath.Join(gatewayDir(), d.Name())); err != nil {
				return err
			}
		}
	}
	type saved struct {
		path string
		size int64
		at   time.Time
	}
	var files []saved
	var total int64
	err = filepath.WalkDir(gatewayDir(), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() && strings.HasSuffix(d.Name(), ".json") {
			info, err := d.Info()
			if err != nil {
				return err
			}
			files = append(files, saved{path, info.Size(), info.ModTime()})
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].at.Before(files[j].at) })
	for i, f := range files {
		if total+incoming <= gatewayStoreMax && len(files)-i < 4096 {
			break
		}
		if err := os.Remove(f.path); err != nil {
			return err
		}
		_ = os.Remove(filepath.Dir(f.path)) // empty identity buckets only
		total -= f.size
	}
	return nil
}

// GatewayTranscript reads recent captured exchanges. Only a full matching
// previous history prefix is removed, never equal text found elsewhere in a chat.
func GatewayTranscript(agent, id string) (Transcript, error) {
	out := Transcript{Parts: []Part{}, Source: "gateway"}
	if agent == "" || id == "" {
		return out, errors.New("no such session")
	}
	gatewayMu.Lock()
	defer gatewayMu.Unlock()
	now := time.Now()
	var paths []string
	for d := 0; d <= 7; d++ {
		dir := filepath.Join(gatewayDir(), now.UTC().AddDate(0, 0, -d).Format(time.DateOnly), gatewayIdentity(agent, id))
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return out, err
		}
		for _, e := range entries {
			if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".json") {
				paths = append(paths, filepath.Join(dir, e.Name()))
			}
		}
	}
	sort.Strings(paths)
	if len(paths) > gatewayReadMax {
		paths = paths[len(paths)-gatewayReadMax:]
		out.Cut = true
	}
	var previous []Part
	var system []Part
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			return out, err
		}
		b, err := io.ReadAll(io.LimitReader(f, gatewayTurnMax+1))
		f.Close()
		if err != nil {
			return out, err
		}
		var turn GatewayTurn
		if len(b) > gatewayTurnMax || json.Unmarshal(b, &turn) != nil {
			return out, errors.New("unreadable gateway conversation")
		}
		if turn.Agent != agent || turn.Session != id || turn.Time.Before(now.Add(-GatewayRetention)) {
			continue
		}
		out.Captured++
		out.Cut = out.Cut || turn.Cut
		// Context is kept independently because it is repeated before every
		// request's conversation, rather than being a new conversational turn.
		var context, input []Part
		for _, p := range turn.Input {
			if p.Role == "system" {
				context = append(context, p)
			} else {
				input = append(input, p)
			}
		}
		if !slices.Equal(context, system) {
			for _, p := range context {
				if !out.add(true, p) {
					return out, nil
				}
			}
			system = context
		}
		fresh := input
		if len(previous) > 0 && len(input) >= len(previous) && slices.Equal(input[:len(previous)], previous) {
			fresh = input[len(previous):]
		}
		for _, p := range fresh {
			if !out.add(true, p) {
				return out, nil
			}
		}
		for _, p := range turn.Output {
			if !out.add(false, p) {
				return out, nil
			}
		}
		previous = append(slices.Clone(input), turn.Output...)
	}
	return out, nil
}
