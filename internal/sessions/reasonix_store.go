package sessions

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

var reasonixStoreFailures = struct {
	sync.Mutex
	dirs map[string]bool
}{dirs: map[string]bool{}}

const reasonixStoreRevision = "reasonix-store-v3:"
const reasonixFrameLimit = 8 << 20

type reasonixStoreManifest struct {
	Schema      int       `json:"schemaVersion"`
	Codec       string    `json:"codec"`
	ID          string    `json:"sessionId"`
	Created     time.Time `json:"createdAt"`
	ContentRoot string    `json:"contentRoot"`
	Source      *struct {
		Path     string `json:"path"`
		LegacyID string `json:"legacyHeadId"`
	} `json:"source"`
}
type reasonixStoreRef struct {
	Digest string `json:"digest"`
	Bytes  int64  `json:"bytes"`
}
type reasonixStoreEvent struct {
	ID       string            `json:"id"`
	Seq      uint64            `json:"seq"`
	Kind     string            `json:"kind"`
	Optional bool              `json:"optional"`
	Required bool              `json:"required"`
	Payload  json.RawMessage   `json:"payload"`
	Ref      *reasonixStoreRef `json:"payloadRef"`
}
type reasonixStoreCommit struct {
	Schema        int                  `json:"schemaVersion"`
	Codec         string               `json:"codec"`
	Record        string               `json:"recordType"`
	ID            string               `json:"commitId"`
	Operation     string               `json:"operationId"`
	OperationHash string               `json:"operationHash"`
	First         uint64               `json:"firstSeq"`
	Count         int                  `json:"eventCount"`
	Generation    uint64               `json:"writerGeneration"`
	At            time.Time            `json:"createdAt"`
	Events        []reasonixStoreEvent `json:"events"`
	Event         *struct {
		ID       string            `json:"id"`
		Seq      uint64            `json:"seq"`
		Kind     string            `json:"kind"`
		Optional bool              `json:"optional"`
		Required bool              `json:"required"`
		Payload  []byte            `json:"payload"`
		Ref      *reasonixStoreRef `json:"payloadRef"`
	} `json:"event"`
	Digest string `json:"sha256"`
}

func reasonixStoreDirs() []string {
	root := ReasonixDir()
	var dirs []string
	for _, pattern := range []string{filepath.Join(root, "sessions-v*", "*"), filepath.Join(root, "projects", "*", "sessions-v*", "*"), filepath.Join(root, "desktop-sessions-v*", "by-id", "*")} {
		found, _ := SessionGlob(pattern)
		dirs = append(dirs, found...)
	}
	return dirs
}
func reasonixStoreManifestAt(dir string) (reasonixStoreManifest, error) {
	var m reasonixStoreManifest
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return m, err
	}
	if err = json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	if m.ID == "" {
		return m, errors.New("Reasonix store has no session identity")
	}
	switch m.Codec {
	case "reasonix.session.linear/v4":
		if m.Schema != 4 {
			return m, errors.New("Reasonix framed schema mismatch")
		}
	case "reasonix.session.linear/v3.1", "reasonix.session.linear/v3", "reasonix.session.events/v3":
		if m.Schema != 3 {
			return m, errors.New("Reasonix commit schema mismatch")
		}
	default:
		return m, fmt.Errorf("unknown Reasonix codec %q", m.Codec)
	}
	return m, nil
}
func reasonixStoreLog(dir string, m reasonixStoreManifest) string {
	if m.Codec == "reasonix.session.linear/v4" {
		return filepath.Join(dir, "events.frames")
	}
	return filepath.Join(dir, "events.jsonl")
}
func reasonixNativeFiles(chosen map[string][]file) {
	for _, dir := range reasonixStoreDirs() {
		m, err := reasonixStoreManifestAt(dir)
		if err != nil {
			continue
		}
		p := reasonixStoreLog(dir, m)
		b, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
		header, _ := os.ReadFile(filepath.Join(dir, "header.json"))
		digest := sha256.Sum256(append(b, header...))
		f := file{agent: "reasonix", key: "reasonix:" + m.ID, sid: m.ID, path: p, main: true, manifest: filepath.Join(dir, "manifest.json"), rev: reasonixStoreRevision + m.Codec + ":" + hex.EncodeToString(digest[:])}
		if filepath.Base(filepath.Dir(dir)) != "sessions-v4" {
			f.readOnly = true
		}
		if !stat(&f) {
			continue
		}
		old := chosen[m.ID]
		if len(old) > 0 && strings.HasPrefix(old[0].rev, reasonixStoreRevision) && (old[0].mod.After(f.mod) || old[0].mod.Equal(f.mod) && old[0].path < f.path) {
			continue
		}
		pair := []file{f}
		if m.Source != nil {
			for id, source := range chosen {
				if len(source) > 0 && filepath.Clean(source[0].path) == filepath.Clean(m.Source.Path) {
					if len(source) > 1 {
						for _, ledger := range source[1:] {
							ledger.key = f.key
							pair = append(pair, ledger)
						}
					}
					if id != m.ID {
						delete(chosen, id)
					}
				}
			}
		}
		chosen[m.ID] = pair
	}
}

var reasonixStoreKinds = map[string]bool{}

func init() {
	for _, k := range strings.Fields("message/complete message/upsert message/retract assistant/attempt tool/call tool/start tool/result turn/start turn/end step/start step/end todo/write interaction/created interaction/resolved plan/state goal/state session/title session/config model/context-replace history/replace context/replace compaction runtime/recovery legacy/import diagnostic submission/accepted") {
		reasonixStoreKinds[k] = true
	}
}

// Complete transactions alone are visible. Reading never takes a writer lease,
// repairs a torn tail, rewrites an index, or migrates the source.
func reasonixScanStore(path string, visit func(reasonixStoreEvent, time.Time) error) error {
	dir := filepath.Dir(path)
	m, err := reasonixStoreManifestAt(dir)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	reader := io.LimitReader(f, info.Size())
	next := uint64(1)
	operations := map[string]string{}
	accept := func(c reasonixStoreCommit) error {
		if c.ID == "" || c.Operation == "" || c.OperationHash == "" || c.Generation == 0 || c.First != next || c.Count <= 0 || len(c.Events) != c.Count {
			return errors.New("invalid Reasonix commit boundary")
		}
		if prior, ok := operations[c.Operation]; ok && prior != c.OperationHash {
			return errors.New("conflicting Reasonix operation")
		}
		for i, e := range c.Events {
			if e.ID == "" || e.Kind == "" || e.Seq != next+uint64(i) || e.Ref != nil && len(e.Payload) > 0 {
				return errors.New("invalid Reasonix event boundary")
			}
			if !e.Optional && !reasonixStoreKinds[e.Kind] {
				return fmt.Errorf("unknown required Reasonix event %q", e.Kind)
			}
		}
		// Validate every boundary before publishing any event from this commit.
		for _, e := range c.Events {
			if err := visit(e, c.At); err != nil {
				return err
			}
		}
		operations[c.Operation] = c.OperationHash
		next += uint64(c.Count)
		return nil
	}
	if m.Codec != "reasonix.session.linear/v4" {
		r := bufio.NewReader(reader)
		for {
			line, err := r.ReadBytes('\n')
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			var c reasonixStoreCommit
			if err = json.Unmarshal(line, &c); err != nil {
				return err
			}
			if c.Schema != 3 || c.Codec != m.Codec || c.Record != "commit" {
				return errors.New("invalid Reasonix JSONL commit codec")
			}
			if err = accept(c); err != nil {
				return err
			}
		}
	}
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(reasonixFrameLimit))
	if err != nil {
		return err
	}
	defer decoder.Close()
	var pending *reasonixStoreCommit
	var digest hash.Hash
	for {
		raw, err := reasonixReadFrame(reader, decoder)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil
		}
		if err != nil {
			return err
		}
		var r reasonixStoreCommit
		if err = json.Unmarshal(raw, &r); err != nil {
			return err
		}
		if r.Schema != 4 || r.Codec != m.Codec {
			return errors.New("invalid Reasonix frame codec")
		}
		switch r.Record {
		case "batch/begin":
			if pending != nil || r.First != next || r.Count <= 0 || r.ID == "" || r.Operation == "" || r.OperationHash == "" || r.Generation == 0 {
				return errors.New("invalid Reasonix batch start")
			}
			pending = &r
			digest = sha256.New()
			digest.Write(raw)
			digest.Write([]byte{0})
		case "batch/event":
			if pending == nil || r.Event == nil || len(pending.Events) >= pending.Count {
				return errors.New("Reasonix event outside batch")
			}
			e := r.Event
			pending.Events = append(pending.Events, reasonixStoreEvent{ID: e.ID, Seq: e.Seq, Kind: e.Kind, Optional: e.Optional, Required: e.Required, Payload: e.Payload, Ref: e.Ref})
			digest.Write(raw)
			digest.Write([]byte{0})
		case "batch/end":
			if pending == nil || r.ID != pending.ID || r.First != pending.First || r.Count != pending.Count || r.Digest != hex.EncodeToString(digest.Sum(nil)) {
				return errors.New("invalid Reasonix batch checksum or end")
			}
			if err = accept(*pending); err != nil {
				return err
			}
			pending = nil
			digest = nil
		default:
			return fmt.Errorf("unknown Reasonix frame record %q", r.Record)
		}
	}
}
func reasonixReadFrame(r io.Reader, d *zstd.Decoder) ([]byte, error) {
	var h [12]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, err
	}
	if string(h[:4]) != "RX4F" {
		return nil, errors.New("invalid Reasonix frame magic")
	}
	compressed, raw := int(binary.BigEndian.Uint32(h[4:8])), int(binary.BigEndian.Uint32(h[8:12]))
	if compressed <= 0 || compressed > reasonixFrameLimit || raw <= 0 || raw > reasonixFrameLimit {
		return nil, errors.New("invalid Reasonix frame size")
	}
	b := make([]byte, compressed)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	out, err := d.DecodeAll(b, nil)
	if err != nil {
		return nil, err
	}
	if len(out) != raw {
		return nil, errors.New("Reasonix frame length mismatch")
	}
	return out, nil
}

func reasonixStorePayload(dir string, e reasonixStoreEvent) ([]byte, error) {
	if e.Ref == nil {
		return e.Payload, nil
	}
	m, err := reasonixStoreManifestAt(dir)
	if err != nil {
		return nil, err
	}
	root := m.ContentRoot
	if root == "" {
		root = "../.content-v1"
	}
	if root != ".content-v1" && root != "../.content-v1" {
		return nil, errors.New("unsafe Reasonix content root")
	}
	ref := e.Ref
	digest, err := hex.DecodeString(ref.Digest)
	if err != nil || len(digest) != sha256.Size || ref.Bytes < 0 {
		return nil, errors.New("invalid Reasonix content reference")
	}
	p := filepath.Join(dir, root, "objects", ref.Digest[:2], ref.Digest[2:4], ref.Digest)
	contentDir, err := filepath.EvalSymlinks(filepath.Join(dir, root))
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(contentDir, real)
	if err != nil || !filepath.IsLocal(relative) {
		return nil, errors.New("Reasonix content escapes object store")
	}
	f, err := os.Open(real)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() != ref.Bytes {
		return nil, errors.New("Reasonix content length mismatch")
	}
	b, err := io.ReadAll(io.LimitReader(f, ref.Bytes+1))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	if len(b) != int(ref.Bytes) || !bytes.Equal(sum[:], digest) {
		return nil, errors.New("Reasonix content checksum mismatch")
	}
	return b, nil
}

type reasonixStoreProjection struct {
	messages     []reasonixMessage
	positions    map[string]int
	title, model string
	cwd          string
	last         time.Time
	context      *reasonixStoreEvent
}

func reasonixProjectStore(path string, summary bool) (*reasonixStoreProjection, error) {
	p := &reasonixStoreProjection{positions: map[string]int{}}
	err := reasonixScanStore(path, func(e reasonixStoreEvent, at time.Time) error {
		if at.After(p.last) {
			p.last = at
		}
		switch e.Kind {
		case "model/context-replace", "compaction":
			copy := e
			p.context = &copy
			return nil
		case "message/complete", "message/upsert", "message/retract", "history/replace", "legacy/import", "session/title", "session/config":
		default:
			return nil
		}
		b, err := reasonixStorePayload(filepath.Dir(path), e)
		if err != nil {
			return err
		}
		var body struct {
			Message  *reasonixMessage  `json:"message"`
			Messages []reasonixMessage `json:"messages"`
			IDs      []string          `json:"messageIds"`
			Title    string            `json:"title"`
			Model    string            `json:"modelRef"`
		}
		if err = json.Unmarshal(b, &body); err != nil {
			return err
		}
		prepare := func(m reasonixMessage) reasonixMessage {
			// Older producers did not attach provenance to host snapshots.
			// Recognize the complete envelope before bounding summary text;
			// explicit user provenance remains authoritative.
			if m.Role == "user" && m.Origin == "" && strings.HasPrefix(strings.TrimSpace(m.Content), `<session-context version="1">`) && strings.HasSuffix(strings.TrimSpace(m.Content), "</session-context>") {
				m.Origin = "host"
			}
			if cwd := reasonixNativeWorkspace(m); cwd != "" {
				p.cwd = cwd
			}
			if m.At <= 0 {
				m.At = at.UnixMilli()
			}
			if summary {
				m.NativeSeq = e.Seq
				reasonixMessageParts(m, func(_ bool, part Part) bool {
					if part, ok := keep(part); ok {
						m.DisplaySize += len(part.Text)
					}
					return true
				})
				if m.Role == "user" {
					m.Content = title(reasonixTitleText(m))
					if m.RawContent != nil {
						v := title(*m.RawContent)
						m.RawContent = &v
					}
				} else {
					m.Content = ""
					m.RawContent = nil
				}
				m.Thinking = ""
				for i := range m.Tools {
					m.Tools[i].Arguments = ""
				}
			}
			return m
		}
		switch e.Kind {
		case "session/title":
			p.title = body.Title
		case "session/config":
			p.model = body.Model
		case "message/complete", "message/upsert":
			if body.Message == nil || body.Message.ID == "" {
				return errors.New("Reasonix message has no identity")
			}
			m := prepare(*body.Message)
			i, ok := p.positions[m.ID]
			if ok {
				if e.Kind == "message/upsert" {
					p.messages[i] = m
				}
			} else {
				p.positions[m.ID] = len(p.messages)
				p.messages = append(p.messages, m)
			}
		case "message/retract":
			for _, id := range body.IDs {
				if i, ok := p.positions[id]; ok {
					p.messages[i] = reasonixMessage{}
					delete(p.positions, id)
				}
			}
		case "history/replace", "legacy/import":
			if body.Messages == nil {
				return errors.New("Reasonix replacement has no messages")
			}
			p.messages = nil
			p.positions = map[string]int{}
			for index, m := range body.Messages {
				if m.ID == "" {
					m.ID = reasonixImportedID(e.Seq, index)
				}
				if _, ok := p.positions[m.ID]; m.ID != "" && ok {
					continue
				}
				p.positions[m.ID] = len(p.messages)
				p.messages = append(p.messages, prepare(m))
			}
			if e.Kind == "legacy/import" && body.Model != "" {
				p.model = body.Model
			}
		}
		return nil
	})
	if err == nil && p.context != nil {
		b, e := reasonixStorePayload(filepath.Dir(path), *p.context)
		if e != nil {
			return p, e
		}
		var body struct {
			Messages []reasonixMessage `json:"messages"`
		}
		if e = json.Unmarshal(b, &body); e != nil {
			return p, e
		}
		for _, m := range body.Messages {
			if cwd := reasonixNativeWorkspace(m); cwd != "" {
				p.cwd = cwd
			}
		}
	}
	return p, err
}
func parseReasonixStore(f file) *state {
	s := &state{ID: f.sid, Size: f.size, Mod: f.mod.UnixNano(), DBRevision: f.rev, UsageIncomplete: true}
	m, err := reasonixStoreManifestAt(filepath.Dir(f.path))
	if err != nil {
		return s
	}
	s.Start = m.Created
	var header struct {
		Schema int    `json:"schemaVersion"`
		ID     string `json:"sessionId"`
		Cwd    string `json:"cwd"`
	}
	if b, e := os.ReadFile(filepath.Join(filepath.Dir(f.path), "header.json")); e == nil && json.Unmarshal(b, &header) == nil && header.Schema == 1 && header.ID == m.ID {
		s.Cwd = header.Cwd
	}
	p, err := reasonixProjectStore(f.path, true)
	reasonixStoreFailures.Lock()
	if err != nil {
		reasonixStoreFailures.dirs[filepath.Dir(f.path)] = true
	} else {
		delete(reasonixStoreFailures.dirs, filepath.Dir(f.path))
	}
	reasonixStoreFailures.Unlock()
	if err != nil {
		s.UsageIncomplete = true
	}
	if p != nil {
		s.Named = p.title
		if err != nil && s.Named == "" {
			s.Named = "Reasonix session (read error)"
		}
		for _, msg := range p.messages {
			b, _ := json.Marshal(msg)
			reasonixLine(s, b, true)
		}
		if p.last.After(s.Last) {
			s.Last = p.last
		}
		if p.model != "" {
			if s.Models == nil {
				s.Models = map[string]Tokens{}
			}
			s.Models[strings.TrimPrefix(p.model, "magpie/")] = Tokens{}
		}
	}
	// Project directories carry an exact workspace marker; never reverse a
	// lossy slug. Imported legacy metadata can supply the same information.
	marker := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(f.path))), ".workspace-root")
	if b, e := os.ReadFile(marker); e == nil && s.Cwd == "" {
		s.Cwd = strings.TrimSpace(string(b))
	}
	if s.Cwd == "" {
		s.Cwd = p.cwd
		if s.Cwd == "" {
			s.Cwd = s.ReasonixCwd
		}
	}
	return s
}

// Resolve canonical identity/order first using small metadata, then materialize
// only the transcript window. A late upsert or retract cannot resurrect old text,
// and opening a 300 MB history does not retain that entire history in the GUI.
func reasonixStoreTranscript(path string, add func(bool, Part) bool) error {
	p, err := reasonixProjectStore(path, true)
	if err != nil {
		return err
	}
	wanted := map[uint64]map[string]bool{}
	size := 0
	for _, m := range p.messages {
		if m.DisplaySize == 0 {
			continue
		}
		if size >= transcriptMax {
			break
		}
		if wanted[m.NativeSeq] == nil {
			wanted[m.NativeSeq] = map[string]bool{}
		}
		wanted[m.NativeSeq][m.ID] = true
		size += m.DisplaySize
	}
	parts := map[string][]Part{}
	err = reasonixScanStore(path, func(e reasonixStoreEvent, _ time.Time) error {
		ids := wanted[e.Seq]
		if len(ids) == 0 {
			return nil
		}
		b, err := reasonixStorePayload(filepath.Dir(path), e)
		if err != nil {
			return err
		}
		var body struct {
			Message  *reasonixMessage  `json:"message"`
			Messages []reasonixMessage `json:"messages"`
		}
		if err = json.Unmarshal(b, &body); err != nil {
			return err
		}
		messages := body.Messages
		if body.Message != nil {
			messages = []reasonixMessage{*body.Message}
		}
		for index, m := range messages {
			if m.ID == "" {
				m.ID = reasonixImportedID(e.Seq, index)
			}
			if !ids[m.ID] {
				continue
			}
			if _, ok := parts[m.ID]; ok {
				continue
			}
			list := []Part{}
			reasonixMessageParts(m, func(_ bool, part Part) bool {
				if part, ok := keep(part); ok {
					list = append(list, part)
				}
				return true
			})
			parts[m.ID] = list
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, m := range p.messages {
		list, ok := parts[m.ID]
		if !ok {
			if m.DisplaySize > 0 {
				add(false, Part{Kind: "text"})
				break
			}
			continue
		}
		for _, part := range list {
			if !add(part.Role == "user", part) {
				return nil
			}
		}
	}
	return nil
}

func reasonixImportedID(seq uint64, index int) string {
	return fmt.Sprintf("magpie-import-%d-%d", seq, index)
}

var reasonixNativeWorkspaceLine = regexp.MustCompile(`(?m)^Current workspace: ("(?:[^"\\]|\\.)*")(?:\.)?$`)

func reasonixNativeWorkspace(m reasonixMessage) string {
	if cwd := reasonixWorkspace(m.Content); cwd != "" && (m.RawContent != nil || m.HostAuthored || m.Origin == "host") {
		return cwd
	}
	if m.Origin != "host" && !m.HostAuthored {
		return ""
	}
	content := strings.TrimSpace(strings.ReplaceAll(m.Content, "\r\n", "\n"))
	if !strings.HasPrefix(content, `<session-context version="1">`+"\n") || !strings.HasSuffix(content, "\n</session-context>") {
		return ""
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(content, `<session-context version="1">`+"\n"), "\n</session-context>")
	marker := "\n\nDigest: sha256:"
	i := strings.LastIndex(inner, marker)
	if i < 0 {
		return ""
	}
	body, digest := inner[:i], inner[i+len(marker):]
	sum := sha256.Sum256([]byte(body))
	if digest != hex.EncodeToString(sum[:]) {
		return ""
	}
	match := reasonixNativeWorkspaceLine.FindStringSubmatch(body)
	var cwd string
	if len(match) == 2 {
		_ = json.Unmarshal([]byte(match[1]), &cwd)
	}
	return cwd
}
