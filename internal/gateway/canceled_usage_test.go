package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// 取消发生在路由切换后，账本仍应把最后尝试的供应商与模型一起记录。
func TestCanceledRouteUsageKeepsAttemptedModel(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		name := "fallback"
		if grouped {
			name = "group"
		}
		t.Run(name, func(t *testing.T) {
			fresh(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				io.WriteString(w, `{"error":{"message":"rate limited"}}`)
			}))
			defer first.Close()
			sent := make(chan string, 1)
			second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model string `json:"model"`
				}
				json.NewDecoder(r.Body).Decode(&body)
				sent <- body.Model
				cancel()
				<-r.Context().Done()
			}))
			defer second.Close()
			primary := provider.Provider{ID: "primary", Name: "Primary", Key: "test", Chat: first.URL + "/v1", Models: []string{"first"}}
			if !grouped {
				primary.Fallback = []string{"spare/second"}
			}
			for _, p := range []provider.Provider{primary, {ID: "spare", Name: "Spare", Key: "test", Chat: second.URL + "/v1", Models: []string{"second"}}} {
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
			}
			requested := "primary/first"
			if grouped {
				if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"primary/first", "spare/second"}, Routing: provider.Ordered}); err != nil {
					t.Fatal(err)
				}
				requested = "group/g"
			}
			s := New()
			body := `{"model":"` + requested + `","messages":[{"role":"user","content":"hi"}]}`
			s.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)).WithContext(ctx))
			select {
			case model := <-sent:
				if model != "second" {
					t.Fatalf("upstream was sent model %q", model)
				}
			default:
				t.Fatal("the fallback was not attempted")
			}
			records := usage.Load(time.Time{})
			if len(records) != 1 {
				t.Fatalf("ledger has %d records, want one canceled request", len(records))
			}
			r := records[0]
			if r.Provider != "spare" || r.Model != "second" || r.Requested != requested || r.Status != 499 || r.Served != "" || r.Input+r.Output != 0 {
				t.Fatalf("canceled ledger entry: %+v", r)
			}
		})
	}
}
