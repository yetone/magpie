package library

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/mcpauth"
	"github.com/yetone/magpie/internal/mcpauth/mcpauthtest"
	"github.com/yetone/magpie/internal/settings"
)

func TestSaveServerRenameKeepsOAuth(t *testing.T) {
	for _, tc := range []struct {
		name      string
		saveAlpha bool
		newName   string
		wantError string
	}{
		{
			name:      "name_conflict",
			saveAlpha: true,
			newName:   "beta",
			wantError: "the library already has a server called beta",
		},
		{
			name:      "missing_old",
			newName:   "gamma",
			wantError: "no server called alpha",
		},
		{
			name:      "unused_name",
			saveAlpha: true,
			newName:   "gamma",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sandbox(t)
			alphaFake, betaFake := mcpauthtest.New(t), mcpauthtest.New(t)
			agents := []string{"claude", "codex", "cursor"}
			alpha := Server{Name: "alpha", Transport: "http", URL: alphaFake.URL, Agents: agents}
			beta := Server{Name: "beta", Transport: "http", URL: betaFake.URL, Agents: agents}
			if tc.saveAlpha {
				ok(t)(SaveServer("", alpha))
			}
			ok(t)(SaveServer("", beta))
			// In missing_old, alpha has a sign-in but no library entry.
			alphaFake.SignIn(t, "alpha")
			betaFake.SignIn(t, "beta")
			// Independent fake servers start with the same token counter.
			// A second sign-in gives beta distinct access and refresh tokens.
			betaFake.SignIn(t, "beta")

			before := map[string]mcpauth.Record{}
			for _, s := range []Server{alpha, beta} {
				r, found := mcpauth.Get(s.Name)
				if !found || r.URL != s.URL || r.Access == "" || r.Refresh == "" {
					t.Fatalf("sign-in fixture for %s: found=%v record=%+v", s.Name, found, r)
				}
				before[s.Name] = r
			}
			if before["alpha"].Access == before["beta"].Access || before["alpha"].Refresh == before["beta"].Refresh {
				t.Fatal("sign-in fixtures must have distinct tokens")
			}
			assertRecord := func(name string, want mcpauth.Record) {
				t.Helper()
				if got, found := mcpauth.Get(name); !found || !reflect.DeepEqual(got, want) {
					t.Errorf("OAuth record for %s changed: found=%v got=%+v want=%+v", name, found, got, want)
				}
				if token, err := mcpauth.Token(t.Context(), name); err != nil || token != want.Access {
					t.Errorf("OAuth token for %s: got=%q error=%v want=%q", name, token, err, want.Access)
				}
				if !mcpauth.SignedIn(name, want.URL) {
					t.Errorf("OAuth sign-in for %s no longer matches URL %s", name, want.URL)
				}
			}
			assertRecord("alpha", before["alpha"])
			assertRecord("beta", before["beta"])
			if t.Failed() {
				t.Fatal("invalid sign-in fixture")
			}

			files := []string{path(), filepath.Join(settings.Dir(), "mcp-signins.json")}
			var targets []*Target
			for _, id := range agents {
				target := targetByID(id)
				if target == nil || target.MCP == nil {
					t.Fatalf("sandbox has no MCP target for %s", id)
				}
				targets = append(targets, target)
				files = append(files, target.MCP.files()...)
			}
			contents := map[string][]byte{}
			for _, p := range files {
				b, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				contents[p] = b
			}

			renamed := alpha
			renamed.Name = tc.newName
			res, err := SaveServer("alpha", renamed)
			if tc.wantError != "" {
				if err == nil || err.Error() != tc.wantError || res != nil {
					t.Fatalf("SaveServer rejected rename: result=%+v error=%v want=%q", res, err, tc.wantError)
				}
				t.Logf("SaveServer returned the expected rejection: %v", err)
				assertRecord("alpha", before["alpha"])
				assertRecord("beta", before["beta"])
				if tc.newName != "beta" {
					if _, found := mcpauth.Get(tc.newName); found {
						t.Errorf("rejected rename created an OAuth record for %s", tc.newName)
					}
				}
				for _, p := range files {
					b, err := os.ReadFile(p)
					if err != nil {
						t.Error(err)
						continue
					}
					if !bytes.Equal(b, contents[p]) {
						t.Errorf("file changed after rejected rename: %s", p)
					}
				}
				return
			}

			ok(t)(res, err)
			if _, found := mcpauth.Get("alpha"); found {
				t.Error("successful rename kept the old OAuth record")
			}
			assertRecord(tc.newName, before["alpha"])
			assertRecord("beta", before["beta"])
			l, err := load()
			if err != nil {
				t.Fatal(err)
			}
			if s := l.server(tc.newName); l.server("alpha") != nil || s == nil || !s.same(&renamed) {
				t.Errorf("successful rename did not replace the library entry: %+v", l.MCP)
			}
			for _, target := range targets {
				given, err := target.MCP.read()
				if err != nil {
					t.Fatal(err)
				}
				if given["alpha"] != nil {
					t.Errorf("%s kept the old MCP entry after rename", target.Agent.ID)
				}
				for _, name := range []string{tc.newName, "beta"} {
					if s := given[name]; s == nil || s.Transport != "http" || s.URL != gateway.URL()+"/mcp/"+name {
						t.Errorf("%s MCP entry %s did not use its gateway URL: %+v", target.Agent.ID, name, s)
					}
				}
			}
		})
	}
}
