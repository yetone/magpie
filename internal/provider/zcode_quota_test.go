package provider

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

// TestZCodeNoMonthlyCap: an older GLM Coding plan's reply (#366) — the
// five hours untouched, no week, and the month's MCP tool calls with no
// cap given as 100% used. The account is not out: only the five hours
// count, and the uncapped month reads as nothing used.
func TestZCodeNoMonthlyCap(t *testing.T) {
	now := time.Now()
	reset := now.Add(3 * time.Hour).UnixMilli()
	month := now.Add(20 * 24 * time.Hour).UnixMilli()
	var d zhipuLimits
	if err := json.Unmarshal([]byte(`{"level":"pro","limits":[
		{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":0,"nextResetTime":`+strconv.FormatInt(reset, 10)+`},
		{"type":"TIME_LIMIT","unit":5,"number":1,"usage":0,"currentValue":0,"remaining":0,"percentage":100,"nextResetTime":`+strconv.FormatInt(month, 10)+`}]}`), &d); err != nil {
		t.Fatal(err)
	}
	ws := d.windows()
	if len(ws) != 2 || ws[0].Name != "5 hours" || ws[0].Aside || ws[1].Name != "MCP · Month" || !ws[1].Aside || ws[1].Used != 0 {
		t.Fatalf("windows: %+v", ws)
	}
	a := allowanceOf(ws, now)
	if used, _ := a.For("glm-5.1", now); used != 0 {
		t.Fatalf("used %v, want 0", used)
	}
	if full := a.Full("glm-5.1", 100, now); !full.IsZero() {
		t.Fatalf("full until %v", full)
	}

	// the MCP calls used up on a plan that caps them don't stop the models
	// either, and still show as used
	d = zhipuLimits{}
	json.Unmarshal([]byte(`{"limits":[
		{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":10,"nextResetTime":`+strconv.FormatInt(reset, 10)+`},
		{"type":"TOKENS_LIMIT","unit":6,"number":1,"percentage":30,"nextResetTime":`+strconv.FormatInt(month, 10)+`},
		{"type":"TIME_LIMIT","unit":5,"number":1,"usage":100,"currentValue":100,"remaining":0,"percentage":100,"nextResetTime":`+strconv.FormatInt(month, 10)+`}]}`), &d)
	ws = d.windows()
	if len(ws) != 3 || ws[2].Used != 100 || !ws[2].Aside || ws[1].Aside {
		t.Fatalf("capped: %+v", ws)
	}
	a = allowanceOf(ws, now)
	if used, _ := a.For("glm-5.1", now); used != 30 {
		t.Fatalf("capped used %v, want 30", used)
	}
	if full := a.Full("glm-5.1", 100, now); !full.IsZero() {
		t.Fatalf("capped full until %v", full)
	}
}

// A window whose whole is told carries the count itself, as amount of limit
// (#659): the card says it as used or as left, as it says the share beside
// it — the remaining branch and the currentValue branch alike.
func TestZCodeWindowsCarryTheirCount(t *testing.T) {
	var d zhipuLimits
	if err := json.Unmarshal([]byte(`{"limits":[
		{"type":"CREDIT_LIMIT","unit":3,"number":5,"usage":2000,"remaining":1500,"percentage":25},
		{"type":"CREDIT_LIMIT","unit":6,"number":1,"usage":8000,"currentValue":2000}
	]}`), &d); err != nil {
		t.Fatal(err)
	}
	ws := d.windows()
	if len(ws) != 2 {
		t.Fatalf("windows: %+v", ws)
	}
	if w := ws[0]; w.Amount != 500 || w.Limit != 2000 ||
		w.Count(false) != groupedNumber(500)+" / "+groupedNumber(2000) ||
		w.Count(true) != groupedNumber(1500)+" / "+groupedNumber(2000) {
		t.Fatalf("remaining: %+v", w)
	}
	if w := ws[1]; w.Amount != 2000 || w.Limit != 8000 || w.Used != 25 ||
		w.Count(false) != groupedNumber(2000)+" / "+groupedNumber(8000) ||
		w.Count(true) != groupedNumber(6000)+" / "+groupedNumber(8000) {
		t.Fatalf("currentValue: %+v", w)
	}
}
