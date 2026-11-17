package usage

// The usage log in a backup file (JasonLeeForOnly on Discord, who moved
// magpie to a new server with backup and restore and lost every call's
// history): LogBackup packs usage.jsonl's lines, gzipped, and MergeLog
// adds the ones this computer's log doesn't have yet. A line is the
// call's record as it was written, so a line already here, from an
// earlier restore of the same backup or from the log itself, is the same
// bytes and is never added twice.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"hash/maphash"
	"io"
	"os"
	"path/filepath"
)

// LogBackup is usage.jsonl's whole lines, gzipped, and how many there
// are: none (nil, 0) when there is no log. A line still being written
// when it is read is left for the next backup.
func LogBackup() ([]byte, int, error) {
	f, err := os.Open(Path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	var z bytes.Buffer
	w, _ := gzip.NewWriterLevel(&z, gzip.BestCompression)
	n := 0
	rd := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			if err != io.EOF {
				return nil, 0, err
			}
			break // an unfinished last line
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if _, err := w.Write(line); err != nil {
			return nil, 0, err
		}
		n++
	}
	if err := w.Close(); err != nil {
		return nil, 0, err
	}
	if n == 0 {
		return nil, 0, nil
	}
	return z.Bytes(), n, nil
}

// logLine is a line as MergeLog compares it: without its line ending.
func logLine(b []byte) []byte { return bytes.TrimRight(b, " \t\r\n") }

// MergeLog adds the lines of a log LogBackup packed that usage.jsonl
// doesn't have, at its end, and says how many it added. A line the log
// has is passed over as often as the log has it, so a restore made twice
// adds nothing the second time, and two calls that were the same bytes
// both stay. A line that isn't a JSON object is passed over.
func MergeLog(gz []byte) (int, error) {
	if len(gz) == 0 {
		return 0, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return 0, err
	}
	seed := maphash.MakeSeed()
	have := map[uint64]int32{}
	path := Path()
	// most of the log is read before the gateway's lock is taken, so a
	// long one doesn't hold its calls up; what it wrote meanwhile is read
	// under the lock below
	off, err := countLines(path, 0, seed, have)
	if err != nil {
		return 0, err
	}
	mu.Lock()
	defer mu.Unlock()
	if _, err := countLines(path, off, seed, have); err != nil {
		return 0, err
	}
	// the log is opened once a line is new, and written to a few MB at a
	// time: a long history isn't held whole twice over
	var f *os.File
	var before os.FileInfo
	var buf []byte
	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		if f == nil {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			var err error
			if f, err = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err != nil {
				return err
			}
			before, _ = statLogHandle(f)
			// a log that ends in an unfinished line gets its own: the first
			// line added is never run into it
			if before != nil && before.Size() > 0 && !endsInNewline(path, before.Size()) {
				buf = append([]byte{'\n'}, buf...)
			}
		}
		_, err := f.Write(buf)
		buf = buf[:0]
		return err
	}
	added := 0
	rd := bufio.NewReaderSize(zr, 64<<10)
	var werr error
	for werr == nil {
		raw, err := rd.ReadBytes('\n')
		line := logLine(raw)
		if len(line) > 0 && line[0] == '{' && json.Valid(line) {
			k := maphash.Bytes(seed, line)
			if have[k] > 0 {
				have[k]--
			} else {
				buf = append(append(buf, line...), '\n')
				added++
				if len(buf) >= 4<<20 {
					werr = flush()
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				werr = err
			}
			break
		}
	}
	if werr == nil {
		werr = flush()
	}
	if f != nil {
		after, _ := statLogHandle(f)
		noteLogAppend(path, before, after)
		f.Close()
	}
	if werr != nil {
		// what part of it reached the log is there: a restore again adds
		// the rest, and not what reached it
		return added, werr
	}
	return added, nil
}

// countLines counts the whole lines of the log from off on into have, by
// their hash, and says where the last whole one ended.
func countLines(path string, off int64, seed maphash.Seed, have map[uint64]int32) (int64, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return off, nil
	}
	if err != nil {
		return off, err
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return off, err
	}
	rd := bufio.NewReaderSize(f, 64<<10)
	for {
		raw, err := rd.ReadBytes('\n')
		if err != nil {
			if err != io.EOF {
				return off, err
			}
			return off, nil // an unfinished line is read again under the lock
		}
		off += int64(len(raw))
		if line := logLine(raw); len(line) > 0 {
			have[maphash.Bytes(seed, line)]++
		}
	}
}

// endsInNewline says whether the log's byte at size-1 is a newline.
func endsInNewline(path string, size int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	b := make([]byte, 1)
	if _, err := f.ReadAt(b, size-1); err != nil {
		return true
	}
	return b[0] == '\n'
}
