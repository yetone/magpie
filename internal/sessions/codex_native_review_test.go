package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// These usage-only fixtures were extracted from real rollouts. In particular,
// the page's counters have different bases and its history parent is absent.
func TestCodexNativeReadersAgree(t *testing.T) {
	cases := []struct {
		name   string
		calls  int
		tokens Tokens
	}{
		{"codex-native-paginated.jsonl", 3, Tokens{128290, 293, 254720, 0}},
		{"codex-native-compaction.jsonl", 4, Tokens{22857, 8001, 520320, 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, sourceFirst := range []bool{false, true} {
				t.Run(map[bool]string{false: "calls-first", true: "source-first"}[sourceFirst], func(t *testing.T) {
					data, err := os.ReadFile(filepath.Join("testdata", tc.name))
					if err != nil {
						t.Fatal(err)
					}
					path := codexUsageLog(t, strings.Split(strings.TrimSpace(string(data)), "\n"))
					check := func(cs []Call) {
						t.Helper()
						var total Tokens
						for _, c := range cs {
							total.add(c.Tokens)
						}
						if len(cs) != tc.calls || total != tc.tokens {
							t.Fatalf("calls=%d tokens=%+v; want %d %+v", len(cs), total, tc.calls, tc.tokens)
						}
					}
					var first []Call
					source := func() []Call {
						t.Helper()
						for _, s := range CallSources() {
							if s.Path == path {
								return ReadCallSource(s)
							}
						}
						t.Fatal("source missing")
						return nil
					}
					if sourceFirst {
						first = source()
					} else {
						first = Calls(time.Time{})
					}
					check(first)
					check(source())
					check(Calls(time.Time{}))
					if list := List(0); len(list) != 1 || list[0].Tokens != tc.tokens {
						t.Fatalf("summary differs: %+v", list)
					}
					Reset()
					check(source())
					check(Calls(time.Time{}))
					// Summary JSON has no duplicated per-response index.
					var stateCopy state
					b, err := json.Marshal(cache[path])
					if err != nil {
						t.Fatal(err)
					}
					if err = json.Unmarshal(b, &stateCopy); err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(b), "Entries") || stateCopy.Codex != nil {
						t.Fatal("parser index persisted")
					}
				})
			}
		})
	}
}

func TestCodexColdAppendRebuildsTransientState(t *testing.T) {
	data, err := os.ReadFile("testdata/codex-native-paginated.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	// Restart between RECORD and token_count, when no parent is available.
	split := 0
	for i, line := range lines {
		if strings.Contains(line, `"type":"token_usage_record"`) {
			split = i + 1
			break
		}
	}
	if split == 0 {
		t.Fatal("fixture has no response record")
	}
	path := codexUsageLog(t, lines[:split])
	Calls(time.Time{})
	List(0)
	Reset()
	appendText(t, path, strings.Join(lines[split:], "\n")+"\n")
	got := Calls(time.Time{})
	List(0)
	if len(got) != 3 {
		t.Fatalf("cold append doubled calls: %d", len(got))
	}
	Reset()
	if again := Calls(time.Time{}); !reflect.DeepEqual(got, again) {
		t.Fatal("restart changed calls")
	}
}

func TestCodexRecordPreservesMetadataSession(t *testing.T) {
	lines := append(cxTurnLines(0, "t1", "gpt-6-astra", "high"), cxRecordLine(1, "a", cxUse(100, 0, 0, 10, 0), cxUse(100, 0, 0, 10, 0)))
	lines[0] = `{"timestamp":"` + stamp(0) + `","type":"session_meta","payload":{"id":"leaf-thread","session_id":"leaf-session"}}`
	codexUsageLog(t, lines)
	if cs := Calls(time.Time{}); len(cs) != 1 || cs[0].Session != "leaf-session" {
		t.Fatalf("record changed session grouping: %+v", cs)
	}
}
