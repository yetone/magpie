package gateway

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// This explicit check uses the signed-in account for one native Codex turn.
func TestLiveCodexSealedAgentGuidance(t *testing.T) {
	bin, home, codexHome := os.Getenv("MAGPIE_LIVE_CODEX_BIN"), os.Getenv("MAGPIE_LIVE_HOME"), os.Getenv("MAGPIE_LIVE_CODEX_HOME")
	if bin == "" || home == "" || codexHome == "" {
		t.Skip("live Codex environment not set")
	}
	var upstreamCalls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		http.Error(w, "unexpected child upstream request", 500)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "fake", Models: []string{"m1"}, Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}

	s := New()
	handler := s.Handler()
	var mu sync.Mutex
	var sawCiphertext, sawGuidance, leaked bool
	var childStatus int
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isChild := false
		if r.Method == http.MethodPost && r.URL.Path == CodexPath+"/responses" {
			body, ok := s.readRequestBody(w, r, provider.Responses, codexReader, 0)
			if !ok {
				return
			}
			isChild = bytes.Contains(body, []byte(`"model":"fake/m1"`))
			if isChild && bytes.Contains(body, []byte("gAAAAA")) {
				mu.Lock()
				sawCiphertext = true
				mu.Unlock()
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		if !isChild {
			handler.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		mu.Lock()
		childStatus = rec.Code
		sawGuidance = sawGuidance || strings.Contains(rec.Body.String(), "Magpie-served model for the lead")
		leaked = leaked || bytes.Contains(rec.Body.Bytes(), []byte("gAAAAA"))
		mu.Unlock()
		for key, values := range rec.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(rec.Code)
		w.Write(rec.Body.Bytes())
	}))
	defer gateway.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "exec", "--ignore-user-config", "--ephemeral", "--sandbox", "read-only", "--skip-git-repo-check",
		"-C", t.TempDir(), "-m", "gpt-6-sol", "-c", fmt.Sprintf("openai_base_url=%q", gateway.URL+CodexPath),
		"-c", "features.multi_agent_v2=true", "Spawn one worker subagent on model fake/m1 with fork_turns=none. Tell it to reply PONG-LIVE-AGENT. Wait for it. If the tool fails, report its exact error message. Do not use other tools.")
	cmd.Env = append(os.Environ(), "HOME="+home, "CODEX_HOME="+codexHome)
	output, err := cmd.CombinedOutput()
	mu.Lock()
	gotCiphertext, gotGuidance, gotLeak, status := sawCiphertext, sawGuidance, leaked, childStatus
	mu.Unlock()
	cliGuidance := strings.Contains(string(output), "Magpie-served model for the lead")
	if !gotCiphertext || !gotGuidance || gotLeak || status != 400 || upstreamCalls.Load() != 0 || !cliGuidance || err != nil {
		t.Errorf("live guidance: sealed=%v status=%d errorShown=%v leaked=%v upstreamCalls=%d cliShowsGuidance=%v cliError=%v", gotCiphertext, status, gotGuidance, gotLeak, upstreamCalls.Load(), cliGuidance, err)
	}
}
