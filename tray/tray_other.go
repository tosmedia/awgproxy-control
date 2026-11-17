//go:build !darwin

// Package tray — значок в трее. На Windows и Linux — обёртка над fyne.io/systray,
// на macOS — своя реализация через Objective-C runtime без cgo (tray_darwin.go),
// чтобы приложение собиралось на обычном Linux-сервере GitHub.
package tray

import "fyne.io/systray"

type MenuItem struct {
	ClickedCh <-chan struct{}
	it        *systray.MenuItem
}

func wrap(it *systray.MenuItem) *MenuItem { return &MenuItem{ClickedCh: it.ClickedCh, it: it} }

func Run(onReady, onExit func()) { systray.Run(onReady, onExit) }
func Quit()                      { systray.Quit() }
func SetIcon(b []byte)           { systray.SetIcon(b) }
func SetTooltip(s string)        { systray.SetTooltip(s) }
func AddSeparator()              { systray.AddSeparator() }

func AddMenuItem(title, tooltip string) *MenuItem {
	return wrap(systray.AddMenuItem(title, tooltip))
}

func AddMenuItemCheckbox(title, tooltip string, checked bool) *MenuItem {
	return wrap(systray.AddMenuItemCheckbox(title, tooltip, checked))
}

func (m *MenuItem) AddSubMenuItem(title, tooltip string) *MenuItem {
	return wrap(m.it.AddSubMenuItem(title, tooltip))
}

func (m *MenuItem) AddSubMenuItemCheckbox(title, tooltip string, checked bool) *MenuItem {
	return wrap(m.it.AddSubMenuItemCheckbox(title, tooltip, checked))
}

func (m *MenuItem) SetTitle(s string) { m.it.SetTitle(s) }
func (m *MenuItem) Disable()          { m.it.Disable() }
func (m *MenuItem) Enable()           { m.it.Enable() }
func (m *MenuItem) Hide()             { m.it.Hide() }
func (m *MenuItem) Show()             { m.it.Show() }
func (m *MenuItem) Check()            { m.it.Check() }
func (m *MenuItem) Uncheck()          { m.it.Uncheck() }
