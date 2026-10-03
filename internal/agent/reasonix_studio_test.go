package agent

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The official Electron archive stores package metadata after a Pickle header.
func reasonixStudioArchive(t testing.TB, name, version string) []byte {
	t.Helper()
	metadata, err := json.Marshal(map[string]string{"name": name, "version": version})
	if err != nil {
		t.Fatal(err)
	}
	header, err := json.Marshal(map[string]any{"files": map[string]any{
		"package.json": map[string]any{"size": len(metadata), "offset": "0"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	headerSize := (8 + len(header) + 3) &^ 3
	archive := make([]byte, 8+headerSize)
	binary.LittleEndian.PutUint32(archive[0:4], 4)
	binary.LittleEndian.PutUint32(archive[4:8], uint32(headerSize))
	binary.LittleEndian.PutUint32(archive[8:12], uint32(headerSize-4))
	binary.LittleEndian.PutUint32(archive[12:16], uint32(len(header)))
	copy(archive[16:], header)
	return append(archive, metadata...)
}

func TestReasonixStudioPackageRequiresVersionTwo(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		want          bool
	}{
		{"reasonix-studio-electron", "2.24.0", true},
		{"reasonix-studio-electron", "2.25.0-beta.1", true},
		{"reasonix-studio-electron", "1.21.3", false},
		{"reasonix-studio-electron", "0.0.0", false},
		{"some-other-app", "2.24.0", false},
	} {
		t.Run(tc.name+"/"+tc.version, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app.asar")
			if err := os.WriteFile(path, reasonixStudioArchive(t, tc.name, tc.version), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := reasonixStudioPackage(path); got != tc.want {
				t.Fatalf("Studio package detection = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReasonixStudioPackageRejectsMalformedArchive(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("not an archive"), make([]byte, 16),
		{4, 0, 0, 0, 255, 255, 255, 255},
		reasonixStudioArchive(t, "reasonix-studio-electron", "2.24.0")[:20],
	} {
		path := filepath.Join(t.TempDir(), "app.asar")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if reasonixStudioPackage(path) {
			t.Fatal("malformed Electron archive counted as Studio")
		}
	}
}

func FuzzReasonixStudioArchive(f *testing.F) {
	f.Add(reasonixStudioArchive(f, "reasonix-studio-electron", "2.24.0"))
	f.Add([]byte{4, 0, 0, 0, 255, 255, 255, 255})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		reasonixStudioArchiveVersion(bytes.NewReader(data), int64(len(data)))
	})
}
