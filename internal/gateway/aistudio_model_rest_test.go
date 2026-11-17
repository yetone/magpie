package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// AIStudio2API (the backend of @dorokuma/magpie-gemini-web's ai-studio)
// answers a model whose accounts all cool down with its AllCoolingError
// (internal/aistudio/accounts.go) in an OpenAI error object
// (internal/api/errors.go), the cooldown's kind and Google's own words
// after the "；" as docs/protocol.md shows them. AI Studio limits each
// model's day on its own: the refusal is gemini-4-hydrogen's, and argon
// on the same account still answers (WangYeruo on X).
const aiStudioModelDay = `{"error":{"message":"模型 gemini-4-hydrogen 的可用账户均在冷却，最早恢复时间 2026-11-18T16:00:00+08:00；每日限额: AI Studio GenerateContent 返回 HTTP 429、协议错误码 8: … limit: 100, model: gemini-4-hydrogen … Please try again in 3600s.","type":"requests","param":null,"code":"rate_limit_exceeded"}}`

func TestAIStudioModelDayRestsTheModel(t *testing.T) {
	restingUntil.Lock()
	restingUntil.m, restingUntil.note = map[string]time.Time{}, map[string]Rest{}
	restingUntil.Unlock()
	s := &Server{}
	c := candidate{p: provider.Provider{ID: "ai-studio", Key: "k-one"}, model: "gemini-4-hydrogen", rest: "ai-studio"}
	r := s.restAfter(c, 429, http.Header{"Retry-After": {"3600"}}, []byte(aiStudioModelDay))
	if r.Key != c.restID() || r.Model != "gemini-4-hydrogen" {
		t.Fatalf("rests %q (model %q); want the model %q", r.Key, r.Model, c.restID())
	}
}
