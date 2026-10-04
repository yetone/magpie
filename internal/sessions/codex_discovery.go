package sessions

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
)

const (
	codexCopiesDiffer = iota
	codexCopiesEqual
	codexFirstPrefix
	codexSecondPrefix
	maxCodexCopyPairs = 4096
)

type codexCopyPair struct{ First, Second string }
type codexCopyResult struct {
	First, Second os.FileInfo
	Relation      int
}

var codexCopyCache = struct {
	sync.Mutex
	Pairs map[codexCopyPair]codexCopyResult
}{Pairs: map[codexCopyPair]codexCopyResult{}}

// Windows replaces this with a same-handle stat carrying ChangeTime.
var codexDiscoveryStat = os.Stat
var compareCodexCopies = readCodexCopies

type codexDiscoveryInfo struct {
	os.FileInfo
	ChangeTime string
}

func codexChangeStamp(info os.FileInfo) string {
	if wrapped, ok := info.(codexDiscoveryInfo); ok {
		return wrapped.ChangeTime
	}
	// Darwin and Linux expose nanosecond ctime under different field names.
	// An unsupported filesystem gets no cached content comparison: mtime
	// alone cannot detect a same-size rewrite with a restored timestamp.
	v := reflect.ValueOf(info.Sys())
	if v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return ""
	}
	for _, name := range []string{"Ctimespec", "Ctim"} {
		c := v.FieldByName(name)
		if c.IsValid() && c.Kind() == reflect.Struct {
			sec, nsec := c.FieldByName("Sec"), c.FieldByName("Nsec")
			if sec.IsValid() && nsec.IsValid() && sec.CanInt() && nsec.CanInt() {
				return fmt.Sprintf("%d:%d", sec.Int(), nsec.Int())
			}
		}
	}
	return ""
}

func sameCodexSnapshot(a, b os.FileInfo) bool {
	if a == nil || b == nil || a.Size() != b.Size() || !a.ModTime().Equal(b.ModTime()) || codexChangeStamp(a) != codexChangeStamp(b) {
		return false
	}
	if wrapped, ok := a.(codexDiscoveryInfo); ok {
		a = wrapped.FileInfo
	}
	if wrapped, ok := b.(codexDiscoveryInfo); ok {
		b = wrapped.FileInfo
	}
	return os.SameFile(a, b)
}

// Only a proven decoded prefix is a redundant copy. A shared thread/name
// alone cannot establish ownership of divergent logs or distinct segments.
func distinctCodexFiles(files []file) []file {
	groups := map[string][]int{}
	keep := make([]bool, len(files))
	for i, f := range files {
		keep[i] = true
		name := strings.TrimSuffix(filepath.Base(f.path), zstSuffix)
		for _, j := range groups[name] {
			if !keep[j] {
				continue
			}
			switch codexCopyRelation(files[j].path, f.path) {
			case codexCopiesEqual, codexSecondPrefix:
				keep[i] = false
			case codexFirstPrefix:
				keep[j] = false
			}
			if !keep[i] {
				break
			}
		}
		groups[name] = append(groups[name], i)
	}
	out := files[:0]
	for i, f := range files {
		if keep[i] {
			out = append(out, f)
		}
	}
	return out
}

func codexCopyRelation(first, second string) int {
	a, errA := codexDiscoveryStat(first)
	b, errB := codexDiscoveryStat(second)
	if errA != nil || errB != nil {
		return codexCopiesDiffer
	}
	key := codexCopyPair{first, second}
	cacheable := codexChangeStamp(a) != "" && codexChangeStamp(b) != ""
	codexCopyCache.Lock()
	saved, found := codexCopyCache.Pairs[key]
	codexCopyCache.Unlock()
	if cacheable && found && sameCodexSnapshot(a, saved.First) && sameCodexSnapshot(b, saved.Second) {
		return saved.Relation
	}
	relation, err := compareCodexCopies(first, second)
	if err != nil {
		return codexCopiesDiffer
	}
	// A comparison spanning an append/replacement is not a stable proof.
	afterA, errA := codexDiscoveryStat(first)
	afterB, errB := codexDiscoveryStat(second)
	if errA != nil || errB != nil || !sameCodexSnapshot(a, afterA) || !sameCodexSnapshot(b, afterB) {
		return codexCopiesDiffer
	}
	if cacheable {
		codexCopyCache.Lock()
		if len(codexCopyCache.Pairs) >= maxCodexCopyPairs {
			for old := range codexCopyCache.Pairs {
				delete(codexCopyCache.Pairs, old)
				break
			}
		}
		codexCopyCache.Pairs[key] = codexCopyResult{a, b, relation}
		codexCopyCache.Unlock()
	}
	return relation
}

// Stream comparisons use constant memory. When one stream ends, consume the
// other to validate its compression trailer before selecting the longer copy.
func readCodexCopies(first, second string) (int, error) {
	a, err := openLines(first)
	if err != nil {
		return 0, err
	}
	defer a.Close()
	b, err := openLines(second)
	if err != nil {
		return 0, err
	}
	defer b.Close()
	x, y := make([]byte, 32<<10), make([]byte, 32<<10)
	for {
		nx, endA, ex := readCodexChunk(a, x)
		ny, endB, ey := readCodexChunk(b, y)
		if ex != nil {
			return 0, ex
		}
		if ey != nil {
			return 0, ey
		}
		if !bytes.Equal(x[:min(nx, ny)], y[:min(nx, ny)]) {
			return codexCopiesDiffer, nil
		}
		if nx < ny {
			_, err = io.Copy(io.Discard, b)
			return codexFirstPrefix, err
		}
		if ny < nx {
			_, err = io.Copy(io.Discard, a)
			return codexSecondPrefix, err
		}
		if endA && endB {
			return codexCopiesEqual, nil
		}
	}
}

// io.ReadFull turns a clean short EOF into ErrUnexpectedEOF, making it
// indistinguishable from a truncated zstd frame. Preserve that distinction.
func readCodexChunk(r io.Reader, b []byte) (n int, end bool, err error) {
	for n < len(b) {
		read, err := r.Read(b[n:])
		n += read
		if err == io.EOF {
			return n, true, nil
		}
		if err != nil {
			return n, false, err
		}
		if read == 0 {
			return n, false, io.ErrNoProgress
		}
	}
	return n, false, nil
}
