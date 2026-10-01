//go:build !nogui

package gui

import (
	"context"
	"runtime"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// watchTrayUsage keeps the tray's cards up to date, and plain "magpie" in
// the tooltip while there are none.
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
	provider.OnSubscriptionUsage = onTrayUsage
	go func() {
		<-h.ready // the tray is made once the app runs
		shown, drawn := "", false
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			s := settings.Load()
			cells, label, tip := trayUsageView(trayUsageCards(ctx, s.TrayUsages), time.Now(), s.QuotaLeft)
			cancel()
			if s.TrayNoLogos {
				cells = trayPlain(cells)
			}
			if tip == "" {
				tip = "magpie"
			}
			if runtime.GOOS != "darwin" {
				cells = nil
			} else if label != "" {
				// the menu bar sets the text hard against the icon
				label = " " + label
			}
			// what is shown, to set it again only when it changes
			now := label + "\x00" + tip
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
					if trayImageShow(cells, trayIcon) {
						drawn = true
					} else {
						h.tray.SetLabel(label)
						shown = "" // and try again next time
					}
				} else {
					if drawn {
						drawn = false
						trayImageHide()
						h.tray.SetTemplateIcon(trayIcon)
					}
					h.tray.SetLabel(label)
				}
				h.tray.SetTooltip(tip)
			}
			// the first answer can take a while; look again soon after it
			next := trayUsageEvery()
			if label == "" && len(s.TrayUsages) > 0 {
				next = 20 * time.Second
			}
			select {
			case <-wake:
			case <-time.After(next):
			}
		}
	}()
}
