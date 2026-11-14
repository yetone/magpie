//go:build !nogui

package gui

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// watchTrayUsage keeps the tray's cards up to date, and magpie's name over
// today's total in the tooltip while there are none (trayIdleTip).
func (h *host) watchTrayUsage() {
	wake := make(chan struct{}, 1)
	onTrayUsage = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	// a card read while stale is refreshed behind it: read it again once
	// that lands, not a tick later, so the menu bar says what the panel does
	provider.OnSubscriptionUsage(onTrayUsage)
	// another account answering, or the agent switched to another: a card
	// following the account in use reads it at once (#1516)
	provider.OnServedMoved(onTrayUsage)
	// the taskbar turned dark or light: the figure in its colour
	if runtime.GOOS == "windows" {
		h.app.Event.OnApplicationEvent(events.Windows.SystemThemeChanged, func(*application.ApplicationEvent) { onTrayUsage() })
	}
	// Schedule native cell clicks on the application thread after startup.
	onTrayCellClick = func(id string) {
		h.whenReady(func() { application.InvokeAsync(func() { h.trayCellClick(id) }) })
	}
	go func() {
		<-h.ready // the tray is made once the app runs
		shown, drawn, numbered := "", false, false
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			s := settings.Load()
			// in magpie's language, as the tray menu is (yi_time)
			lang := trayLang(s.Lang, systemLang)
			cells, label, tip := trayCardsView(lang, trayUsageCards(ctx, lang, s.TrayUsages), time.Now(), s.QuotaLeft, s.TrayPace, s.TrayWindows)
			cancel()
			if s.TrayNoLogos {
				cells = trayPlain(cells)
			}
			if tip == "" {
				currency, rate := trayCurrency()
				tip = trayIdleTip(lang, trayMoney(trayTotalSum(usage.Today), s.TrayNoCost), currency, rate)
			}
			// Windows' tray shows no text: the first card's figure is its
			// icon instead (traynumber.go), drawn for the taskbar's size and
			// colour
			fig, figKey := trayIconFigure(runtime.GOOS, s.TrayNoNumber, cells), ""
			if fig != "" {
				figKey = fmt.Sprint(fig, "@", trayIconSize(), trayTaskbarDark())
			}
			if runtime.GOOS != "darwin" {
				cells = nil
			} else if label != "" {
				// the menu bar sets the text hard against the icon
				label = " " + label
			}
			// what is shown, to set it again only when it changes
			now := label + "\x00" + tip + "\x00" + figKey
			for _, c := range cells {
				now += "\x00" + c.Letter + strings.Join(c.Rows, "\x01")
				if c.Plain {
					now += "\x02"
				}
			}
			if now != shown {
				shown = now
				// the Mac's cells as an image, the text where it can't be
				if len(cells) > 0 {
					h.tray.SetLabel("")
				} else {
					if drawn {
						drawn = false
						trayImageHide()
						h.tray.SetTemplateIcon(trayIcon)
					}
					h.tray.SetLabel(label)
				}
				if runtime.GOOS == "windows" {
					numbered = h.setTrayNumber(fig, numbered)
				}
				h.tray.SetTooltip(tip)
				// Wails' is a no-op on Linux; until the tray is up, again next time
				if !setTrayTip(h.tray, tip) {
					shown = ""
				}
			}
			// Reapply even unchanged cells: a system menu-bar rebuild can
			// replace the composed image with Wails' bird icon.
			if len(cells) > 0 {
				bird := trayIcon
				if s.TrayNoBird {
					bird = nil // the cards alone
				}
				if trayImageShow(cells, bird) {
					drawn = true
				} else {
					h.tray.SetLabel(label)
					shown = "" // and try again next time
				}
			}
			// the first answer can take a while; look again soon after it
			next := trayUsageEvery()
			if label == "" && len(s.TrayUsages) > 0 {
				next = 20 * time.Second
			}
			select {
			case <-wake:
			case <-time.After(next):
			case <-backendCtx.Done():
				// handed the gateway to the new version, whose icon
				// shows the usage now: this one's is hidden (#4938)
				return
			}
		}
	}()
}

// setTrayNumber makes Windows' tray icon fig (trayNumberIcon), or the
// app's icon again when there is none and it was one (numbered); it says
// whether the icon is a figure now. Wails sets one icon for a dark and a
// light taskbar alike, so it is drawn again when the taskbar's changes.
func (h *host) setTrayNumber(fig string, numbered bool) bool {
	if fig != "" {
		if icon := trayNumberIcon(fig, trayIconSize(), trayTaskbarDark()); icon != nil {
			h.tray.SetIcon(icon)
			return true
		}
	}
	if numbered {
		h.tray.SetIcon(winTrayIcon)
	}
	return false
}

// trayCellClick opens the allowance represented by the clicked image cell.
// A visible panel closes on the next click, as clicking the bird does.
func (h *host) trayCellClick(id string) {
	if id == "" {
		return
	}
	if settings.Shared().Tray == "window" {
		h.ShowMain(quotaView(id))
		return
	}
	w, _ := h.panelWin()
	if !windowOpen(w) {
		w.ExecJS(panelQuotaJS(id))
	}
	h.togglePanel()
}
