//go:build !nogui

package gui

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// notifier is the system's notifications, started the first time an alert
// needs them rather than with the app: started with it, a Mac build that
// isn't a signed bundle, or a Linux desktop without a notification daemon,
// would stop the app from starting at all.
type notifier struct {
	once sync.Once
	ns   *notifications.NotificationService
	err  error
	h    *host
	// refused is the Mac's answer when asked this run was no: before it
	// is asked, not yet allowed and turned off read the same
	refused atomic.Bool
}

func (n *notifier) start() error {
	n.once.Do(func() {
		ns := notifications.New()
		// on the main thread, as the app would start it: Windows registers
		// its toast activator for the thread it is on
		if err := application.InvokeSyncWithError(func() error {
			return ns.ServiceStartup(context.Background(), application.ServiceOptions{})
		}); err != nil {
			n.err = err
			log.Println("notifications:", err)
			return
		}
		if runtime.GOOS == "linux" {
			_ = ns.RegisterNotificationCategory(clickCategory)
		}
		ns.OnNotificationResponse(func(r notifications.NotificationResult) {
			if v := clickView(runtime.GOOS, r.Response); v != "" {
				n.h.whenReady(func() { application.InvokeAsync(func() { n.h.ShowMain(v) }) })
			}
		})
		n.ns = ns
	})
	return n.err
}

// allowed asks for the system's leave to notify where it is asked for (the
// Mac, which asks once and then keeps the answer).
func (n *notifier) allowed() bool {
	if n.start() != nil {
		return false
	}
	if ok, err := n.ns.CheckNotificationAuthorization(); err == nil && ok {
		return true
	}
	ok, err := n.ns.RequestNotificationAuthorization()
	n.refused.Store(err == nil && !ok)
	return err == nil && ok
}

// problem is notifyProblem: why a notification wouldn't be seen.
func (n *notifier) problem() string {
	select {
	case <-n.h.ready:
	default:
		return "" // the app isn't running yet
	}
	if n.start() != nil {
		return "unavailable"
	}
	if !n.refused.Load() {
		return ""
	}
	if ok, err := n.ns.CheckNotificationAuthorization(); err == nil && !ok {
		return "denied"
	}
	return ""
}

// turnNotice is the id a turn's end is notified under, an alert's
// "magpie-alert-"; maskNotice a value masked for the first time, followed
// by its record's key and "-"
const (
	turnNotice = "magpie-turn-"
	maskNotice = "magpie-mask-"
)

func (n *notifier) send(title, body string) { n.sendAs("magpie-alert-", title, body) }

func (n *notifier) sendAs(kind, title, body string) {
	// every alert goes to Bark too, when it is set, whether or not the
	// system lets this computer notify (EV_John_Liu on X)
	barkPush(title, body)
	if !n.allowed() {
		return
	}
	o := notice(runtime.GOOS, fmt.Sprintf("%s%d", kind, time.Now().UnixNano()), title, body)
	send := n.ns.SendNotification
	if o.CategoryID != "" {
		send = n.ns.SendNotificationWithActions
	}
	if err := send(o); err != nil {
		log.Println("notifications:", err)
	}
}

// linuxClick is the freedesktop action key a click on a notification's body
// invokes, and clickCategory gives an action that id. Wails reports an
// action of a category under its own id, and maps "default" to
// DefaultActionIdentifier only without one; it reports a Linux
// notification closed by the user, with its × or cleared from the list, as
// DefaultActionIdentifier too. Under clickCategory the two come back apart.
const linuxClick = "default"

var clickCategory = notifications.NotificationCategory{
	ID:      "magpie-click",
	Actions: []notifications.NotificationAction{{ID: linuxClick, Title: "Open"}},
}

// notice is the notification sent as id: on Linux under clickCategory.
func notice(goos, id, title, body string) notifications.NotificationOptions {
	o := notifications.NotificationOptions{ID: id, Title: title, Body: body}
	if goos == "linux" {
		o.CategoryID = clickCategory.ID
	}
	return o
}

// clickView is the page a response to a notification opens, "" for none.
// A click on an alert opens Usage; on a value masked, the 脱敏 page at the
// request that masked it; on a sign-in alert, its provider's accounts. On a turn's end, where the user goes is their
// agent, not magpie. On Linux only a click (linuxClick) is one: an alert
// dismissed there opened the main window, maximised if it was left so,
// over the desktop with the focus (#2513).
func clickView(goos string, r notifications.NotificationResponse) string {
	if strings.HasPrefix(r.ID, turnNotice) || goos == "linux" && r.ActionIdentifier != linuxClick {
		return ""
	}
	if rest, ok := strings.CutPrefix(r.ID, maskNotice); ok {
		key, _, _ := strings.Cut(rest, "-")
		if _, err := strconv.ParseInt(key, 10, 64); err != nil || key == "0" {
			return "masking"
		}
		return "masking&key=" + key
	}
	if rest, ok := strings.CutPrefix(r.ID, signInNotice); ok {
		return signInView(rest)
	}
	return "usage"
}

// watchAlerts tells the user, with a notification, of a window used past
// the share or a balance fallen to the amount set in Settings (#368).
func (h *host) watchAlerts() {
	n := &notifier{h: h}
	wake := make(chan struct{}, 1)
	onAlerts = func() {
		// the Mac's question asked as the alert is turned on, not later
		// when one is due and the user is elsewhere
		go n.allowed()
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	notifyProblem = n.problem
	go func() {
		<-h.ready
		// set before this run: asked now, so Settings can say if it was refused
		if s := settings.Load(); s.UsageAlert > 0 || s.BalanceAlert > 0 || s.ResetReminder > 0 || s.TurnDoneAlert > 0 || s.MaskAlert {
			go n.allowed()
		}
		// a value masked for the first time (Must2333 on X)
		masks := newMaskAlerts(func() bool { return settings.Shared().MaskAlert }, func(m maskNote) {
			title, body := maskAlertText(trayLang(settings.Shared().Lang, systemLang), m, agentName)
			n.sendAs(fmt.Sprintf("%s%d-", maskNotice, m.Key), title, body)
		})
		gateway.OnMasked = masks.add
		// an agent's turn done, past the length set (itieniu on X)
		gateway.TurnDone = func(t gateway.TurnEnd) {
			title, body := turnDoneText(trayLang(settings.Shared().Lang, systemLang), agentName(t.Agent), t)
			n.sendAs(turnNotice, title, body)
		}
		// an account in use signed out, or a Claude sign-in about to end
		// (steven_4422 on Discord): always on, as the account's Sign in
		// again is
		go watchBarkDaily(backendCtx)
		go provider.WatchSignIns(backendCtx, func(a provider.SignInAlert) {
			title, body := signInText(trayLang(settings.Shared().Lang, systemLang), a, time.Now())
			n.sendAs(signInKind(a.Provider), title, body)
		})
		// until this one has handed the gateway to the new version,
		// which alerts from then on (#4938)
		provider.WatchQuotas(backendCtx, wake, func(a provider.QuotaAlert) {
			s := settings.Load()
			title, body := alertText(trayLang(s.Lang, systemLang), a, s.BalanceAlert, s.QuotaLeft, time.Now())
			n.send(title, body)
		})
	}()
}
