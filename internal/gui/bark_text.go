package gui

import (
	"fmt"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// barkNearPct is the share of a window the daily summary calls near its end
// when Settings' Allowance alert is off (with it on, its own share).
const barkNearPct = 80

// barkNearMax is how many near-limit lines a push carries before "and N
// more": a push is read on a lock screen.
const barkNearMax = 8

// barkWords is the daily summary's and the test push's words, in the
// languages the notifications speak. %s is an amount, %d a count.
var barkWords = map[string]map[string]string{
	"en": {
		"title": "magpie · today", "spent": "Spent about %s today · %d requests", "calls": "%d requests today",
		"none": "No requests yet today", "near": "Near the limit:", "clear": "No allowance is near its limit.",
		"more": "…and %d more", "test": "Bark is set up: alerts and the daily summary come here.",
	},
	"zh": {
		"title": "magpie · 今日", "spent": "今日约花费 %s · %d 次请求", "calls": "今日 %d 次请求",
		"none": "今天还没有请求", "near": "额度快用完：", "clear": "没有额度接近上限。",
		"more": "…另有 %d 项", "test": "Bark 已接通：提醒和每日汇总会发到这里。",
	},
	"zh-TW": {
		"title": "magpie · 今日", "spent": "今日約花費 %s · %d 次請求", "calls": "今日 %d 次請求",
		"none": "今天還沒有請求", "near": "額度快用完：", "clear": "沒有額度接近上限。",
		"more": "…另有 %d 項", "test": "Bark 已接通：提醒和每日彙總會發到這裡。",
	},
	"ja": {
		"title": "magpie · 今日", "spent": "今日の支出は約 %s · %d リクエスト", "calls": "今日 %d リクエスト",
		"none": "今日はまだリクエストがありません", "near": "上限が近い枠：", "clear": "上限に近い枠はありません。",
		"more": "…ほか %d 件", "test": "Bark につながりました。通知と毎日のまとめがここに届きます。",
	},
	"de": {
		"title": "magpie · heute", "spent": "Heute etwa %s ausgegeben · %d Anfragen", "calls": "%d Anfragen heute",
		"none": "Heute noch keine Anfragen", "near": "Nahe am Limit:", "clear": "Kein Kontingent ist nahe am Limit.",
		"more": "…und %d weitere", "test": "Bark ist eingerichtet: Hinweise und die tägliche Übersicht kommen hierher.",
	},
}

func barkWord(lang, key string, args ...any) string {
	s, ok := barkWords[lang][key]
	if !ok {
		s = barkWords["en"][key]
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// barkTestText is the push Settings' Test sends.
func barkTestText(lang string) (title, body string) {
	return "magpie", barkWord(lang, "test")
}

// barkDailyText is the once-a-day push (EV_John_Liu on X): what today cost
// and the windows and balances near their end, each as its alert would say
// it. near is what provider.NearLimits found, bal the balance alert's line.
func barkDailyText(lang string, today usage.Totals, currency string, rate float64, near []provider.QuotaAlert, bal float64, left bool, now time.Time) (title, body string) {
	var lines []string
	switch {
	case today.Calls == 0:
		lines = append(lines, barkWord(lang, "none"))
	case today.Cost > 0:
		c := usage.FormatCost(today.Cost, currency, rate)
		if today.Unpriced > 0 {
			c += "+"
		}
		lines = append(lines, barkWord(lang, "spent", c, today.Calls))
	default:
		lines = append(lines, barkWord(lang, "calls", today.Calls))
	}
	if len(near) == 0 {
		lines = append(lines, barkWord(lang, "clear"))
	} else {
		lines = append(lines, barkWord(lang, "near"))
		for i, a := range near {
			if i == barkNearMax {
				lines = append(lines, barkWord(lang, "more", len(near)-i))
				break
			}
			t, b := alertText(lang, a, bal, left, now)
			lines = append(lines, "· "+t+": "+b)
		}
	}
	return barkWord(lang, "title"), strings.Join(lines, "\n")
}
