package gateway

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A model Cursor serves only in Max Mode is asked for again in Max Mode
// when Cursor refuses it without (ARNO on Discord: "Cursor: Max Mode
// Required: The model "gpt-5.6-luna-low" requires Max Mode to be enabled"),
// as cursor-agent turns Max Mode on for such a model, and in Max Mode from
// then on; a model served without it goes as before.
func TestServeCursorMaxModeRequired(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cursorMaxOnly.Clear()
	t.Cleanup(cursorMaxOnly.Clear)
	var ran []string // each Run's model, its ModelDetails' max_mode and its RequestedModel's
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, err := readConnectFrame(bufio.NewReader(r.Body))
		if err != nil {
			http.Error(w, `{"code":"invalid_argument","message":"no run"}`, 400)
			return
		}
		var model string
		var details, requested uint64
		for _, rf := range pbFields(f.data) {
			if rf.num != 1 {
				continue
			}
			for _, x := range pbFields(rf.data) {
				switch x.num {
				case 3:
					model, details = pbStr(pbFields(x.data), 1), pbNum(pbFields(x.data), 7)
				case 9:
					requested = pbNum(pbFields(x.data), 2)
				}
			}
		}
		ran = append(ran, fmt.Sprintf("%s:%d:%d", model, details, requested))
		cursorDuplex(w)
		w.Header().Set("Content-Type", "application/connect+proto")
		if strings.HasPrefix(model, "gpt-5.6-luna") && details == 0 {
			w.Write(cursorEnd(`{"error":{"code":"failed_precondition","message":"Error","details":[{"debug":{"error":"ERROR_MAX_MODE_REQUIRED","details":{"title":"Max Mode Required","detail":"The model \"` + model + `\" requires Max Mode to be enabled. Please enable Max Mode and try again."}}}]}}`))
		} else {
			w.Write(cursorUpdate(1, pb{}.str(1, "Hello")))
			w.Write(cursorUpdate(14, pb{}.varint(1, 3).varint(2, 1)))
		}
		w.(http.Flusher).Flush()
	}))
	defer up.Close()
	tok, ver, agent, api := cursorToken, cursorVersion, cursorAgent, cursorAPI
	cursorToken = func() (string, error) { return "tok", nil }
	cursorVersion = func() string { return "cli-test" }
	cursorAgent, cursorAPI = up.URL, up.URL
	defer func() { cursorToken, cursorVersion, cursorAgent, cursorAPI = tok, ver, agent, api }()
	forgetCursorEndpoint(t)

	serve := func(model string) *httptest.ResponseRecorder {
		ran = nil
		body := `{"model":"x","messages":[{"role":"user","content":"hi"}]}`
		w := httptest.NewRecorder()
		var u Usage
		New().serveCursor(w, httptest.NewRequest("POST", "/", strings.NewReader(body)), provider.Chat, model, []byte(body), &u)
		return w
	}
	if w := serve("gpt-5.6-luna-low"); w.Code != 200 || !strings.Contains(w.Body.String(), "Hello") ||
		strings.Join(ran, ",") != "gpt-5.6-luna-low:0:0,gpt-5.6-luna-low:1:1" {
		t.Fatalf("%d %s %v", w.Code, w.Body, ran)
	}
	// known now: in Max Mode at once
	if w := serve("gpt-5.6-luna-low"); w.Code != 200 || strings.Join(ran, ",") != "gpt-5.6-luna-low:1:1" {
		t.Fatalf("%d %s %v", w.Code, w.Body, ran)
	}
	// a model served without it: one Run, no Max Mode
	if w := serve("composer-2.5"); w.Code != 200 || strings.Join(ran, ",") != "composer-2.5:0:0" {
		t.Fatalf("%d %s %v", w.Code, w.Body, ran)
	}
	if !cursorMaxRequired(`Max Mode Required: The model "gpt-5.6-luna-low" requires Max Mode to be enabled. Please enable M`) || cursorMaxRequired("usage limit reached") {
		t.Fatal("cursorMaxRequired")
	}
}
