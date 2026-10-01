package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDevinUsage: magpie's built-in Devin account showed no usage while the
// Devin plugin showed its day, week and ACU windows; both read
// GetUserStatus now, and a key Devin refuses says to sign in again. The
// CLI's own account had no plan either, where the plugin's said its tier.
func TestDevinUsage(t *testing.T) {
	home := claudeHome(t)
	data := filepath.Join(home, "data")
	t.Setenv("XDG_DATA_HOME", data)
	os.MkdirAll(filepath.Join(data, "devin"), 0o700)
	os.WriteFile(filepath.Join(data, "devin", "credentials.toml"), devinCredentials("devin-session-token$good", "", "", ""), 0o600)
	exe := filepath.Join(home, "devin")
	os.WriteFile(exe, []byte(`#!/bin/sh
printf 'Logged in (via Devin).\n\nUser:\n  Email:             dev@example.com\n\nAccount:\n  Tier:              Devin Pro\n'
`), 0o755)
	fakeDevin(t, exe)
	forgetDevinStatus()
	old := firstAsk
	firstAsk = time.Minute // a loaded machine's shell takes longer than a look's first wait
	t.Cleanup(func() { firstAsk = old })

	var asked map[string]any
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/exa.seat_management_pb.SeatManagementService/GetUserStatus" || r.Header.Get("Connect-Protocol-Version") != "1" {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &asked)
		if strings.Contains(string(b), "refused") {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"code":"unauthenticated","message":"bad key"}`)
			return
		}
		io.WriteString(w, `{"userStatus":{"planStatus":{"planInfo":{"planName":"Pro"},"planEnd":"2026-11-01T00:00:00Z",
			"dailyQuotaRemainingPercent":75,"dailyQuotaResetAtUnix":"1790000000",
			"weeklyQuotaRemainingPercent":"40","weeklyQuotaResetAtUnix":"1790500000",
			"acuLimit":"250","acuConsumed":12.345,"overageBalanceMicros":"5000000"}}}`)
	}))
	defer fake.Close()
	t.Setenv("WINDSURF_API_SERVER_URL", fake.URL)

	ls, ok := builtinLogins("devin")
	// the CLI's own account is named by its tier, as the plugin names it
	if !ok || len(ls) != 1 || ls[0].User != "dev@example.com" || ls[0].Plan != "Devin Pro" {
		t.Fatalf("logins %+v %v", ls, ok)
	}
	q := loginQuota(context.Background(), ls[0])
	if q.Error != "" {
		t.Fatal(q.Error)
	}
	if md, _ := asked["metadata"].(map[string]any); md["apiKey"] != "devin-session-token$good" || md["ideName"] != "devin-cli" {
		t.Fatalf("asked %v", asked)
	}
	if len(q.Windows) != 3 {
		t.Fatalf("windows %+v", q.Windows)
	}
	day, week, acu := q.Windows[0], q.Windows[1], q.Windows[2]
	if day.Name != "1 day" || day.Used != 25 || day.ResetsAt == nil || day.ResetsAt.Unix() != 1790000000 {
		t.Fatalf("day %+v", day)
	}
	if week.Name != "7 days" || week.Used != 60 || week.Span != 7*24*time.Hour {
		t.Fatalf("week %+v", week)
	}
	if acu.Name != "ACUs" || !acu.Aside || acu.Display != "12.35 / 250 ACUs" || acu.ResetsAt == nil {
		t.Fatalf("acu %+v", acu)
	}
	if q.Until == nil || q.Until.Format("2006-01-02") != "2026-11-01" || q.Balance != "$5.00" {
		t.Fatalf("plan %+v", q)
	}

	os.WriteFile(filepath.Join(data, "devin", "credentials.toml"), devinCredentials("devin-session-token$refused", "", "", ""), 0o600)
	if q := loginQuota(context.Background(), ls[0]); q.Error != "Devin's sign-in has expired — sign in again" {
		t.Fatalf("refused %+v", q)
	}
}
