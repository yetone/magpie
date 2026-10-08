package gui

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/yetone/magpie/internal/omarchy"
)

// onOmarchy is whether magpie runs on Omarchy, asked once: its windows then
// take Omarchy's look and its bar's place instead of the tray's.
var onOmarchy = omarchy.Detect()

// hyprland is whether magpie runs under Hyprland (omarchy.Hyprland), a
// variable so a test can stand in for it.
var hyprland = omarchy.Hyprland

// onBarIcon is told whether magpie's icon is in Omarchy's bar once Settings'
// Bar icon has changed it. The app then takes its tray item out of the bar's
// tray (setTrayInBar), so the bar shows one magpie, not the widget and the
// tray icon side by side (Alex on Discord: two icons with Bar icon on).
var onBarIcon func(on bool)

// omarchyTheme is the Omarchy theme the page draws with, when on Omarchy.
func omarchyTheme() (omarchy.Theme, bool) {
	if !onOmarchy {
		return omarchy.Theme{}, false
	}
	return omarchy.Current()
}

// barIcon is whether magpie can put its icon in Omarchy's bar (the desktop
// app on Omarchy's Hyprland; not a browser's page), and whether it has.
func barIcon(w Windows) map[string]bool {
	ok := onOmarchy && !isWeb(w) && hyprland()
	return map[string]bool{"available": ok, "on": ok && omarchy.WidgetOn()}
}

// omarchyRoutes are Settings' Bar icon: magpie's icon in Omarchy's bar
// beside its own, opening the quick panel, instead of in the tray's drawer.
func omarchyRoutes(mux *http.ServeMux, w Windows) {
	mux.HandleFunc("GET /api/omarchy/widget", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, barIcon(w))
	})
	mux.HandleFunc("POST /api/omarchy/widget", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ On bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if !barIcon(w)["available"] {
			http.Error(rw, "Omarchy's bar isn't here", http.StatusConflict)
			return
		}
		var err error
		if in.On {
			var exe string
			if exe, err = os.Executable(); err == nil {
				err = omarchy.AddWidget(exe)
			}
		} else {
			err = omarchy.RemoveWidget()
		}
		// what is in the bar now, though the omarchy command failed after
		// the files went in or out
		if onBarIcon != nil {
			onBarIcon(barIcon(w)["on"])
		}
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, barIcon(w))
	})
	// a widget put in by an older magpie, or one elsewhere, runs this one
	if barIcon(w)["on"] {
		go func() {
			if exe, err := os.Executable(); err == nil {
				if err := omarchy.KeepWidget(exe); err != nil {
					log.Println("omarchy bar icon:", err)
				}
			}
		}()
	}
}
