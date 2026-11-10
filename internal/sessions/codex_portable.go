package sessions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/claudeseal"
	"github.com/yetone/magpie/internal/codexsummary"
	"github.com/yetone/magpie/internal/filelock"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/steady"
)

// A Codex conversation magpie compacted keeps magpie's compaction item in
// its rollout, and Codex sends it back with every later turn: to OpenAI
// itself, once magpie is quit or disconnected, it is refused
// (invalid_encrypted_content) and the conversation can't go on (LukeMa on
// X). PortCodexRollouts writes, in each rollout that has one, the message
// it stands for in its place (codexsummary.Message): what Codex's own
// local compaction keeps, and what the gateway sends for it anyway, so a
// rollout so written reads the same through magpie and goes on without it.
//
// A reasoning item magpie gave Codex with Claude's block in its
// encrypted_content (claudeseal) is refused the same way; it is written
// as its summary, as the gateway sends it to OpenAI.
//
// A reasoning item magpie gave Codex for a reply another vendor made
// (WorkBuddy, DeepSeek, Kimi …) isn't one OpenAI sealed, and once Codex
// talks to OpenAI itself every turn of the thread is refused, which
// Codex doesn't retry (hentulong on X, after routing Codex to WorkBuddy
// and back; each tried on a real ChatGPT account):
//   - a summary and no encrypted_content: the history is taken as a
//     warmup, and the turn sent on it with previous_response_id is
//     refused, 400 unsupported_persisted_item_context, "Persisted response
//     contains hosted-tool, compaction, or unverifiable hidden reasoning
//     state that Rustponses cannot replay";
//   - its reasoning_text in content: 400 array_above_max_length
//     ("Invalid 'input[5].content': array too long");
//   - another API's encrypted_content: invalid_encrypted_content.
//
// Every reasoning item OpenAI didn't seal (its encrypted_content is a
// Fernet token, "gAAAA…"), one with Claude's block too, is written as
// magpie_reasoning, a type Codex reads as one it doesn't know and never
// sends (ResponseItem::Other), its summary and content kept for magpie's
// own reading. That is done only as magpie lets Codex go
// (PortCodexRolloutsLeft), as through magpie the gateway sends each as
// the model it goes to takes it (a DeepSeek or Kimi one gets its
// reasoning back from them), and only in a thread begun on OpenAI's or
// magpie's provider: one on another provider of the user's own is theirs.
//
// Only magpie's compaction items and those reasoning items change, in
// whichever line holds them (a response_item, a compacted line's
// replacement_history); every other line keeps its bytes, and the file
// its line count, mode and time. A rollout is changed as Codex publishes
// a compressed one
// (codex-rs/rollout/src/writer_lock.rs, try_acquire_for_publication):
// under its home's coordination lock, and only when no Codex holds the
// thread's writer lock, so never under a Codex that has the thread open.
// One that does is left for a later pass. A Codex home without
// thread-writer-locks (a Codex from before them) is left alone, as there
// is no telling whether a Codex is writing there.

// codexPortState is what PortCodexRollouts keeps between passes, per
// Codex home: when its last whole pass began, and the rollouts it had to
// leave for a Codex that held them.
type codexPortState struct {
	Homes map[string]codexPortHome `json:"homes"`
}

type codexPortHome struct {
	Since   time.Time `json:"since"`
	Pending []string  `json:"pending,omitempty"`
	// Lineage: every fork's place in its parent was looked at once
	// (repairLineages), after magpie before it moved them
	Lineage bool `json:"lineage,omitempty"`
}

// codexPortFirst is the first day magpie wrote a compaction item of its
// own (c3931c91d, 2026-09-25): no rollout last written before then has one.
var codexPortFirst = time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)

// codexLeftFirst is the day before magpie's first commit (2026-09-22): no
// rollout last written before then has a reasoning item of magpie's.
var codexLeftFirst = time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)

// codexLeftKey is the state a PortCodexRolloutsLeft pass keeps for a
// home, apart from the other passes': one of those, while Codex went
// through magpie, left magpie's reasoning items as they were.
const codexLeftKey = " (left)"

// codexPortSlack is how far before a pass began the next one looks again:
// a file's time can be coarser than the clock.
const codexPortSlack = 2 * time.Minute

func codexPortPath() string { return filepath.Join(settings.Dir(), "codex-portable.json") }

// CodexPort is what a pass did.
type CodexPort struct {
	Rewritten []string // rollouts written portable
	Held      []string // rollouts with magpie's item a Codex holds, left for a later pass
	Repaired  []string // forks whose place in their parent was set again (repairLineages)
	Done      bool     // every rollout due was looked at
}

// PortCodexRollouts makes the rollouts of the Codex home home portable,
// those written since the last whole pass and those it left before, until
// ctx ends. The first pass reads every rollout written since magpie began
// to compact; later ones only what changed since.
func PortCodexRollouts(ctx context.Context, home string) (CodexPort, error) {
	return portCodexRollouts(ctx, home, false)
}

// PortCodexRolloutsLeft is PortCodexRollouts for a Codex that no longer
// goes through magpie: magpie's reasoning items are written as
// magpie_reasoning too, as OpenAI can't take them as a base.
func PortCodexRolloutsLeft(ctx context.Context, home string) (CodexPort, error) {
	return portCodexRollouts(ctx, home, true)
}

func portCodexRollouts(ctx context.Context, home string, left bool) (CodexPort, error) {
	var res CodexPort
	locks := filepath.Join(home, "thread-writer-locks")
	if fi, err := os.Stat(locks); err != nil || !fi.IsDir() {
		return res, nil
	}
	// one pass at a time across magpies
	unlock, held := filelock.Lock(codexPortPath()+".lock", 0)
	if !held {
		return res, nil
	}
	defer unlock()
	var st codexPortState
	if b, err := os.ReadFile(codexPortPath()); err == nil {
		if json.Unmarshal(b, &st) != nil {
			st = codexPortState{} // unreadable: a whole pass again, which only costs time
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return res, err
	}
	if st.Homes == nil {
		st.Homes = map[string]codexPortHome{}
	}
	key, first := filepath.Clean(home), codexPortFirst
	if left {
		key, first = key+codexLeftKey, codexLeftFirst
	}
	was := st.Homes[key]
	since := first
	if !was.Since.IsZero() {
		since = was.Since.Add(-codexPortSlack)
	}
	start := time.Now()
	var due []string
	seen := map[string]bool{}
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			due = append(due, p)
		}
	}
	for _, p := range was.Pending {
		for _, q := range []string{p, rolloutTwin(p)} { // Codex may have compressed it since
			if fi, err := os.Stat(q); err == nil && fi.Mode().IsRegular() {
				add(q)
			}
		}
	}
	for _, folder := range []string{"sessions", "archived_sessions"} {
		filepath.WalkDir(filepath.Join(home, folder), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || codexThreadOf(d.Name()) == "" {
				return nil
			}
			if fi, err := d.Info(); err == nil && fi.Mode().IsRegular() && !fi.ModTime().Before(since) {
				add(p)
			}
			return nil
		})
	}
	res.Done = true
	var pending []string
	for i, p := range due {
		if ctx.Err() != nil {
			res.Done = false
			pending = append(pending, due[i:]...)
			break
		}
		changed, err := portRollout(ctx, home, p, left)
		switch {
		case errors.Is(err, errRolloutHeld):
			res.Held = append(res.Held, p)
			pending = append(pending, p)
		case err != nil:
			if ctx.Err() != nil {
				res.Done = false
				pending = append(pending, due[i:]...)
				break
			}
			log.Printf("codex rollout %s: %v", p, err)
		case changed:
			res.Rewritten = append(res.Rewritten, p)
		}
		if !res.Done {
			break
		}
	}
	now := codexPortHome{Since: was.Since, Pending: pending, Lineage: was.Lineage}
	if res.Done && !was.Lineage {
		fixed, whole := repairLineages(ctx, home)
		res.Repaired = fixed
		now.Lineage = whole
	}
	if res.Done {
		now.Since = start
	} else {
		now.Pending = append(now.Pending, was.Pending...)
		now.Pending = uniq(now.Pending)
	}
	st.Homes[key] = now
	b, _ := json.MarshalIndent(st, "", "  ")
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		return res, err
	}
	return res, steady.WriteFile(codexPortPath(), b, 0o600)
}

func uniq(list []string) []string {
	var out []string
	for _, s := range list {
		if !contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// errRolloutHeld is a rollout a Codex has open: its thread's writer lock
// is held, or the file changed while it was read.
var errRolloutHeld = errors.New("held by a Codex")

// codexRolloutName is Codex's name for a rollout (rollout_file_name.rs):
// its time, its thread and, for a later rollout of the thread, its own id.
var codexRolloutName = lazyre.New(`^rollout-\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}-([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})(?:_[0-9a-fA-F-]{36})?\.jsonl(?:\.zst)?$`)

// codexThreadOf is the thread a rollout's file name says it is of; "" for
// a file that isn't one.
func codexThreadOf(name string) string {
	m := codexRolloutName.FindStringSubmatch(name)
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}

var magpieMark = []byte(codexsummary.Mark)

// claudeMark begins a reasoning item's encrypted_content carrying Claude's
// block (claudeseal), which only magpie reads.
var claudeMark = []byte(claudeseal.Mark)

// reasoningMark is in a line with a reasoning item, as Codex writes it;
// openAISealed begins the encrypted_content of one OpenAI sealed (a
// Fernet token).
var reasoningMark, openAISealed = []byte(`"type":"reasoning"`), []byte(`"encrypted_content":"gAAAA`)

// unsealedReasoning says a line may hold a reasoning item OpenAI didn't
// seal, which a line of OpenAI's own reasoning (most of them) doesn't; a
// compacted line, with other items' seals beside, is always looked at.
func unsealedReasoning(line []byte) bool {
	n := bytes.Count(line, reasoningMark)
	return n > 0 && (n > bytes.Count(line, openAISealed) || bytes.Contains(line[:min(len(line), 256)], []byte(`"type":"compacted"`)))
}

// portRollout writes the rollout at path portable, if it has magpie's
// compaction item (or, left, a reasoning item OpenAI didn't seal), and
// says whether it did.
func portRollout(ctx context.Context, home, path string, left bool) (bool, error) {
	return editRollout(ctx, home, path, func() func(line []byte, paged bool) ([]byte, bool) {
		ours := true // begun on OpenAI's provider or magpie's
		return func(line []byte, paged bool) ([]byte, bool) {
			if isMetaLine(line) {
				switch gjson.GetBytes(line, "payload.model_provider").String() {
				case "", "openai", "magpie":
				default:
					ours = false
				}
			}
			out, ok := portLineAs(line, left && ours)
			if ok {
				out, ok = fitLine(line, out, paged)
			}
			return out, ok
		}
	})
}

// editRollout writes the rollout at path with each line as an edit from
// edits gives it, if one changed a line, and says whether it did; paged is
// whether the rollout's history is paginated. The rollout is read once to
// see whether a line changes, and only then again to write it: most
// rollouts a pass looks at have nothing to change, and writing each one
// out, compressed for a .zst, cost a pass more than reading it (#4735).
func editRollout(ctx context.Context, home, path string, edits func() func(line []byte, paged bool) ([]byte, bool)) (bool, error) {
	thread := codexThreadOf(filepath.Base(path))
	if thread == "" {
		return false, nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if changed, err := eachEditedLine(ctx, path, edits(), nil); err != nil || !changed {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".magpie-"+filepath.Base(path)+"-*")
	if err != nil {
		return false, err
	}
	rolloutWrites.Add(1)
	done := false
	var z *zstd.Encoder
	defer func() {
		if !done {
			if z != nil {
				z.Close()
			}
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	var w io.Writer = tmp
	if packed(path) {
		if z, err = zstd.NewWriter(tmp); err != nil {
			return false, err
		}
		w = z
	}
	bw := bufio.NewWriterSize(w, 64<<10)
	changed, err := eachEditedLine(ctx, path, edits(), bw)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, errRolloutHeld // it changed between the reads
	}
	if err := bw.Flush(); err != nil {
		return false, err
	}
	if z != nil {
		err := z.Close()
		z = nil
		if err != nil {
			return false, err
		}
	}
	if err := tmp.Chmod(fi.Mode().Perm()); err != nil {
		return false, err
	}
	if err := steady.Sync(tmp); err != nil {
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	os.Chtimes(tmp.Name(), fi.ModTime(), fi.ModTime())

	// published as Codex publishes a compressed rollout: under the
	// coordination lock every Codex writer takes before its thread's
	// lock, with that lock free (or never made)
	locks := filepath.Join(home, "thread-writer-locks")
	unlock, held := filelock.Lock(filepath.Join(locks, ".coordination.lock"), filelock.Wait)
	if !held {
		return false, errRolloutHeld
	}
	defer unlock()
	if lf, err := os.OpenFile(filepath.Join(locks, thread+".lock"), os.O_RDWR, 0); err == nil {
		got, err := filelock.TryLock(lf)
		if err != nil || !got {
			lf.Close()
			return false, errRolloutHeld
		}
		defer func() { filelock.Unlock(lf); lf.Close() }()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, errRolloutHeld
	}
	// written to since it was read: a Codex had it after all
	if now, err := os.Stat(path); err != nil || now.Size() != fi.Size() || !now.ModTime().Equal(fi.ModTime()) {
		return false, errRolloutHeld
	}
	if err := steady.Rename(tmp.Name(), path); err != nil {
		return false, err
	}
	done = true
	return true, nil
}

// rolloutWrites counts the rollouts editRollout began to write out.
var rolloutWrites atomic.Int64

// eachEditedLine reads the rollout at path through edit, writing each
// line as edit gives it to w (when there is one), and says whether edit
// changed a line.
func eachEditedLine(ctx context.Context, path string, edit func(line []byte, paged bool) ([]byte, bool), w io.Writer) (bool, error) {
	in, err := openLines(path)
	if err != nil {
		return false, err
	}
	defer in.Close()
	br := bufio.NewReaderSize(in, 64<<10)
	changed := false
	paged, metaSeen := false, false
	for n := 0; ; n++ {
		if n%4096 == 0 && ctx.Err() != nil {
			return false, ctx.Err()
		}
		line, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			var long []byte
			long, err = readLongLine(br, line)
			line = long
		}
		if len(line) > 0 {
			if !metaSeen && isMetaLine(line) {
				paged, metaSeen = paginated(line), true
			}
			out, ok := edit(line, paged)
			changed = changed || ok
			if w == nil && changed {
				return true, nil
			}
			if w != nil {
				if _, werr := w.Write(out); werr != nil {
					return false, werr
				}
			}
		}
		if err == io.EOF {
			return changed, nil
		}
		if err != nil {
			return false, err
		}
	}
}

// readLongLine is the rest of a line longer than br's buffer, after its
// start: ReadSlice's, which the next read writes over, so it is copied.
func readLongLine(br *bufio.Reader, start []byte) ([]byte, error) {
	line := append([]byte{}, start...)
	rest, err := br.ReadBytes('\n')
	return append(line, rest...), err
}

// Codex keeps places in a rollout by byte: a fork's or a revert's
// rollout names where its parent's history ends (session_meta's
// history_base.end_byte_offset), and Codex's state database where each
// turn starts and ends and how far it has read (thread_turns,
// thread_history_projection_state). A rewritten line that is shorter or
// longer moves every place after it, and a rollout that shrinks leaves
// them past its end: Codex then won't open the thread ("invalid paginated
// history lineage …: cutoff byte offset is past the source rollout",
// xybio on Discord). So a line magpie rewrites keeps its length: a
// shorter one is padded with spaces after its JSON, which Codex's reader
// (serde_json) skips, and a longer one, in a rollout whose history is
// paginated, is not written at all.

// isMetaLine says whether a rollout line is a session_meta.
func isMetaLine(line []byte) bool {
	return bytes.Contains(line[:min(len(line), 1024)], cxMeta) && gjson.GetBytes(line, "type").String() == "session_meta"
}

// paginated says whether a session_meta line's rollout keeps its history
// paginated, the one whose places Codex keeps by byte.
func paginated(meta []byte) bool {
	return gjson.GetBytes(meta, "payload.history_mode").String() == "paginated"
}

// fitLine is out, a rewrite of line, as long as line: padded with spaces
// after its JSON when it is shorter (spaces line had there are taken
// first). One that is longer is out as it is, unless paged: then it is
// line, and false.
func fitLine(line, out []byte, paged bool) ([]byte, bool) {
	body := bytes.TrimRight(line, "\r\n")
	end := line[len(body):]
	nb := bytes.TrimRight(bytes.TrimRight(out, "\r\n"), " ")
	if len(nb) > len(body) {
		if paged {
			return line, false
		}
		return out, true
	}
	fit := make([]byte, 0, len(line))
	fit = append(fit, nb...)
	fit = append(fit, bytes.Repeat([]byte{' '}, len(body)-len(nb))...)
	return append(fit, end...), true
}

// A fork made before magpie kept a parent's lines whole names a byte in
// it that a shorter parent no longer ends a line at, or no longer has:
// Codex refuses to open it. Every line kept its place by count, though,
// and Codex names each line's ordinal, so repairLineages sets each such
// place again to where the parent's line before history_base's
// end_ordinal_exclusive ends now, in the fork's session_meta, which keeps
// its own length (fitLine). It says which forks it wrote, and whether it
// looked at every one (none held by a Codex, ctx not done).
func repairLineages(ctx context.Context, home string) (fixed []string, whole bool) {
	rollouts := map[string]string{} // rollout id → its file
	var all []string
	for _, folder := range []string{"sessions", "archived_sessions"} {
		filepath.WalkDir(filepath.Join(home, folder), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			if id := codexRolloutID(d.Name()); id != "" {
				rollouts[id] = p
				all = append(all, p)
			}
			return nil
		})
	}
	whole = true
	known := map[string]map[int64]int64{} // a parent's ordinalEnds
	for _, p := range all {
		if ctx.Err() != nil {
			return fixed, false
		}
		meta := firstLine(p)
		base := gjson.GetBytes(meta, "payload.history_base")
		if !isMetaLine(meta) || !paginated(meta) || !base.IsObject() {
			continue
		}
		parent, ok := rollouts[strings.ToLower(base.Get("thread_id").String())]
		n := base.Get("end_ordinal_exclusive").Int()
		if !ok || n <= 0 {
			continue
		}
		ends, seen := known[parent]
		if !seen {
			ends = ordinalEnds(parent)
			known[parent] = ends
		}
		at, ok := ends[n-1]
		if !ok || at == base.Get("end_byte_offset").Int() {
			continue
		}
		changed, err := editRollout(ctx, home, p, func() func(line []byte, paged bool) ([]byte, bool) {
			return func(line []byte, paged bool) ([]byte, bool) {
				r := gjson.GetBytes(line, "payload.history_base.end_byte_offset")
				if !isMetaLine(line) || r.Index <= 0 || r.Type != gjson.Number {
					return line, false
				}
				out := append([]byte{}, line[:r.Index]...)
				out = strconv.AppendInt(out, at, 10)
				out = append(out, line[r.Index+len(r.Raw):]...)
				return fitLine(line, out, true)
			}
		})
		switch {
		case err != nil:
			whole = false
			if !errors.Is(err, errRolloutHeld) {
				log.Printf("codex rollout %s: %v", p, err)
			}
		case changed:
			fixed = append(fixed, p)
		}
	}
	return fixed, whole
}

// codexRolloutID is the id a rollout's file name gives it: a later
// rollout of a thread's own, else its thread's.
func codexRolloutID(name string) string {
	m := codexRolloutName.FindStringSubmatch(name)
	if m == nil {
		return ""
	}
	if i := strings.IndexByte(name, '_'); i > 0 {
		return strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(name[i+1:], zstSuffix), ".jsonl"))
	}
	return strings.ToLower(m[1])
}

// firstLine is a rollout's first line, "" when it can't be read.
func firstLine(path string) []byte {
	r, err := openLines(path)
	if err != nil {
		return nil
	}
	defer r.Close()
	line, _ := bufio.NewReaderSize(r, 64<<10).ReadBytes('\n')
	return line
}

// ordinalEnds is where, in a rollout's lines, each line Codex numbered
// ends, its newline included, up to the first line without an ordinal
// (after which there is no telling).
func ordinalEnds(path string) map[int64]int64 {
	ends := map[int64]int64{}
	r, err := openLines(path)
	if err != nil {
		return ends
	}
	defer r.Close()
	br := bufio.NewReaderSize(r, 64<<10)
	var at int64
	for {
		line, err := br.ReadBytes('\n')
		if len(line) == 0 || line[len(line)-1] != '\n' {
			return ends
		}
		at += int64(len(line))
		o := gjson.GetBytes(line, "ordinal")
		if o.Type != gjson.Number {
			return ends
		}
		ends[o.Int()] = at
		if err != nil {
			return ends
		}
	}
}

// portLineAs is portLine, with magpie's reasoning items written as
// magpie_reasoning first when left.
func portLineAs(line []byte, left bool) ([]byte, bool) {
	retired := false
	if left && unsealedReasoning(line) {
		line, retired = retireReasoning(line)
	}
	out, ok := portLine(line)
	return out, ok || retired
}

// retireReasoning is a rollout line with each reasoning item in it that
// OpenAI didn't seal (a response_item's, those in a compacted line's
// replacement_history) written as magpie_reasoning: a type Codex reads
// as one it doesn't know (ResponseItem::Other) and never sends. The item
// keeps its summary, content and keys' order, without its id and
// encrypted_content, so the line is no longer; the rest of the line keeps
// its bytes, as magpie's own readers read a line's head by them.
func retireReasoning(line []byte) ([]byte, bool) {
	body := bytes.TrimRight(line, "\r\n")
	if !gjson.ValidBytes(body) {
		return line, false
	}
	var at []gjson.Result // items, in the order they are in body, with their place
	switch gjson.GetBytes(body, "type").String() {
	case "response_item":
		at = append(at, gjson.GetBytes(body, "payload"))
	case "compacted":
		h := gjson.GetBytes(body, "payload.replacement_history")
		if !h.IsArray() || h.Index <= 0 {
			break
		}
		from := 0
		h.ForEach(func(_, v gjson.Result) bool {
			i := strings.Index(h.Raw[from:], v.Raw)
			if i < 0 {
				return false
			}
			v.Index = h.Index + from + i
			from += i + len(v.Raw)
			at = append(at, v)
			return true
		})
	}
	var out []byte
	done := 0
	for _, it := range at {
		if it.Index <= 0 || !it.IsObject() || it.Get("type").String() != "reasoning" {
			continue
		}
		if strings.HasPrefix(it.Get("encrypted_content").String(), "gAAAA") {
			continue // OpenAI's own
		}
		var obj []byte
		it.ForEach(func(k, v gjson.Result) bool {
			switch k.String() {
			case "id", "encrypted_content":
				return true
			}
			if len(obj) > 0 {
				obj = append(obj, ',')
			}
			obj = append(obj, k.Raw...)
			obj = append(obj, ':')
			if k.String() == "type" {
				obj = append(obj, `"magpie_reasoning"`...)
			} else {
				obj = append(obj, v.Raw...)
			}
			return true
		})
		out = append(out, body[done:it.Index]...)
		out = append(out, '{')
		out = append(out, obj...)
		out = append(out, '}')
		done = it.Index + len(it.Raw)
	}
	if out == nil {
		return line, false
	}
	out = append(out, line[done:]...)
	return out, true
}

// portLine is a rollout line with each of magpie's compaction items in it
// written as the message it stands for; any other line as it is, byte for
// byte. One of magpie's items whose summary can't be read is left out of
// a list (and its entry of replacement_history_metadata with it), as the
// gateway leaves it out; a line that is one alone stays as it is, as
// leaving a line out would move every line after it.
func portLine(line []byte) ([]byte, bool) {
	if !bytes.Contains(line, magpieMark) && !bytes.Contains(line, claudeMark) {
		return line, false
	}
	body := bytes.TrimRight(line, "\r\n")
	end := line[len(body):]
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	var v any
	if d.Decode(&v) != nil || d.More() {
		return line, false
	}
	v, changed, _ := portValue(v)
	if !changed {
		return line, false
	}
	var buf bytes.Buffer
	e := json.NewEncoder(&buf)
	e.SetEscapeHTML(false)
	if e.Encode(v) != nil {
		return line, false
	}
	return append(bytes.TrimRight(buf.Bytes(), "\n"), end...), true
}

// portValue is v with magpie's compaction items in it written as their
// messages; drop says v is one of magpie's that can't be read.
func portValue(v any) (_ any, changed, drop bool) {
	switch x := v.(type) {
	case map[string]any:
		if t, _ := x["type"].(string); t == "compaction" || t == "compaction_summary" {
			enc, _ := x["encrypted_content"].(string)
			if msg, ours := codexsummary.Message(enc); ours {
				if msg == nil {
					return x, false, true
				}
				return msg, true, false
			}
			return x, false, false
		}
		if t, _ := x["type"].(string); t == "reasoning" {
			// Claude's block magpie gave Codex to carry back: sent to
			// OpenAI it would be refused as invalid_encrypted_content, so
			// the item goes as its summary, as magpie sends it to OpenAI
			if enc, _ := x["encrypted_content"].(string); claudeseal.Is(enc) {
				delete(x, "encrypted_content")
				return x, true, false
			}
			return x, false, false
		}
		for k, c := range x {
			list, isList := c.([]any)
			if !isList {
				if nv, ch, dr := portValue(c); ch && !dr {
					x[k], changed = nv, true
				}
				continue
			}
			// Codex reads replacement_history_metadata beside
			// replacement_history, one entry for each item: an item is
			// left out only with its entry, and where the two don't line
			// up (Codex refuses that line anyway) none is
			meta, hasMeta := x[k+"_metadata"]
			metaList, _ := meta.([]any)
			drop := !hasMeta || meta == nil || len(metaList) == len(list)
			kept, gone, ch := portList(list, drop)
			if !ch {
				continue
			}
			x[k], changed = kept, true
			if len(gone) > 0 && metaList != nil {
				m := []any{}
				for i, e := range metaList {
					if !gone[i] {
						m = append(m, e)
					}
				}
				x[k+"_metadata"] = m
			}
		}
		return x, changed, false
	case []any:
		kept, _, ch := portList(x, true)
		return kept, ch, false
	}
	return v, false, false
}

func portList(list []any, drop bool) (_ []any, gone map[int]bool, changed bool) {
	out := make([]any, 0, len(list))
	for i, e := range list {
		nv, ch, dr := portValue(e)
		if dr && drop {
			if gone == nil {
				gone = map[int]bool{}
			}
			gone[i], changed = true, true
			continue
		}
		changed = changed || ch
		out = append(out, nv)
	}
	return out, gone, changed
}
