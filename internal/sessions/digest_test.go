package sessions

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A cache a magpie before digest wrote holds the whole hashes in hex. Read
// now, its files must be the same files, or every one is read again on the
// first start after the update.
func TestDigestReadsTheHashesWrittenBefore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	head := strings.Repeat(`{"type":"session_meta"}`+"\n", 3)
	if err := os.WriteFile(path, []byte(head+strings.Repeat("x", 200<<10)), 0o600); err != nil {
		t.Fatal(err)
	}
	size := int64(len(head) + 200<<10)
	oldHead := fmt.Sprintf("%x", sha256.Sum256([]byte(head)))
	oldHash := oldPrefixHash(path, size)
	if !strings.HasPrefix(oldHash, "sample-v1:") || len(oldHead) != 64 {
		t.Fatalf("old forms %q %q", oldHead, oldHash)
	}

	var st state
	old := fmt.Sprintf(`{"head":%q,"head_size":%d,"content_hash":%q,"size":%d}`, oldHead, len(head), oldHash, size)
	if err := json.Unmarshal([]byte(old), &st); err != nil {
		t.Fatal(err)
	}
	if !sameHead(head, st.Head, int(st.HeadSize)) || st.ContentHash != prefixHash(path, size) || st.ContentHash == (digest{}) {
		t.Fatalf("an old state reads as another file: %x %x", st.Head, st.ContentHash)
	}
	// A shard keeps its hashes as strings: one written before restores the same.
	c := callDiskState{Head: oldHead, ContentHash: oldHash}.restore()
	if c.Head != st.Head || c.ContentHash != st.ContentHash {
		t.Fatalf("an old shard reads as another file: %x %x", c.Head, c.ContentHash)
	}
	if back := diskCallState(c).restore(); back.Head != c.Head || back.ContentHash != c.ContentHash {
		t.Fatal("a shard written now reads back as another file")
	}

	b, err := json.Marshal(&st)
	if err != nil {
		t.Fatal(err)
	}
	var again state
	if err := json.Unmarshal(b, &again); err != nil || again.Head != st.Head || again.ContentHash != st.ContentHash {
		t.Fatalf("written now, read again: %s", b)
	}
	if strings.Contains(string(b), oldHead) || !strings.Contains(string(b), oldHead[:32]) {
		t.Fatalf("not written as the digest: %s", b)
	}

	// None stays none, and isn't written.
	b, _ = json.Marshal(&state{})
	if strings.Contains(string(b), "head") || strings.Contains(string(b), "content_hash") {
		t.Fatalf("no hash written as one: %s", b)
	}
	for _, s := range []string{"", "sample-v1:", "zz" + oldHead[2:], oldHead[:40]} {
		if parseDigest(s) != (digest{}) {
			t.Fatalf("%q read as a digest", s)
		}
	}
}
