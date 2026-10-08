//go:build linux && !nogui

package gui

import (
	"log"
	"reflect"
	"unsafe"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// setTrayTip puts the tooltip on the tray's StatusNotifierItem. Wails
// (v3.0.0-beta.24) leaves SetTooltip on Linux a no-op ("TBD"): the item's
// ToolTip stays the "magpie" it was made with and no NewToolTip is sent, so
// a bar never had the quotas to show (atie on Discord: Omarchy's bar showed
// none on hover with 菜单栏显示额度 on). This sets the property Wails
// exported, through its own prop.Properties (which keeps the (sa(iiay)ss)
// type), and sends NewToolTip from the same connection: waybar reads ToolTip
// again only on that signal, Quickshell on it or PropertiesChanged.
func setTrayTip(tray *application.SystemTray, tip string) bool {
	props := trayProps(tray)
	if props == nil {
		return false
	}
	v := struct {
		V0 string           // icon name
		V1 []application.PX // icon pixmaps
		V2 string           // title: what Omarchy's bar shows, and waybar
		V3 string           // description: waybar only, under the title
	}{V2: sniTip(tip), V1: []application.PX{}}
	if err := props.Set("org.kde.StatusNotifierItem", "ToolTip", dbus.MakeVariant(v)); err != nil {
		log.Println("tray: tooltip:", err)
		return false
	}
	conn, err := dbus.SessionBus() // the connection Wails' tray is on
	if err != nil {
		return false
	}
	if err := conn.Emit("/StatusNotifierItem", "org.kde.StatusNotifierItem.NewToolTip"); err != nil {
		log.Println("tray: tooltip signal:", err)
	}
	return true
}

// trayProps is the item's exported properties inside Wails' Linux tray
// (SystemTray.impl.props), nil once a Wails version keeps them elsewhere.
func trayProps(tray *application.SystemTray) (props *prop.Properties) {
	defer func() {
		if recover() != nil {
			props = nil
		}
	}()
	if tray == nil {
		return nil
	}
	impl := reflect.ValueOf(tray).Elem().FieldByName("impl")
	if !impl.IsValid() || impl.Kind() != reflect.Interface || impl.IsNil() {
		return nil
	}
	p := impl.Elem()
	if p.Kind() != reflect.Pointer || p.IsNil() || p.Elem().Kind() != reflect.Struct {
		return nil
	}
	f := p.Elem().FieldByName("props")
	if !f.IsValid() || f.Type() != reflect.TypeOf((*prop.Properties)(nil)) {
		return nil
	}
	return *(**prop.Properties)(unsafe.Pointer(f.UnsafeAddr()))
}

// setTrayPassive takes the tray's StatusNotifierItem out of the bar's tray
// (Status "Passive") while magpie's icon is in Omarchy's bar as its widget,
// and puts it back ("Active") when it isn't. Omarchy's tray and waybar's both
// leave a Passive item out. Wails exports Status read-only and has no Hide
// on Linux, so it is set through Wails' own properties, as the tooltip is,
// and NewStatus is sent for the hosts that read it again only on that.
func setTrayPassive(tray *application.SystemTray, passive bool) (ok bool) {
	props := trayProps(tray)
	if props == nil {
		return false
	}
	status := "Active"
	if passive {
		status = "Passive"
	}
	defer func() {
		if r := recover(); r != nil {
			log.Println("tray: status:", r)
			ok = false
		}
	}()
	props.SetMust("org.kde.StatusNotifierItem", "Status", status)
	conn, err := dbus.SessionBus() // the connection Wails' tray is on
	if err != nil {
		return false
	}
	if err := conn.Emit("/StatusNotifierItem", "org.kde.StatusNotifierItem.NewStatus", status); err != nil {
		log.Println("tray: status signal:", err)
	}
	return true
}
