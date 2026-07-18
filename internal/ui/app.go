// Package ui is the TUI face of habit: one root model owning tab routing,
// an overlay LIFO stack, the toast line, and the async plumbing (fsnotify
// reload, minute tick for day rollover). Views are pure; every mutation is
// optimistic in-memory with async persistence (spec §6.1).
package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/fsnotify/fsnotify"

	"habit/internal/config"
	"habit/internal/domain"
	"habit/internal/store"
	"habit/internal/ui/theme"
	"habit/internal/ui/widgets"
)

type Tab int

const (
	TabDashboard Tab = iota
	TabAnalytics
	TabSettings
)

var tabNames = [...]string{"Dashboard", "Analytics", "Settings"}

// Overlay is a floating surface on the LIFO stack: palette, modals, help.
// Input always routes to the top of the stack first; returning a nil
// Overlay closes it.
type Overlay interface {
	Update(msg tea.Msg, a *App) (Overlay, tea.Cmd)
	View(a *App) string
}

// Messages.
type (
	snapMsg         struct{ snap *store.Snapshot }
	storeChangedMsg struct{}
	confChangedMsg  struct{}
	saveConfMsg     struct{ gen int } // debounced Settings write (§5.5)
	minuteMsg       struct{}
	toastExpiredMsg struct{ gen int }
	undoneMsg       struct {
		desc string
		err  error
		redo bool
	}
	errMsg struct{ err error }
)

type App struct {
	store   *store.Store
	snap    *store.Snapshot
	day     domain.Day // real logical today
	viewDay domain.Day // day the dashboard shows/edits; == day unless time-traveling

	tab      Tab
	dash     dashModel
	ana      anaModel
	set      setModel
	overlays []Overlay

	conf    config.Config
	theme   theme.Theme
	gl      theme.Glyphs
	border  lipgloss.Border
	keys    KeyMap
	darkBG  bool // terminal background, from BackgroundColorMsg; drives theme="auto"
	bgKnown bool // terminal answered OSC 11; gates solid paint under theme="auto"

	toastText string
	toastGen  int

	w, h    int
	tabX    [3][2]int // header x-ranges of the tab labels, for mouse
	changes <-chan struct{}
	confCh  <-chan struct{}
	mutCh   chan<- func()
	pending atomic.Int32 // queued writes; gates reloads (see storeChangedMsg)
}

// Run starts the TUI over an already-open store.
func Run(s *store.Store, cfg config.Config) error {
	changes, closeWatch, err := watchDB(s.Path())
	if err != nil {
		return err
	}
	defer closeWatch()
	// First run: the config dir must exist before it can be watched.
	if err := os.MkdirAll(filepath.Dir(config.Path()), 0o755); err != nil {
		return err
	}
	confCh, closeConf, err := watchDB(config.Path()) // same watcher shape
	if err != nil {
		return err
	}
	defer closeConf()
	// All mutations flow through one FIFO worker: concurrent tea.Cmd
	// goroutines could otherwise commit rapid keystrokes out of order.
	mutCh := make(chan func(), 64)
	defer close(mutCh)
	go func() {
		for f := range mutCh {
			f()
		}
	}()
	app := &App{
		store:   s,
		day:     s.Today(),
		viewDay: s.Today(),
		keys:    Keys,
		changes: changes,
		confCh:  confCh,
		mutCh:   mutCh,
		darkBG:  true, // assume dark until the terminal answers — matches the dark default
	}
	app.applyConfig(cfg)
	_, err = tea.NewProgram(app).Run()
	return err
}

// applyConfig makes a config (from disk, Settings, or the palette) the live
// truth: theme, glyphs, borders, and store options.
func (a *App) applyConfig(cfg config.Config) {
	a.conf = cfg
	name := cfg.Theme
	if name == "auto" { // resolved against the detected terminal background
		if a.darkBG {
			name = "tokyo-night"
		} else {
			name = "tokyo-night-day"
		}
	}
	t, err := theme.Load(name, cfg.Accent)
	if err != nil {
		t = theme.Default()
	}
	a.theme = t
	a.gl = theme.GlyphSet(cfg.Borders == "ascii" || theme.ASCIIOnly())
	a.border = theme.Border(cfg.Borders)
	a.store.SetOpt(store.Opts{
		RolloverHour:  cfg.RolloverHour,
		WeekStart:     cfg.WeekStartDay(),
		DisableFreeze: !cfg.FreezeTokens,
	})
}

// mutate queues a store mutation. The enqueue happens HERE, synchronously
// inside Update — Bubble Tea runs commands on concurrent goroutines, so
// enqueueing inside the command would race away keystroke order. The
// returned command only awaits the result; the pending counter keeps
// mid-queue snapshots from clobbering optimistic state.
func (a *App) mutate(f func(s *store.Store) error) tea.Cmd {
	s := a.store
	a.pending.Add(1)
	done := make(chan error, 1)
	a.mutCh <- func() {
		done <- f(s)
		a.pending.Add(-1)
	}
	return func() tea.Msg {
		if err := <-done; err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// watchDB emits on any write to the database files (WAL included), so a CLI
// mutation from another pane repaints the open TUI within a frame (§6.7).
func watchDB(path string) (<-chan struct{}, func(), error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, nil, err
	}
	if err := w.Add(filepath.Dir(path)); err != nil {
		w.Close()
		return nil, nil, err
	}
	ch := make(chan struct{}, 1)
	base := filepath.Base(path)
	go func() {
		for ev := range w.Events {
			if strings.HasPrefix(filepath.Base(ev.Name), base) {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
		}
	}()
	return ch, func() { w.Close() }, nil
}

// ---- commands ----

func (a *App) loadSnap() tea.Cmd {
	s, day := a.store, a.viewDay
	return func() tea.Msg {
		snap, err := s.Snapshot(day)
		if err != nil {
			return errMsg{err}
		}
		return snapMsg{snap}
	}
}

// waitChange blocks on the watcher channel, coalescing bursts (one SQLite
// commit touches db + wal). The TUI's own writes land here too — the
// resulting reload is a cheap no-op refresh. ponytail: suppress self-events
// only if profiling ever cares.
func waitChange(ch <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		<-ch
		time.Sleep(30 * time.Millisecond)
		for {
			select {
			case <-ch:
			default:
				return storeChangedMsg{}
			}
		}
	}
}

func minuteTick() tea.Cmd {
	return tea.Every(time.Minute, func(time.Time) tea.Msg { return minuteMsg{} })
}

func (a *App) rolloverCmd() tea.Cmd {
	s, day := a.store, a.viewDay
	return func() tea.Msg {
		if err := s.FinalizeThrough(s.Today()); err != nil {
			return errMsg{err}
		}
		snap, err := s.Snapshot(day)
		if err != nil {
			return errMsg{err}
		}
		return snapMsg{snap}
	}
}

// rolloverIfNeeded advances the logical day once midnight (rollover hour)
// passes — from the minute tick AND from snapMsg, so a reload that lands
// before the tick can't strand a.day ahead of viewDay with the rollover
// check permanently disarmed. Skipped while writes are queued (the reload
// would clobber optimistic state, per ui.md); the next tick retries.
func (a *App) rolloverIfNeeded() tea.Cmd {
	t := a.store.Today()
	if t == a.day || a.pending.Load() > 0 {
		return nil
	}
	if a.viewDay == a.day {
		a.viewDay = t // follow the rollover unless time-traveling
	}
	a.day = t
	return a.rolloverCmd()
}

// undoCmd goes through the same worker, enqueued in Update order — rapid
// u/ctrl+r presses must apply in keystroke order.
func (a *App) undoCmd(redo bool) tea.Cmd {
	s := a.store
	a.pending.Add(1)
	res := make(chan undoneMsg, 1)
	a.mutCh <- func() {
		var desc string
		var err error
		if redo {
			desc, err = s.Redo()
		} else {
			desc, err = s.Undo()
		}
		a.pending.Add(-1)
		res <- undoneMsg{desc, err, redo}
	}
	return func() tea.Msg { return <-res }
}

// Toast shows a transient line above the help bar for 4 s (§5.1).
func (a *App) Toast(text string) tea.Cmd {
	a.toastText = text
	a.toastGen++
	gen := a.toastGen
	return tea.Tick(4*time.Second, func(time.Time) tea.Msg { return toastExpiredMsg{gen} })
}

// ---- tea.Model ----

func (a *App) Init() tea.Cmd {
	return tea.Batch(a.loadSnap(), waitChange(a.changes), waitConf(a.confCh), minuteTick(), tea.RequestBackgroundColor)
}

// waitConf mirrors waitChange for the config file (§5.5: external edits
// hot-reload — Settings is just a friendly editor for the file).
func waitConf(ch <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		<-ch
		time.Sleep(30 * time.Millisecond)
		for {
			select {
			case <-ch:
			default:
				return confChangedMsg{}
			}
		}
	}
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.w, a.h = msg.Width, msg.Height
		return a, nil

	case tea.BackgroundColorMsg:
		a.bgKnown = true
		if dark := msg.IsDark(); dark != a.darkBG {
			a.darkBG = dark
			a.applyConfig(a.conf)
		}
		return a, nil

	case snapMsg:
		if msg.snap.Day != a.viewDay {
			return a, nil // stale: the user shifted days again mid-flight
		}
		a.snap = msg.snap
		a.dash.rebuild(a)
		return a, a.rolloverIfNeeded()

	case storeChangedMsg:
		if a.pending.Load() > 0 {
			// Writes still queued: this snapshot would be stale. The event
			// after the final commit triggers the clean reload.
			return a, waitChange(a.changes)
		}
		cmds := []tea.Cmd{a.loadSnap(), waitChange(a.changes)}
		if a.ana.loadedYear != 0 {
			a.ana.loadedYear = 0 // stale; reload lazily
			if a.tab == TabAnalytics {
				cmds = append(cmds, a.ana.ensure(a))
			}
		}
		return a, tea.Batch(cmds...)

	case anaMsg:
		a.ana.habits, a.ana.entries, a.ana.loadedYear = msg.habits, msg.entries, msg.year
		if a.ana.pendingDetail {
			a.ana.pendingDetail = false
			a.overlays = append(a.overlays, newDayDetail(a, a.ana.selDay))
		}
		return a, nil

	case confChangedMsg:
		if cfg, err := config.Load(); err == nil {
			a.applyConfig(cfg)
		}
		return a, tea.Batch(waitConf(a.confCh), a.loadSnap())

	case saveConfMsg:
		if msg.gen != a.set.saveGen {
			return a, nil // superseded by a newer change
		}
		cfg := a.conf
		return a, func() tea.Msg {
			if err := cfg.Save(); err != nil {
				return errMsg{err}
			}
			return nil
		}

	case minuteMsg:
		return a, tea.Batch(a.rolloverIfNeeded(), minuteTick())

	case toastExpiredMsg:
		if msg.gen == a.toastGen {
			a.toastText = ""
		}
		return a, nil

	case toastMsg:
		return a, a.Toast(msg.text)

	case undoneMsg:
		switch {
		case errors.Is(msg.err, store.ErrNothingToUndo), errors.Is(msg.err, store.ErrNothingToRedo):
			return a, a.Toast(msg.err.Error())
		case msg.err != nil:
			return a, a.Toast(a.theme.Danger.Render("error: " + msg.err.Error()))
		default:
			verb := "undid"
			if msg.redo {
				verb = "redid"
			}
			return a, tea.Batch(a.Toast(fmt.Sprintf("%s: %s · %s", verb, msg.desc, a.theme.Dim.Render("ctrl+r redo"))), a.loadSnap())
		}

	case errMsg:
		return a, a.Toast(a.theme.Danger.Render("error: " + msg.err.Error()))

	case tea.KeyPressMsg:
		return a.handleKey(msg)

	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft && len(a.overlays) == 0 {
			return a, a.handleClick(msg.Mouse().X, msg.Mouse().Y)
		}
		return a, nil
	}

	// Anything else goes to the top overlay, if one is open.
	if n := len(a.overlays); n > 0 {
		ov, cmd := a.overlays[n-1].Update(msg, a)
		if ov == nil {
			a.overlays = a.overlays[:n-1]
		} else {
			a.overlays[n-1] = ov
		}
		return a, cmd
	}
	return a, nil
}

func (a *App) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Overlays swallow input first (LIFO).
	if n := len(a.overlays); n > 0 {
		ov, cmd := a.overlays[n-1].Update(msg, a)
		if ov == nil {
			a.overlays = a.overlays[:n-1]
		} else {
			a.overlays[n-1] = ov
		}
		return a, cmd
	}

	k := a.keys
	switch {
	case key.Matches(msg, k.Quit):
		return a, tea.Quit
	case key.Matches(msg, k.Tab1):
		a.tab = TabDashboard
	case key.Matches(msg, k.Tab2):
		a.tab = TabAnalytics
		return a, a.ana.ensure(a)
	case key.Matches(msg, k.Tab3):
		a.tab = TabSettings
	case key.Matches(msg, k.NextTab):
		a.tab = (a.tab + 1) % 3
		if a.tab == TabAnalytics {
			return a, a.ana.ensure(a)
		}
	case key.Matches(msg, k.PrevTab):
		a.tab = (a.tab + 2) % 3
		if a.tab == TabAnalytics {
			return a, a.ana.ensure(a)
		}
	case key.Matches(msg, k.Help):
		a.overlays = append(a.overlays, helpOverlay{})
	case key.Matches(msg, k.Undo):
		return a, a.undoCmd(false)
	case key.Matches(msg, k.Redo):
		return a, a.undoCmd(true)
	case key.Matches(msg, k.Palette):
		a.overlays = append(a.overlays, newPalette(a))
	default:
		switch a.tab {
		case TabDashboard:
			return a, a.dash.handleKey(msg, a)
		case TabAnalytics:
			return a, a.ana.handleKey(msg, a)
		case TabSettings:
			return a, a.set.handleKey(msg, a)
		}
	}
	return a, nil
}

// handleClick: tabs on the header row; dashboard rows select, a click on
// the glyph cell toggles (§4.2 — mouse never advertised, always accepted).
func (a *App) handleClick(x, y int) tea.Cmd {
	if y == 0 {
		for i, r := range a.tabX {
			if x >= r[0] && x < r[1] {
				a.tab = Tab(i)
				return nil
			}
		}
		return nil
	}
	if a.tab == TabAnalytics {
		return a.ana.click(a, x, y)
	}
	if a.tab != TabDashboard {
		return nil
	}
	d := &a.dash
	if v, ok := d.rowLines[y-2]; ok { // body starts under the 2-line header
		d.selG, d.selR = v[0], v[1]
		if v[1] == -1 { // group header: click toggles collapse
			gid := d.groups[v[0]].g.ID
			if d.collapsed[gid] {
				delete(d.collapsed, gid)
				d.selR = 0
			} else {
				d.collapsed[gid] = true
			}
			return nil
		}
		if x >= 5 && x <= 6 { // status glyph cell
			return d.toggle(a)
		}
	}
	return nil
}

func (a *App) View() tea.View {
	content := a.render()
	for _, ov := range a.overlays {
		content = widgets.Compose(content, ov.View(a), a.w, a.h, a.theme.Faint)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	// Solid mode redefines the terminal's default bg AND fg: unstyled runs
	// (and text after an embedded SGR reset) must land on theme colors, not
	// whatever the terminal profile happens to use. Under theme="auto" wait
	// for the OSC 11 answer first — painting rewrites the very color the
	// detection queries, which would lock auto to the startup guess.
	if a.conf.Background == "solid" && (a.conf.Theme != "auto" || a.bgKnown) {
		v.BackgroundColor = a.theme.Bg
		v.ForegroundColor = a.theme.Text.GetForeground()
	}
	return v
}

// ---- rendering ----

func (a *App) render() string {
	if a.w == 0 {
		return ""
	}
	header := a.headerView()
	footer := a.footerView()
	bodyH := a.h - lipgloss.Height(header) - lipgloss.Height(footer)
	if bodyH < 0 {
		bodyH = 0
	}
	body := lipgloss.NewStyle().Height(bodyH).MaxHeight(bodyH).Render(a.tabView())
	return header + "\n" + body + "\n" + footer
}

// headerView renders the product mark, tabs with the accent underline on
// the active tab, and the logical date — one line, no border (§5.1).
func (a *App) headerView() string {
	th, gl := a.theme, a.gl
	row := "  " + th.Accent.Render(gl.Logo) + th.Text.Render(" habit") + "          "
	underline := strings.Repeat(" ", lipgloss.Width(row))
	for i, name := range tabNames {
		a.tabX[i] = [2]int{lipgloss.Width(row), lipgloss.Width(row) + len(name)}
		if Tab(i) == a.tab {
			row += th.Text.Render(name)
			underline += th.Accent.Render(strings.Repeat(gl.TabRule, lipgloss.Width(name)))
		} else {
			row += th.Dim.Render(name)
			underline += strings.Repeat(" ", lipgloss.Width(name))
		}
		if i < len(tabNames)-1 {
			row += "    "
			underline += "    "
		}
	}
	date := a.day.Time().Format("Mon · Jan 2")
	pad := a.w - lipgloss.Width(row) - lipgloss.Width(date) - 2
	if pad > 0 {
		row += strings.Repeat(" ", pad)
	}
	return row + th.Dim.Render(date) + "\n" + underline
}

func (a *App) footerView() string {
	th := a.theme
	toast := " " + a.toastText
	rule := th.Faint.Render(strings.Repeat(a.gl.HRule, max(a.w-1, 0)))
	return toast + "\n" + rule + "\n " + a.helpLine()
}

// helpLine renders the contextual help bar from the keymap (§5.1).
func (a *App) helpLine() string {
	k := a.keys
	var bs []key.Binding
	switch a.tab {
	case TabDashboard:
		bs = []key.Binding{k.Toggle, k.Inc, k.Skip, k.New, k.Palette, k.Undo, k.Help, k.Quit}
	case TabAnalytics:
		bs = []key.Binding{k.PrevHabit, k.PrevYear, k.Palette, k.Help, k.Quit}
	default:
		bs = []key.Binding{k.Down, k.Right, k.Toggle, k.OpenConfig, k.Help, k.Quit}
	}
	parts := make([]string, len(bs))
	for i, b := range bs {
		parts[i] = b.Help().Key + " " + b.Help().Desc
	}
	return a.theme.Dim.Render(strings.Join(parts, " · "))
}

// tabView renders the active tab. Dashboard/Analytics/Settings arrive in
// M5/M7/M8; the placeholders prove data loading and tab routing.
func (a *App) tabView() string {
	th := a.theme
	switch a.tab {
	case TabDashboard:
		if a.snap == nil {
			return "\n   " + th.Dim.Render("loading…")
		}
		return a.dash.view(a)
	case TabAnalytics:
		return a.ana.view(a)
	default:
		return a.set.view(a)
	}
}

// ---- help overlay (full keymap, generated from the single source) ----

type helpOverlay struct{}

func (helpOverlay) Update(msg tea.Msg, a *App) (Overlay, tea.Cmd) {
	if kp, ok := msg.(tea.KeyPressMsg); ok {
		if key.Matches(kp, a.keys.Esc, a.keys.Help, a.keys.Quit) {
			return nil, nil
		}
	}
	return helpOverlay{}, nil
}

func (helpOverlay) View(a *App) string {
	th := a.theme
	section := func(sec helpSection) string {
		var b strings.Builder
		b.WriteString(th.Accent.Render(strings.ToUpper(sec.Title)) + "\n")
		for _, bind := range sec.Keys {
			b.WriteString(fmt.Sprintf("  %s  %s\n",
				th.Text.Render(fmt.Sprintf("%-9s", bind.Help().Key)),
				th.Dim.Render(bind.Help().Desc)))
		}
		return strings.TrimRight(b.String(), "\n")
	}
	secs := helpSections()
	// Two columns so the full keymap fits an 80×24 screen.
	left := section(secs[1])
	right := section(secs[0]) + "\n\n" + section(secs[2])
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, "    ", right)
	box := lipgloss.NewStyle().
		Border(a.border).BorderForeground(th.AccentColor).
		Padding(0, 2).Render(body)
	return widgets.Shadowed(box, a.gl.Shadow, th.Faint)
}
