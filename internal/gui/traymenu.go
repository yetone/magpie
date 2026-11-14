package gui

import (
	"fmt"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/uilang"
)

// trayZh is the tray menu in Chinese; the page has its own words
// (i18n.js), and these few are all the menu and the icon's tooltip show
// (#301).
var trayZh = map[string]string{
	"Open magpie":                          "打开 magpie",
	"Version %s":                           "版本 %s",
	"Restart to Update":                    "重启以更新",
	"Restart to Update to %s":              "重启以更新到 %s",
	"Quit magpie":                          "退出 magpie",
	"Restart Now to Update":                "立即重启以更新",
	"Restart Now to Update (%d in flight)": "立即重启以更新（%d 个进行中）",
	// the gateway paused from the menu (gatewaypause.go)
	"Pause Gateway":  "暂停网关",
	"Resume Gateway": "恢复网关",
	// the tray icon's tooltip (trayusage.go; yi_time on Discord)
	"%s used":                             "已用 %s",
	"%s left":                             "剩余 %s",
	"pace %s":                             "增速 %s",
	"resets in %s":                        "%s 后重置",
	"%dd %dh":                             "%d 天 %d 小时",
	"%dh %dm":                             "%d 小时 %d 分",
	"%dm":                                 "%d 分钟",
	"as of %s, couldn't be read just now": "截至 %s，暂时无法获取最新额度",
	"Jan 2 15:04":                         "1月2日 15:04",
	"Today in magpie":                     "magpie 今日",
	"Last 7 days in magpie":               "magpie 近 7 天",
	"%d requests":                         "%d 个请求",
	"%d request":                          "%d 个请求",
	", %d with no known price":            "，其中 %d 个没有已知价格",
	"%s in":                               "输入 %s",
	"%s out":                              "输出 %s",
	"%s cached":                           "缓存 %s",
	"%s tokens (%s)":                      "%s token（%s）",
	"Today: no requests yet":              "今天：还没有请求",
	"Today: %s":                           "今天：%s",
}

// trayZhTW is the tray menu in Traditional Chinese, with Taiwan's words.
var trayZhTW = map[string]string{
	"Open magpie":                          "開啟 magpie",
	"Version %s":                           "版本 %s",
	"Restart to Update":                    "重新啟動以更新",
	"Restart to Update to %s":              "重新啟動以更新到 %s",
	"Quit magpie":                          "結束 magpie",
	"Restart Now to Update":                "立即重新啟動以更新",
	"Restart Now to Update (%d in flight)": "立即重新啟動以更新（%d 個進行中）",
	// the gateway paused from the menu (gatewaypause.go)
	"Pause Gateway":  "暫停閘道",
	"Resume Gateway": "恢復閘道",
	// the tray icon's tooltip (trayusage.go; yi_time on Discord)
	"%s used":                             "已用 %s",
	"%s left":                             "剩餘 %s",
	"pace %s":                             "增速 %s",
	"resets in %s":                        "%s 後重設",
	"%dd %dh":                             "%d 天 %d 小時",
	"%dh %dm":                             "%d 小時 %d 分",
	"%dm":                                 "%d 分鐘",
	"as of %s, couldn't be read just now": "截至 %s，暫時無法取得最新額度",
	"Jan 2 15:04":                         "1月2日 15:04",
	"Today in magpie":                     "magpie 今日",
	"Last 7 days in magpie":               "magpie 近 7 天",
	"%d requests":                         "%d 個請求",
	"%d request":                          "%d 個請求",
	", %d with no known price":            "，其中 %d 個沒有已知價格",
	"%s in":                               "輸入 %s",
	"%s out":                              "輸出 %s",
	"%s cached":                           "快取 %s",
	"%s tokens (%s)":                      "%s token（%s）",
	"Today: no requests yet":              "今天：還沒有請求",
	"Today: %s":                           "今天：%s",
}

// trayJa is the tray menu in Japanese.
var trayJa = map[string]string{
	"Open magpie":                          "magpie を開く",
	"Version %s":                           "バージョン %s",
	"Restart to Update":                    "再起動してアップデート",
	"Restart to Update to %s":              "再起動して %s にアップデート",
	"Quit magpie":                          "magpie を終了",
	"Restart Now to Update":                "今すぐ再起動してアップデート",
	"Restart Now to Update (%d in flight)": "今すぐ再起動してアップデート（%d 件処理中）",
	// the gateway paused from the menu (gatewaypause.go)
	"Pause Gateway":  "ゲートウェイを一時停止",
	"Resume Gateway": "ゲートウェイを再開",
	// the tray icon's tooltip (trayusage.go; yi_time on Discord)
	"%s used":                             "使用済み %s",
	"%s left":                             "残り %s",
	"pace %s":                             "増え方 %s",
	"resets in %s":                        "%s 後にリセット",
	"%dd %dh":                             "%d日 %d時間",
	"%dh %dm":                             "%d時間 %d分",
	"%dm":                                 "%d分",
	"as of %s, couldn't be read just now": "%s 時点、現在は読み取れません",
	"Jan 2 15:04":                         "1月2日 15:04",
	"Today in magpie":                     "magpie の今日",
	"Last 7 days in magpie":               "magpie の過去 7 日間",
	"%d requests":                         "%d 件のリクエスト",
	"%d request":                          "%d 件のリクエスト",
	", %d with no known price":            "、うち %d 件は価格不明",
	"%s in":                               "入力 %s",
	"%s out":                              "出力 %s",
	"%s cached":                           "キャッシュ %s",
	"%s tokens (%s)":                      "%s トークン（%s）",
	"Today: no requests yet":              "今日：まだリクエストはありません",
	"Today: %s":                           "今日：%s",
}

// trayDe is the tray menu in German.
var trayDe = map[string]string{
	"Open magpie":                          "magpie öffnen",
	"Version %s":                           "Version %s",
	"Restart to Update":                    "Zum Aktualisieren neu starten",
	"Restart to Update to %s":              "Zum Aktualisieren auf %s neu starten",
	"Quit magpie":                          "magpie beenden",
	"Restart Now to Update":                "Jetzt neu starten und aktualisieren",
	"Restart Now to Update (%d in flight)": "Jetzt neu starten und aktualisieren (%d laufend)",
	// the gateway paused from the menu (gatewaypause.go)
	"Pause Gateway":  "Zugangspunkt pausieren",
	"Resume Gateway": "Zugangspunkt fortsetzen",
	// the tray icon's tooltip (trayusage.go; yi_time on Discord)
	"%s used":                             "%s verbraucht",
	"%s left":                             "%s übrig",
	"pace %s":                             "Tempo %s",
	"resets in %s":                        "Reset in %s",
	"%dd %dh":                             "%d T %d h",
	"%dh %dm":                             "%d h %d min",
	"%dm":                                 "%d min",
	"as of %s, couldn't be read just now": "Stand %s, konnte gerade nicht gelesen werden",
	"Jan 2 15:04":                         "2.1. 15:04",
	"Today in magpie":                     "Heute in magpie",
	"Last 7 days in magpie":               "Letzte 7 Tage in magpie",
	"%d requests":                         "%d Anfragen",
	"%d request":                          "%d Anfrage",
	", %d with no known price":            ", %d ohne bekannten Preis",
	"%s in":                               "%s Eingabe",
	"%s out":                              "%s Ausgabe",
	"%s cached":                           "%s Cache",
	"%s tokens (%s)":                      "%s Token (%s)",
	"Today: no requests yet":              "Heute: noch keine Anfragen",
	"Today: %s":                           "Heute: %s",
}

// trayWords are the tray menu's and the tray icon's tooltip's
// translations by language.
var trayWords = map[string]map[string]string{"zh": trayZh, "zh-TW": trayZhTW, "ja": trayJa, "de": trayDe}

// onLang relabels the tray menu when the Settings page changes the
// language; set by the process that has the tray.
var onLang func()

// trayLang is the language the tray menu is in, as the page picks its own
// (uilang.Pick).
func trayLang(pref string, system func() string) string { return uilang.Pick(pref, system) }

// systemLang is the system's language (uilang.System).
func systemLang() string { return uilang.System() }

// notesLang is the language release notes are asked in for lang: there are
// no German ones, so German asks for the English, and Traditional Chinese
// asks for the Chinese ones.
func notesLang(lang string) string {
	switch lang {
	case "de":
		return "en"
	case "zh-TW":
		return "zh"
	}
	return lang
}

// trayText is a menu line in the language, English where it has none.
func trayText(lang, key string, args ...any) string {
	if s, ok := trayWords[lang][key]; ok {
		key = s
	}
	if len(args) == 0 {
		return key
	}
	return fmt.Sprintf(key, args...)
}

// trayLabels are the tray menu's lines: open, pause (the gateway's, or
// its resume), version, restart and quit.
type trayLabels struct{ open, pause, version, restart, quit string }

// trayMenuLabels are the lines in the language; update is the version
// waiting for a restart, if there is one; paused, the gateway is.
func trayMenuLabels(lang, version, update string, paused bool) trayLabels {
	l := trayLabels{
		open:    trayText(lang, "Open magpie"),
		pause:   trayText(lang, "Pause Gateway"),
		version: trayText(lang, "Version %s", version),
		restart: trayText(lang, "Restart to Update"),
		quit:    trayText(lang, "Quit magpie"),
	}
	if update != "" {
		l.restart = trayText(lang, "Restart to Update to %s", update)
	}
	if paused {
		l.pause = trayText(lang, "Resume Gateway")
	}
	return l
}

// trayRestartNow is the restart item while a restart waits for the
// gateway (#577): a click restarts at once, cutting short what b has in
// flight.
func trayRestartNow(lang string, b gateway.Busy) string {
	if n := b.Requests + b.Tools; n > 0 {
		return trayText(lang, "Restart Now to Update (%d in flight)", n)
	}
	return trayText(lang, "Restart Now to Update")
}
