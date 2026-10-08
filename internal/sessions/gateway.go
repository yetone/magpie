package sessions

// Gateway conversations are a separate, opt-in local store, not native agent
// files or the usage ledger. IDs are hashed before they become path components.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
const gatewaySessionMax = gatewayStoreMax

var gatewayMu sync.Mutex
var gatewayGeneration uint64 = 1

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
	return saveGatewayTurn(turn, GatewayRecordingGeneration())
}

// GatewayRecordingGeneration returns the current consent generation. A
// request captures it when recording starts and must present the same value
// when saving, so clearing and re-enabling recording cannot resurrect old data.
func GatewayRecordingGeneration() uint64 {
	gatewayMu.Lock()
	defer gatewayMu.Unlock()
	return gatewayGeneration
}

func SaveGatewayTurnGeneration(turn GatewayTurn, generation uint64) error {
	return saveGatewayTurn(turn, generation)
}

func saveGatewayTurn(turn GatewayTurn, generation uint64) error {
	gatewayMu.Lock()
	allowed := settings.Load().GatewayConversations && generation == gatewayGeneration
	gatewayMu.Unlock()
	if !allowed || turn.Agent == "" || turn.Session == "" {
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
	gatewayMu.Lock()
	defer gatewayMu.Unlock()
	if generation != gatewayGeneration || !settings.Load().GatewayConversations {
		return nil
	}
	dir := filepath.Join(gatewayDir(), turn.Time.UTC().Format(time.DateOnly), gatewayIdentity(turn.Agent, turn.Session))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Append one bounded line; the cost is independent of other sessions and
	// of this session's earlier turns. Cleanup runs outside the request path.
	path := filepath.Join(dir, "turns.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	end, err := gatewayCompleteSize(f, info.Size())
	if err != nil {
		return err
	}
	if end+int64(len(b))+1 > gatewaySessionMax {
		return errors.New("gateway session exceeds storage limit")
	}
	// A process interrupted mid-append must not poison all later turns.
	if end != info.Size() {
		if err := f.Truncate(end); err != nil {
			return err
		}
	}
	_, err = f.Write(append(b, '\n'))
	return errors.Join(err, f.Close())
}

// Ignore at most one unfinished turn; earlier complete lines remain intact.
func gatewayCompleteSize(f *os.File, size int64) (int64, error) {
	if size == 0 {
		return 0, nil
	}
	var last [1]byte
	if _, err := f.ReadAt(last[:], size-1); err != nil {
		return 0, err
	}
	if last[0] == '\n' {
		return size, nil
	}
	n := min(size, int64(gatewayTurnMax+1))
	tail := make([]byte, int(n))
	if _, err := f.ReadAt(tail, size-n); err != nil {
		return 0, err
	}
	if i := bytes.LastIndexByte(tail, '\n'); i >= 0 {
		return size - n + int64(i) + 1, nil
	}
	if n == size {
		return 0, nil
	}
	return 0, errors.New("unreadable gateway conversation")
}

// SetGatewayRecording changes only this machine's consent. Clear disables first.
func SetGatewayRecording(on, clear bool) error {
	gatewayMu.Lock()
	defer gatewayMu.Unlock()
	if clear || !on {
		gatewayGeneration++
	}
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
	root, generation := gatewayDir(), gatewayGeneration
	gatewayMu.Unlock()
	type saved struct {
		path    string
		info    os.FileInfo
		expired bool
	}
	var files []saved
	var total int64
	// Walking and sorting never hold the lock needed by a response's save.
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		date, err := time.Parse(time.DateOnly, strings.Split(rel, string(filepath.Separator))[0])
		expired := err == nil && !date.Add(24*time.Hour).After(now.Add(-GatewayRetention))
		files = append(files, saved{path, info, expired})
		total += info.Size()
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].expired != files[j].expired {
			return files[i].expired
		}
		return files[i].info.ModTime().Before(files[j].info.ModTime())
	})
	for _, f := range files {
		if !f.expired && total <= gatewayStoreMax {
			continue
		}
		gatewayMu.Lock()
		if generation != gatewayGeneration || root != gatewayDir() {
			gatewayMu.Unlock()
			return nil
		}
		current, err := os.Stat(f.path)
		if err == nil && os.SameFile(current, f.info) && current.Size() == f.info.Size() && current.ModTime().Equal(f.info.ModTime()) {
			err = os.Remove(f.path)
			if err == nil {
				total -= f.info.Size()
				_ = os.Remove(filepath.Dir(f.path))
				_ = os.Remove(filepath.Dir(filepath.Dir(f.path)))
			}
		}
		gatewayMu.Unlock()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
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
			if e.Type().IsRegular() && (strings.HasSuffix(e.Name(), ".json") || e.Name() == "turns.jsonl") {
				paths = append(paths, filepath.Join(dir, e.Name()))
			}
		}
	}
	var turns []GatewayTurn
	keep := func(turn GatewayTurn) {
		if turn.Agent != agent || turn.Session != id || turn.Time.Before(now.Add(-GatewayRetention)) {
			return
		}
		turns = append(turns, turn)
		if len(turns) > gatewayReadMax*2 {
			sort.SliceStable(turns, func(i, j int) bool { return turns[i].Time.Before(turns[j].Time) })
			turns = slices.Clone(turns[len(turns)-gatewayReadMax:])
			out.Cut = true
		}
	}
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			return out, err
		}
		if strings.HasSuffix(path, ".jsonl") {
			var info os.FileInfo
			info, err = f.Stat()
			if err != nil {
				f.Close()
				return out, err
			}
			if info.Size() > gatewaySessionMax {
				f.Close()
				return out, errors.New("gateway session exceeds storage limit")
			}
			var end int64
			end, err = gatewayCompleteSize(f, info.Size())
			if err != nil {
				f.Close()
				return out, err
			}
			out.Cut = out.Cut || end != info.Size()
			scan := bufio.NewScanner(io.LimitReader(f, end))
			scan.Buffer(make([]byte, 64<<10), gatewayTurnMax+1)
			for scan.Scan() {
				var turn GatewayTurn
				if json.Unmarshal(scan.Bytes(), &turn) != nil {
					f.Close()
					return out, errors.New("unreadable gateway conversation")
				}
				keep(turn)
			}
			err = scan.Err()
		} else {
			// Read the original per-turn files until their retention expires.
			var data []byte
			data, err = io.ReadAll(io.LimitReader(f, gatewayTurnMax+1))
			var turn GatewayTurn
			if err == nil && (len(data) > gatewayTurnMax || json.Unmarshal(data, &turn) != nil) {
				err = errors.New("unreadable gateway conversation")
			}
			if err == nil {
				keep(turn)
			}
		}
		f.Close()
		if err != nil {
			return out, err
		}
	}
	sort.SliceStable(turns, func(i, j int) bool { return turns[i].Time.Before(turns[j].Time) })
	if len(turns) > gatewayReadMax {
		turns = turns[len(turns)-gatewayReadMax:]
		out.Cut = true
	}
	var previous, system []Part
	for _, turn := range turns {
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
