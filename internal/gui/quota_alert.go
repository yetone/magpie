package gui

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// onAlerts is told when the Settings page turns a usage or balance alert
// on, for the process that notifies to ask the system's leave to and look
// at once; set by the process that has the tray.
var onAlerts func()

// notifyProblem says why magpie's notifications can't be shown, "" when
// they can or it isn't known: "denied" (turned off for magpie in the
// system's settings) or "unavailable" (a build or desktop without them).
// Set by the process that has the tray.
var notifyProblem func() string

var (
	hoursName = regexp.MustCompile(`^(\d+) hours?$`)
	daysName  = regexp.MustCompile(`^(\d+) days?$`)
)

// alertWindowZh is a window's name in Chinese, as the page's i18n.js has
// the common ones; one it doesn't know stays as the vendor named it.
func alertWindowZh(name string) string {
	if m := hoursName.FindStringSubmatch(name); m != nil {
		return m[1] + " 小时"
	}
	if m := daysName.FindStringSubmatch(name); m != nil {
		return m[1] + " 天"
	}
	switch name {
	case "Weekly":
		return "每周"
	case "Monthly":
		return "每月"
	case "Daily":
		return "每日"
	}
	return name
}

// alertClockZh is provider.ResetClock in Chinese.
func alertClockZh(at, now time.Time) string {
	at, now = at.Local(), now.Local()
	day := func(t time.Time) time.Time { y, m, d := t.Date(); return time.Date(y, m, d, 0, 0, 0, 0, time.Local) }
	switch days := int(math.Round(day(at).Sub(day(now)).Hours() / 24)); {
	case days <= 0:
		return at.Format("15:04")
	case days == 1:
		return "明天 " + at.Format("15:04")
	case days < 7:
		return [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}[at.Weekday()] + " " + at.Format("15:04")
	}
	return fmt.Sprintf("%d月%d日 %s", at.Month(), at.Day(), at.Format("15:04"))
}

// alertWindowJa is a window's name in Japanese, as alertWindowZh.
func alertWindowJa(name string) string {
	if m := hoursName.FindStringSubmatch(name); m != nil {
		return m[1] + " 時間"
	}
	if m := daysName.FindStringSubmatch(name); m != nil {
		return m[1] + " 日"
	}
	switch name {
	case "Weekly":
		return "週間"
	case "Monthly":
		return "月間"
	case "Daily":
		return "日次"
	}
	return name
}

// alertClockJa is provider.ResetClock in Japanese.
func alertClockJa(at, now time.Time) string {
	at, now = at.Local(), now.Local()
	day := func(t time.Time) time.Time { y, m, d := t.Date(); return time.Date(y, m, d, 0, 0, 0, 0, time.Local) }
	switch days := int(math.Round(day(at).Sub(day(now)).Hours() / 24)); {
	case days <= 0:
		return at.Format("15:04")
	case days == 1:
		return "明日 " + at.Format("15:04")
	case days < 7:
		return [...]string{"日曜", "月曜", "火曜", "水曜", "木曜", "金曜", "土曜"}[at.Weekday()] + " " + at.Format("15:04")
	}
	return fmt.Sprintf("%d月%d日 %s", at.Month(), at.Day(), at.Format("15:04"))
}

// alertWindowDe is a window's name in German; one it doesn't know stays as
// the vendor named it.
func alertWindowDe(name string) string {
	if m := hoursName.FindStringSubmatch(name); m != nil {
		if m[1] == "1" {
			return "1 Stunde"
		}
		return m[1] + " Stunden"
	}
	if m := daysName.FindStringSubmatch(name); m != nil {
		if m[1] == "1" {
			return "1 Tag"
		}
		return m[1] + " Tage"
	}
	switch name {
	case "Weekly":
		return "Wöchentlich"
	case "Monthly":
		return "Monatlich"
	case "Daily":
		return "Täglich"
	}
	return name
}

// alertClockDe is provider.ResetClock in German.
func alertClockDe(at, now time.Time) string {
	at, now = at.Local(), now.Local()
	day := func(t time.Time) time.Time { y, m, d := t.Date(); return time.Date(y, m, d, 0, 0, 0, 0, time.Local) }
	switch days := int(math.Round(day(at).Sub(day(now)).Hours() / 24)); {
	case days <= 0:
		return "um " + at.Format("15:04")
	case days == 1:
		return "morgen um " + at.Format("15:04")
	case days < 7:
		return [...]string{"So.", "Mo.", "Di.", "Mi.", "Do.", "Fr.", "Sa."}[at.Weekday()] + " um " + at.Format("15:04")
	}
	return at.Format("2.1. um 15:04")
}

// alertText is a usage alert as a notification says it, in the language
// (en, zh, ja or de): the card, and what reached the line. left says a
// window by how much of it is left, as the Usage page does when set to.
func alertText(lang string, a provider.QuotaAlert, bal float64, left bool, now time.Time) (title, body string) {
	title = a.Name
	if a.User != "" {
		title += " · " + a.User
	}
	if a.Window == "" {
		line := strconv.FormatFloat(bal, 'f', -1, 64)
		switch lang {
		case "zh":
			return title, fmt.Sprintf("余额已降至 %s（提醒线 %s）", a.Balance, line)
		case "ja":
			return title, fmt.Sprintf("残高が %s まで減りました（通知ライン %s）", a.Balance, line)
		case "de":
			return title, fmt.Sprintf("Guthaben auf %s gesunken (Warnschwelle %s)", a.Balance, line)
		}
		return title, fmt.Sprintf("Balance down to %s (alert at %s)", a.Balance, line)
	}
	n := a.Used
	if left {
		n = max(0, 100-n)
	}
	pct := strconv.FormatFloat(math.Round(n*10)/10, 'f', -1, 64) + "%"
	if lang == "zh" {
		body = fmt.Sprintf("%s窗口已用 %s", alertWindowZh(a.Window), pct)
		if left {
			body = fmt.Sprintf("%s窗口剩余 %s", alertWindowZh(a.Window), pct)
		}
		if a.ResetsAt != nil {
			body += "，" + alertClockZh(*a.ResetsAt, now) + " 重置"
		}
		return title, body
	}
	if lang == "ja" {
		body = fmt.Sprintf("%s枠を %s 使用", alertWindowJa(a.Window), pct)
		if left {
			body = fmt.Sprintf("%s枠の残り %s", alertWindowJa(a.Window), pct)
		}
		if a.ResetsAt != nil {
			body += "、" + alertClockJa(*a.ResetsAt, now) + " にリセット"
		}
		return title, body
	}
	if lang == "de" {
		pct = strings.Replace(pct, ".", ",", 1) // German decimal comma
		body = fmt.Sprintf("%s: %s verbraucht", alertWindowDe(a.Window), pct)
		if left {
			body = fmt.Sprintf("%s: %s übrig", alertWindowDe(a.Window), pct)
		}
		if a.ResetsAt != nil {
			body += ", Zurücksetzung " + alertClockDe(*a.ResetsAt, now)
		}
		return title, body
	}
	body = fmt.Sprintf("%s: %s used", a.Window, pct)
	if left {
		body = fmt.Sprintf("%s: %s left", a.Window, pct)
	}
	if a.ResetsAt != nil {
		body += ", resets " + provider.ResetClock(*a.ResetsAt, now)
	}
	return title, body
}
