//go:build darwin

package tray

// Значок в строке меню macOS без cgo: AppKit вызывается через Objective-C runtime
// (github.com/ebitengine/purego/objc). Все действия с интерфейсом выполняются
// в главном потоке: функции API ставят их в очередь, а главный поток её разбирает.

import (
	"runtime"
	"sync"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

func init() { runtime.LockOSThread() } // NSApplication должен работать в главном потоке

type MenuItem struct {
	ClickedCh <-chan struct{}
	ch        chan struct{}
	id        int
	ns        objc.ID // NSMenuItem (только из главного потока)
	sub       objc.ID // подменю
}

type nsSize struct{ W, H float64 }

var (
	mu      sync.Mutex
	queue   []func()
	items   = map[int]*MenuItem{}
	nextID  = 1
	started bool

	handler, app, statusItem, rootMenu objc.ID
	onExitFn                           func()

	selCache = map[string]objc.SEL{}
	selMu    sync.Mutex
)

func sel(s string) objc.SEL {
	selMu.Lock()
	defer selMu.Unlock()
	if v, ok := selCache[s]; ok {
		return v
	}
	v := objc.RegisterName(s)
	selCache[s] = v
	return v
}

func cls(n string) objc.ID { return objc.ID(objc.GetClass(n)) }

func nsstr(s string) objc.ID { return cls("NSString").Send(sel("stringWithUTF8String:"), s) }

// dispatch — выполнить f в главном потоке.
func dispatch(f func()) {
	mu.Lock()
	queue = append(queue, f)
	ok := started
	mu.Unlock()
	if ok {
		handler.Send(sel("performSelectorOnMainThread:withObject:waitUntilDone:"), sel("tick:"), objc.ID(0), false)
	}
}

func drain() {
	for {
		mu.Lock()
		q := queue
		queue = nil
		mu.Unlock()
		if len(q) == 0 {
			return
		}
		for _, f := range q {
			f()
		}
	}
}

func Run(onReady, onExit func()) {
	if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_GLOBAL|purego.RTLD_NOW); err != nil {
		panic(err)
	}
	c, err := objc.RegisterClass("AWGTrayHandler", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
		{Cmd: sel("tick:"), Fn: func(self objc.ID, cmd objc.SEL, arg objc.ID) { drain() }},
		{Cmd: sel("clicked:"), Fn: func(self objc.ID, cmd objc.SEL, sender objc.ID) {
			tag := objc.Send[int](sender, sel("tag"))
			mu.Lock()
			it := items[tag]
			mu.Unlock()
			if it != nil {
				select {
				case it.ch <- struct{}{}:
				default:
				}
			}
		}},
	})
	if err != nil {
		panic(err)
	}
	handler = objc.ID(c).Send(sel("new"))
	app = cls("NSApplication").Send(sel("sharedApplication"))
	app.Send(sel("setActivationPolicy:"), 1) // Accessory: без значка в Dock
	statusItem = cls("NSStatusBar").Send(sel("systemStatusBar")).Send(sel("statusItemWithLength:"), float64(-1))
	statusItem.Send(sel("retain"))
	rootMenu = cls("NSMenu").Send(sel("new"))
	rootMenu.Send(sel("setAutoenablesItems:"), false)
	statusItem.Send(sel("setMenu:"), rootMenu)
	onExitFn = onExit
	mu.Lock()
	started = true
	mu.Unlock()
	drain()
	go onReady()
	app.Send(sel("run"))
}

func Quit() {
	dispatch(func() {
		if onExitFn != nil {
			onExitFn()
		}
		app.Send(sel("terminate:"), objc.ID(0))
	})
}

func SetIcon(b []byte) {
	if len(b) == 0 {
		return
	}
	data := append([]byte(nil), b...)
	dispatch(func() {
		d := cls("NSData").Send(sel("dataWithBytes:length:"), data, len(data))
		img := cls("NSImage").Send(sel("alloc")).Send(sel("initWithData:"), d)
		if img == 0 {
			return
		}
		img.Send(sel("setSize:"), nsSize{16, 16})
		btn := statusItem.Send(sel("button"))
		btn.Send(sel("setImage:"), img)
		img.Send(sel("release"))
	})
}

func SetTooltip(s string) {
	dispatch(func() { statusItem.Send(sel("button")).Send(sel("setToolTip:"), nsstr(s)) })
}

func AddSeparator() {
	dispatch(func() { rootMenu.Send(sel("addItem:"), cls("NSMenuItem").Send(sel("separatorItem"))) })
}

func newItem() *MenuItem {
	ch := make(chan struct{}, 1)
	mu.Lock()
	m := &MenuItem{ClickedCh: ch, ch: ch, id: nextID}
	items[nextID] = m
	nextID++
	mu.Unlock()
	return m
}

func (m *MenuItem) create(menu objc.ID, title, tooltip string, checked bool) {
	ni := cls("NSMenuItem").Send(sel("alloc")).Send(sel("initWithTitle:action:keyEquivalent:"),
		nsstr(title), sel("clicked:"), nsstr(""))
	ni.Send(sel("setTarget:"), handler)
	ni.Send(sel("setTag:"), m.id)
	if tooltip != "" {
		ni.Send(sel("setToolTip:"), nsstr(tooltip))
	}
	if checked {
		ni.Send(sel("setState:"), 1)
	}
	menu.Send(sel("addItem:"), ni)
	m.ns = ni
}

func AddMenuItem(title, tooltip string) *MenuItem {
	m := newItem()
	dispatch(func() { m.create(rootMenu, title, tooltip, false) })
	return m
}

func AddMenuItemCheckbox(title, tooltip string, checked bool) *MenuItem {
	m := newItem()
	dispatch(func() { m.create(rootMenu, title, tooltip, checked) })
	return m
}

func (p *MenuItem) subMenu() objc.ID {
	if p.sub == 0 {
		p.sub = cls("NSMenu").Send(sel("new"))
		p.sub.Send(sel("setAutoenablesItems:"), false)
		p.ns.Send(sel("setSubmenu:"), p.sub)
	}
	return p.sub
}

func (p *MenuItem) AddSubMenuItem(title, tooltip string) *MenuItem {
	m := newItem()
	dispatch(func() { m.create(p.subMenu(), title, tooltip, false) })
	return m
}

func (p *MenuItem) AddSubMenuItemCheckbox(title, tooltip string, checked bool) *MenuItem {
	m := newItem()
	dispatch(func() { m.create(p.subMenu(), title, tooltip, checked) })
	return m
}

func (m *MenuItem) SetTitle(s string) { dispatch(func() { m.ns.Send(sel("setTitle:"), nsstr(s)) }) }
func (m *MenuItem) Disable()          { dispatch(func() { m.ns.Send(sel("setEnabled:"), false) }) }
func (m *MenuItem) Enable()           { dispatch(func() { m.ns.Send(sel("setEnabled:"), true) }) }
func (m *MenuItem) Hide()             { dispatch(func() { m.ns.Send(sel("setHidden:"), true) }) }
func (m *MenuItem) Show()             { dispatch(func() { m.ns.Send(sel("setHidden:"), false) }) }
func (m *MenuItem) Check()            { dispatch(func() { m.ns.Send(sel("setState:"), 1) }) }
func (m *MenuItem) Uncheck()          { dispatch(func() { m.ns.Send(sel("setState:"), 0) }) }
