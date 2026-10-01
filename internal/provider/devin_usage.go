package provider

// How much of a Devin plan an account has used, as GetUserStatus (what the
// CLI's /usage reads) tells it: the plan's end, the daily and weekly quotas
// a quota-billed plan has (the share left of each, and when it comes back),
// the ACUs a plan with a limit has used this cycle, and the extra usage
// balance. The Devin plugin reads the same.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var devinUsageClient = &http.Client{Timeout: 20 * time.Second}

// devinUsageVersion is the CLI version GetUserStatus is asked as, the one
// the gateway's chats name.
const devinUsageVersion = "3000.11.3"

// devinNum reads a number JSON gives as a number or a string (int64s are
// strings in proto JSON); ok is false when it is left out.
func devinNum(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		return f, err == nil
	}
	var f float64
	return f, json.Unmarshal(raw, &f) == nil
}

type devinPlanStatus struct {
	PlanInfo struct {
		HideDaily  bool `json:"hideDailyQuota"`
		HideWeekly bool `json:"hideWeeklyQuota"`
	} `json:"planInfo"`
	PlanEnd     string          `json:"planEnd"`
	DailyLeft   json.RawMessage `json:"dailyQuotaRemainingPercent"`
	DailyReset  json.RawMessage `json:"dailyQuotaResetAtUnix"`
	WeeklyLeft  json.RawMessage `json:"weeklyQuotaRemainingPercent"`
	WeeklyReset json.RawMessage `json:"weeklyQuotaResetAtUnix"`
	ACULimit    json.RawMessage `json:"acuLimit"`
	ACUUsed     json.RawMessage `json:"acuConsumed"`
	Overage     json.RawMessage `json:"overageBalanceMicros"`
}

// devinQuota fills q from a plan's status. A quota Devin leaves out is not
// taken for used up.
func devinQuota(q *SubscriptionQuota, st devinPlanStatus) {
	var end *time.Time
	if t, err := time.Parse(time.RFC3339Nano, st.PlanEnd); err == nil {
		end = &t
		q.Until = &t
	}
	for _, w := range []struct {
		name        string
		span        time.Duration
		left, reset json.RawMessage
		hide        bool
	}{
		{"1 day", 24 * time.Hour, st.DailyLeft, st.DailyReset, st.PlanInfo.HideDaily},
		{"7 days", 7 * 24 * time.Hour, st.WeeklyLeft, st.WeeklyReset, st.PlanInfo.HideWeekly},
	} {
		left, ok := devinNum(w.left)
		if w.hide || !ok {
			continue
		}
		x := QuotaWindow{Name: w.name, Used: min(100, max(0, 100-left)), Span: w.span}
		if at, _ := devinNum(w.reset); at > 0 {
			t := time.Unix(int64(at), 0).UTC()
			x.ResetsAt = &t
		}
		q.Windows = append(q.Windows, x)
	}
	if limit, _ := devinNum(st.ACULimit); limit > 0 {
		used, _ := devinNum(st.ACUUsed)
		q.Windows = append(q.Windows, QuotaWindow{Name: "ACUs", Used: min(100, 100*used/limit),
			Display: devinACUs(used) + " / " + devinACUs(limit) + " ACUs", ResetsAt: end, Aside: true})
	}
	if extra, _ := devinNum(st.Overage); extra > 0 {
		q.Balance = fmt.Sprintf("$%.2f", extra/1e6)
	}
}

// devinACUs writes an ACU count as the plugin does: to two decimals, none
// trailing.
func devinACUs(f float64) string {
	return strconv.FormatFloat(math.Round(f*100)/100, 'f', -1, 64)
}

func devinLoginQuota(ctx context.Context, l Login) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "devin", Name: "Devin", Icon: "devin", Plan: l.Plan, User: l.User, Windows: []QuotaWindow{}}
	home, found := "", false
	for _, d := range devinLogins() {
		if d.User == l.User {
			home, found = d.Home, true
			break
		}
	}
	if !found {
		q.Error = "no such Devin account"
		return q
	}
	key, server, err := DevinAuthAt(home)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	st, err := devinUserStatus(ctx, key, server)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	devinQuota(&q, st)
	return q
}

var errDevinUsageSignIn = errors.New("Devin's sign-in has expired — sign in again")

func devinUserStatus(ctx context.Context, key, server string) (devinPlanStatus, error) {
	body, _ := json.Marshal(map[string]any{"metadata": map[string]any{
		"ideName": "devin-cli", "ideVersion": devinUsageVersion, "extensionName": "devin-cli",
		"extensionVersion": devinUsageVersion, "apiKey": key, "locale": "en", "os": runtime.GOOS,
	}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server+"/exa.seat_management_pb.SeatManagementService/GetUserStatus", bytes.NewReader(body))
	if err != nil {
		return devinPlanStatus{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	res, err := devinUsageClient.Do(req)
	if err != nil {
		return devinPlanStatus{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	var env struct {
		Code       string `json:"code"`
		Message    string `json:"message"`
		UserStatus struct {
			PlanStatus devinPlanStatus `json:"planStatus"`
		} `json:"userStatus"`
	}
	_ = json.Unmarshal(b, &env)
	if res.StatusCode == http.StatusUnauthorized || env.Code == "unauthenticated" {
		return devinPlanStatus{}, errDevinUsageSignIn
	}
	if res.StatusCode != http.StatusOK {
		return devinPlanStatus{}, fmt.Errorf("Devin: %s", APIError(b, res.Status))
	}
	return env.UserStatus.PlanStatus, nil
}
