package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// zhipuResetList is customer-package-reset/list?targetType=PERSONAL as the
// reporter of #1191 captured it on bigmodel.cn, the account and record ids
// and times redacted there and filled in here with the reply's own shape:
// numeric ids, times as "YYYY-MM-DD HH:mm:ss".
const zhipuResetList = `{
  "code": 200,
  "msg": "操作成功",
  "data": {
    "customerId": 0,
    "targetType": "PERSONAL",
    "organizationId": null,
    "projectId": null,
    "lastFiveHourResetTime": "2026-10-06 21:14:03",
    "lastWeekResetTime": null,
    "fiveHourResets": [
      {
        "recordId": 0,
        "grantType": "DIRECT",
        "expireTime": "2026-11-02 23:59:59",
        "available": true
      }
    ],
    "weekResets": [
      {
        "recordId": 0,
        "grantType": "DIRECT",
        "expireTime": "2026-10-30 23:59:59",
        "available": true
      },
      {
        "recordId": 0,
        "grantType": "DIRECT",
        "expireTime": "2026-10-04 23:59:59",
        "available": false
      }
    ]
  },
  "success": true
}`

// #1191: a GLM Coding Plan's resets are counted from the list the vendor's
// usage page reads, only those still available (one run out stays listed),
// and a list that can't be read shows no count, never 0.
func TestZhipuPersonalResets(t *testing.T) {
	body := ""
	var asked *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r
		w.Write([]byte(body))
	}))
	defer srv.Close()
	read := func(b string) *ResetCredits {
		body = b
		return zhipuPersonalResets(context.Background(), srv.URL, "glm-key")
	}

	r := read(zhipuResetList)
	if asked.URL.Path != "/api/biz/customer-package-reset/list" || asked.URL.Query().Get("targetType") != "PERSONAL" ||
		asked.Method != http.MethodGet || asked.Header.Get("Authorization") != "glm-key" {
		t.Fatalf("asked %s %s with %q", asked.Method, asked.URL, asked.Header.Get("Authorization"))
	}
	if r == nil || r.Count != 2 || r.FiveHour != 1 || r.Weekly != 1 || !r.ByWindow || r.Team {
		t.Fatalf("resets: %+v", r)
	}
	cst := time.FixedZone("CST", 8*3600)
	if want := time.Date(2026, 10, 30, 23, 59, 59, 0, cst); r.Until == nil || !r.Until.Equal(want) {
		t.Fatalf("until %v, want %v", r.Until, want)
	}
	if len(r.Each) != 2 || r.Each[0].Window != "weekly" || r.Each[1].Window != "fiveHour" {
		t.Fatalf("each: %+v", r.Each)
	}
	if w := r.Words(); w != "1 five-hour reset · 1 weekly reset" {
		t.Fatalf("words: %q", w)
	}

	for name, b := range map[string]string{
		// api.z.ai's and bigmodel.cn's answer to a key they don't take
		"refused":  `{"code":1000,"msg":"身份验证失败。","success":false}`,
		"not json": `<html>502 Bad Gateway</html>`,
		"cut off":  zhipuResetList[:200],
		"none left": `{"code":200,"msg":"操作成功","data":{"targetType":"PERSONAL","fiveHourResets":[],"weekResets":[
			{"recordId":0,"grantType":"DIRECT","expireTime":"2026-10-04 23:59:59","available":false}]},"success":true}`,
		"no data": `{"code":200,"msg":"操作成功","data":null,"success":true}`,
	} {
		if r := read(b); r != nil {
			t.Errorf("%s: %+v, want no count", name, r)
		}
	}
}
