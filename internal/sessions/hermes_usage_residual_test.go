package sessions

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func hermesTaskUsageFixture(t *testing.T, sid string, main *Tokens) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "hermes")
	t.Setenv("HERMES_HOME", root)
	path := hermesFixture(t, root, "", sid, true)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE session_model_usage ADD COLUMN task TEXT NOT NULL DEFAULT ''`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE sessions ADD COLUMN billing_provider TEXT`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sessions SET billing_provider='session-provider' WHERE id=?`, sid); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE session_model_usage SET task='vision' WHERE session_id=?`, sid); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if main != nil {
		_, err = db.Exec(`INSERT INTO session_model_usage
			(session_id, model, billing_provider, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, api_call_count, last_seen, task)
			VALUES (?, 'main-model', 'main-provider', ?, ?, ?, ?, 2, 1, ?, '')`,
			sid, main.Input, main.Output, main.CacheRead, main.CacheWrite, float64(time.Date(2026, 9, 28, 10, 0, 12, 0, time.UTC).Unix()))
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHermesUsageAddsMainResidualWhenOnlyAuxiliaryRowsExist(t *testing.T) {
	setup(t)
	path := hermesTaskUsageFixture(t, "aux-only", nil)
	s := hermesSessionByID(t, hermesID(path, "aux-only"))
	if got := model(s, "fallback-model").Tokens; got != (Tokens{90, 30, 40, 10}) {
		t.Fatalf("main-loop residual = %+v, want sessions aggregate", got)
	}
	if got := model(s, "actual-model").Tokens; got != (Tokens{100, 50, 25, 15}) {
		t.Fatalf("auxiliary usage = %+v", got)
	}
	// Hermes does not emit Calls; verify no leakage.
	for _, c := range Calls(time.Time{}) {
		if c.Agent == "hermes" {
			t.Fatalf("unexpected Hermes call: %+v", c)
		}
	}
}

func TestHermesUsageResidualDoesNotDuplicateCoveredOrOvercoveredMainRows(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows []Tokens
	}{
		{
			name: "multiple main routes cover session total",
			rows: []Tokens{{Input: 50, Output: 10, CacheRead: 20, CacheWrite: 5}, {Input: 40, Output: 20, CacheRead: 20, CacheWrite: 5}},
		},
		{
			name: "main routes exceed session total",
			rows: []Tokens{{Input: 70, Output: 20, CacheRead: 30, CacheWrite: 8}, {Input: 40, Output: 20, CacheRead: 20, CacheWrite: 5}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setup(t)
			path := hermesTaskUsageFixture(t, "covered", nil)
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			for i, row := range tc.rows {
				_, err = db.Exec(`INSERT INTO session_model_usage
					(session_id, model, billing_provider, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, api_call_count, last_seen, task)
					VALUES (?, ?, ?, ?, ?, ?, ?, 0, 1, ?, '')`,
					"covered", "main-route-"+itoa(i), "provider-"+itoa(i), row.Input, row.Output, row.CacheRead, row.CacheWrite,
					float64(time.Date(2026, 9, 28, 10, 0, 12, 0, time.UTC).Unix()))
				if err != nil {
					db.Close()
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			s := hermesSessionByID(t, hermesID(path, "covered"))
			if got := model(s, "fallback-model").Tokens; !got.zero() {
				t.Fatalf("unexpected duplicate main residual: %+v", got)
			}
		})
	}
}

func TestHermesUsageResidualSubtractsMainAttributionOnly(t *testing.T) {
	setup(t)
	path := hermesTaskUsageFixture(t, "mixed", &Tokens{Input: 60, Output: 20, CacheRead: 10, CacheWrite: 5})
	s := hermesSessionByID(t, hermesID(path, "mixed"))
	if got := model(s, "main-model").Tokens; got != (Tokens{60, 20, 10, 5}) {
		t.Fatalf("main attribution = %+v", got)
	}
	if got := model(s, "fallback-model").Tokens; got != (Tokens{30, 10, 30, 5}) {
		t.Fatalf("main residual = %+v, want remaining sessions aggregate", got)
	}
	if got := model(s, "actual-model").Tokens; got != (Tokens{100, 50, 25, 15}) {
		t.Fatalf("auxiliary usage changed = %+v", got)
	}
}
