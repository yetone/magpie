package sessions

import (
	"bytes"
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
)

// Kimi Code's calls (#1607, infinitr0us: its direct calls to api.kimi.com
// never reached Usage). Read from the wire files the reporter shared from
// two machines (protocols 1.0, 1.4 and 1.5, redacted, every number kept)
// and Kimi Code's own source as they quoted it. Each session is a folder
// sessions/<workdir slug>/<session id>/ of Kimi's home, each of its agents
// (main, and agent-N for a subagent, whose calls are written only in its
// own file) writing agents/<agent>/wire.jsonl. Every model response that
// came back with usage writes one usage.record:
//
//	{"type":"usage.record","agentId":"main","model":"kimi-code/k3",
//	 "usage":{"inputOther":20257,"output":230,"inputCacheRead":0,
//	 "inputCacheCreation":0},"usageScope":"turn","time":1791696952640}
//
// model is the key of the [models."<key>"] table the call used. Input is
// inputOther plus the cache's read and creation, kept apart here as the
// ledger keeps them; output includes the thinking. usageScope "session" is
// a call outside a turn (compaction), counted all the same. A failed or
// interrupted call writes none, only a step.end with usage null.
//
// What changed between versions, and why nothing here pairs a record with
// its step.end: 1.4 writes the step.end first and the usage.record after
// it, 1.5 the other way round; the June 1.4 build writes no messageId and no
// llm.request; 1.4 and the 1.5 builds before late August write no agentId.
// The record alone carries everything a call needs.
//
// A call Kimi made through magpie's own entry (internal/agent's kimi.go
// writes it as the provider "magpie", its model tables keyed
// "magpie/<provider>/<model>") is the gateway's to count: it logged the
// call itself, with the provider and account that answered.
//
// kimi fork copies the parent's records into the new session unchanged,
// times included, so a call is keyed by its agent, time, model and usage
// (kimiCallKey), and Calls counts each key once. Among the reporter's
// 5,190 records no two calls share one.

// KimiCodeDir is Kimi Code's folder: $KIMI_CODE_HOME, else ~/.kimi-code, as
// internal/agent's KimiDir finds the new Kimi Code's (which this package
// cannot import). The old Python kimi-cli's ~/.kimi has no usage records.
func KimiCodeDir() string {
	if d := strings.TrimSpace(appdir.Getenv("KIMI_CODE_HOME")); d != "" {
		return expandHome(d)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".kimi-code")
}

func kimiSessions() string { return filepath.Join(KimiCodeDir(), "sessions") }

// kimiGateway is how the model of a call to magpie's entry begins.
const kimiGateway = "magpie/"

// kimiCallFiles are every agent's wire.jsonl of every session. A session
// migrated from the Python kimi-cli (a ses_* folder) is read too: one Kimi
// Code opened again writes its own records there. The Kimi desktop app's
// Work sessions are Kimi Code's too (kimi_work.go).
func kimiCallFiles() []file {
	return append(kimiFilesIn(kimiSessions()), kimiWorkCallFiles()...)
}

type kimiUsage struct {
	InputOther         int `json:"inputOther"`
	Output             int `json:"output"`
	InputCacheRead     int `json:"inputCacheRead"`
	InputCacheCreation int `json:"inputCacheCreation"`
}

type kimiRecord struct {
	Type    string     `json:"type"`
	AgentID string     `json:"agentId"`
	Model   string     `json:"model"`
	Usage   *kimiUsage `json:"usage"`
	Time    int64      `json:"time"`
}

var kimiUsageRecord = []byte(`"usage.record"`)

// kimiCallsBegin starts an agent's wire file read from its start. Kimi Code
// only appends to it, each record a line of its own, so readCalls reads one
// that grew on from where it left off (lineCallReaders).
func kimiCallsBegin(st *callFile, f file) {
	st.Session = strings.TrimPrefix(f.key, "kimi:")
}

// kimiCallLine reads one line of an agent's wire file: a usage.record is a
// call.
func kimiCallLine(st *callFile, b []byte) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || len(b) > maxLine || !bytes.Contains(b, kimiUsageRecord) {
		return
	}
	var rec kimiRecord
	if json.Unmarshal(b, &rec) != nil {
		return
	}
	if c, ok := kimiCall(st, filepath.Base(filepath.Dir(st.Path)), rec); ok {
		st.Calls = append(st.Calls, c)
	}
}

// kimiCall is a usage.record as a call.
func kimiCall(st *callFile, agentDir string, rec kimiRecord) (Call, bool) {
	u := rec.Usage
	if rec.Type != "usage.record" || u == nil || strings.HasPrefix(rec.Model, kimiGateway) {
		return Call{}, false
	}
	if u.InputOther < 0 || u.Output < 0 || u.InputCacheRead < 0 || u.InputCacheCreation < 0 {
		return Call{}, false
	}
	at := ms(rec.Time)
	if at.IsZero() {
		return Call{}, false
	}
	model := rec.Model
	if root := kimiWorkRoot(st.Path); root != "" {
		if m, ok := kimiWorkModels(root)[model]; ok {
			if m.gateway {
				return Call{}, false
			}
			model = cmp.Or(m.model, model)
		}
	}
	c := Call{
		Agent:   "kimi",
		Time:    at,
		Session: st.Session,
		Model:   st.str(model),
		Tokens:  Tokens{Input: u.InputOther, Output: u.Output, CacheRead: u.InputCacheRead, CacheWrite: u.InputCacheCreation},
	}
	if c.Tokens.zero() {
		return Call{}, false
	}
	agent := cmp.Or(rec.AgentID, agentDir)
	c.Msg = st.str(kimiCallKey(agent, rec.Time, rec.Model, *u))
	return c, true
}

// kimiCallKey is what tells one call from another across sessions: a fork
// repeats its parent's records byte for byte.
func kimiCallKey(agent string, t int64, model string, u kimiUsage) string {
	return "kimi:" + agent + ":" + strconv.FormatInt(t, 10) + ":" + model + ":" +
		strconv.Itoa(u.InputOther) + "/" + strconv.Itoa(u.Output) + "/" +
		strconv.Itoa(u.InputCacheRead) + "/" + strconv.Itoa(u.InputCacheCreation)
}
