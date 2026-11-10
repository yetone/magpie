package usage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

// A plugin that lists its accounts' calls made outside magpie
// (plugin.Provider.Calls, auth.calls: Cursor's editor and cursor-agent,
// WaiooT on X) has them read into the log as reported calls, so the Usage
// page counts what the subscription spent wherever it was spent. Each is
// written once (its rid, kept by IngestReported); the calls the plugin
// sent for magpie's own gateway are the plugin's to leave out.

// outsideEvery is how often the calls are read, and outsideOverlap how far
// back each reading starts before the last one ended: a vendor lists a
// call some time after it was made, and one listed late is still read.
// outsideBack is how far back the first reading goes.
const (
	outsideEvery   = 10 * time.Minute
	outsideOverlap = 30 * time.Minute
	outsideBack    = 30 * 24 * time.Hour
)

// outsideIO is what a reading reaches outside the package, for the tests.
var outsideIO = struct {
	providers func() []plugin.Provider
	calls     func(ctx context.Context, provider, account string, since time.Time) ([]plugin.OutsideCall, error)
	now       func() time.Time
}{plugin.Cached, plugin.AccountCalls, time.Now}

// outsideMu holds one reading at a time.
var outsideMu sync.Mutex

func outsidePath() string { return filepath.Join(filepath.Dir(Path()), "plugin-calls.json") }

// outsideMarks are when each provider's account (provider + "\x00" + the
// plugin's account key) was last read up to.
func readOutsideMarks() map[string]time.Time {
	m := map[string]time.Time{}
	if b, err := os.ReadFile(outsidePath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func writeOutsideMarks(m map[string]time.Time) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	p := outsidePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// lastThrough is the time of the newest call magpie's gateway sent to
// provider (as the log names it), zero for none. The first reading starts
// after it: the plugin's own calls are left out only from the version that
// knows them, so one sent before could be read as made outside.
func lastThrough(id string) time.Time {
	f, err := os.Open(Path())
	if err != nil {
		return time.Time{}
	}
	defer f.Close()
	mark := []byte(`"provider":` + quoted(id))
	var last time.Time
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, mark) || bytes.Contains(line, reportedMark) {
			continue
		}
		var r struct {
			T        time.Time `json:"t"`
			Provider string    `json:"provider"`
		}
		if json.Unmarshal(line, &r) == nil && r.Provider == id && r.T.After(last) {
			last = r.T
		}
	}
	return last
}

func quoted(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ReadOutsideCalls reads each such plugin's accounts' calls since the
// last reading and writes the new ones to the log. A reading that fails
// keeps its mark, so the next one asks again from there.
func ReadOutsideCalls(ctx context.Context) {
	outsideMu.Lock()
	defer outsideMu.Unlock()
	marks := readOutsideMarks()
	changed := false
	for _, p := range outsideIO.providers() {
		if !p.Calls {
			continue
		}
		id := provider.PluginID(p.ID)
		for _, a := range p.Accounts {
			key := p.ID + "\x00" + a.Key
			now := outsideIO.now()
			since, ok := marks[key]
			if !ok {
				since = now.Add(-outsideBack)
				if t := lastThrough(id); t.After(since) {
					since = t.Add(time.Minute)
				}
			}
			calls, err := outsideIO.calls(ctx, p.ID, a.Key, since)
			if err != nil {
				continue
			}
			user := provider.PluginAccountUser(p.ID, a.Key)
			recs := make([]Record, 0, len(calls))
			for _, c := range calls {
				recs = append(recs, outsideRecord(p.ID, id, user, c))
			}
			if len(recs) > 0 {
				if _, err := IngestReported(recs); err != nil {
					continue
				}
			}
			// the next reading starts a little back from now, for calls
			// listed late; never before this one's start
			next := now.Add(-outsideOverlap)
			if next.Before(since) {
				next = since
			}
			marks[key] = next
			changed = true
		}
	}
	if changed {
		_ = writeOutsideMarks(marks)
	}
}

// outsideRecord is the log's row of a call plugin (whose provider id is
// id) said its account user made outside magpie: by the plugin's own
// agent (Cursor's is the "cursor" agent), with the vendor's cost when it
// gave one.
func outsideRecord(pluginID, id, user string, c plugin.OutsideCall) Record {
	return Record{
		Time:            time.UnixMilli(c.T).UTC(),
		Agent:           pluginID,
		Provider:        id,
		ProviderAccount: user,
		Model:           c.Model,
		Input:           int(c.In),
		Output:          int(c.Out),
		CacheRead:       int(c.CacheRead),
		CacheWrite:      int(c.CacheWrite),
		Status:          200,
		RequestID:       pluginID + ":" + c.RID,
		Session:         c.Session,
		OwnCost:         c.Cost,
	}
}

// KeepOutsideCallsRead reads the calls made outside magpie a minute after
// the start and every outsideEvery after, until ctx ends.
func KeepOutsideCallsRead(ctx context.Context) {
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		rctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		ReadOutsideCalls(rctx)
		cancel()
		t.Reset(outsideEvery)
	}
}
