package main

import (
	"context"
	"io"
	"reflect"

	"fyne.io/systray"
)

// cmdTray runs the session's update icon: a StatusNotifierItem that follows
// the root service. --no-icon runs the same loop without the icon, which is
// what `notify` is now.
func cmdTray(args []string, stderr io.Writer) int {
	fs := newFlags("tray", stderr)
	noIcon := fs.Bool("no-icon", false, "run without the tray icon")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	return runTray(*noIcon, stderr)
}

func runTray(noIcon bool, stderr io.Writer) int {
	// The agent flavor has no service and no panel; its session autostart
	// may still carry an old line, so leave quietly before touching any bus.
	// A missing release file is not an exit: the service reports unmanaged.
	rel, err := LoadRelease(releaseFile())
	if err == nil && rel.Flavor == flavorAgent {
		return exitOK
	}
	app := newTrayApp(rel, noUI{}, dialDaemon, stderr)
	if noIcon {
		app.noIcon = true
		app.run(context.Background())
		return exitOK
	}
	// systray registers with the StatusNotifierWatcher itself, and again
	// whenever a watcher takes the name, so a bar that starts after the tray
	// still shows it.
	systray.Run(func() {
		systray.SetTitle("Hadron Desktop updates")
		app.ui = newSystrayUI(app.clicks)
		go app.run(context.Background())
	}, nil)
	return exitOK
}

// systrayUI is the thin glue between a trayView and fyne.io/systray. Menu
// rules live in BuildMenu; this only draws what it is given.
type systrayUI struct {
	clicks chan<- string
	icons  map[string][]byte
	last   trayView
	shown  bool
}

func newSystrayUI(clicks chan<- string) *systrayUI {
	u := &systrayUI{clicks: clicks, icons: map[string][]byte{}}
	// One pixmap per icon: SNI hosts scale it to the panel, and 44 px stays
	// sharp on a HiDPI panel.
	for kind := range iconColors {
		u.icons[kind] = iconPNG(kind, 44)
	}
	return u
}

func (u *systrayUI) Show(v trayView) {
	if !u.shown || v.Icon != u.last.Icon {
		systray.SetIcon(u.icons[v.Icon])
	}
	if !u.shown || v.Tooltip != u.last.Tooltip {
		systray.SetTooltip(v.Tooltip)
	}
	if !u.shown || !reflect.DeepEqual(v.Menu, u.last.Menu) {
		// ResetMenu closes every old item's ClickedCh, which ends its
		// listener below.
		systray.ResetMenu()
		for _, it := range v.Menu {
			u.add(it, systray.AddMenuItem(it.Label, ""))
		}
	}
	u.last, u.shown = v, true
}

func (u *systrayUI) add(it MenuItem, m *systray.MenuItem) {
	if !it.Enabled {
		m.Disable()
	}
	for _, c := range it.Children {
		u.add(c, m.AddSubMenuItem(c.Label, ""))
	}
	if len(it.Children) == 0 && it.Enabled {
		go func(id string) {
			for range m.ClickedCh {
				u.clicks <- id
			}
		}(it.ID)
	}
}
