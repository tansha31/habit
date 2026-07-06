// Package ui is the TUI face of habit: one root model owning tab routing,
// an overlay LIFO stack, the toast line, and the async plumbing (fsnotify
// reload, minute tick for day rollover). Views are pure; every mutation is
// optimistic in-memory with async persistence (spec §6.1).
package ui

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
	"github.com/fsnotify/fsnotify"

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
	store *store.Store
	snap  *store.Snapshot
	day   domain.Day

	tab      Tab
	dash     dashModel
	overlays []Overlay

	theme  theme.Theme
	gl     theme.Glyphs
	border lipgloss.Border
	keys   KeyMap

	toastText string
	toastGen  int

	w, h    int
	tabX    [3][2]int  // header x-ranges of the tab labels, for mouse
	gotoDay domain.Day // day-detail target from @date palette (M7 consumes)
	changes <-chan struct{}
	mutCh   chan<- func()
}

// Run starts the TUI over an already-open store.
func Run(s *store.Store) error {
	changes, closeWatch, err := watchDB(s.Path())
	if err != nil {
		return err
	}
	defer closeWatch()
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
		theme:   theme.Default(),
		gl:      theme.GlyphSet(theme.ASCIIOnly()),
		border:  theme.Border("rounded"),
		keys:    Keys,
		changes: changes,
		mutCh:   mutCh,
	}
	_, err = tea.NewProgram(app).Run()
	return err
}

// mutate queues a store mutation in keystroke order; errors surface as a
// toast and the fsnotify reload restores the model to DB truth.
func (a *App) mutate(f func(s *store.Store) error) tea.Cmd {
	s, ch := a.store, a.mutCh
	return func() tea.Msg {
		done := make(chan error, 1)
		ch <- func() { done <- f(s) }
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
	s := a.store
	return func() tea.Msg {
		snap, err := s.Snapshot()
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
	s := a.store
	return func() tea.Msg {
		if err := s.FinalizeThrough(s.Today()); err != nil {
			return errMsg{err}
		}
		snap, err := s.Snapshot()
		if err != nil {
			return errMsg{err}
		}
		return snapMsg{snap}
	}
}

func (a *App) undoCmd(redo bool) tea.Cmd {
	s := a.store
	return func() tea.Msg {
		var desc string
		var err error
		if redo {
			desc, err = s.Redo()
		} else {
			desc, err = s.Undo()
		}
		return undoneMsg{desc, err, redo}
	}
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
	return tea.Batch(a.loadSnap(), waitChange(a.changes), minuteTick())
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.w, a.h = msg.Width, msg.Height
		return a, nil

	case snapMsg:
		a.snap = msg.snap
		a.day = msg.snap.Today
		a.dash.rebuild(a)
		return a, nil

	case storeChangedMsg:
		return a, tea.Batch(a.loadSnap(), waitChange(a.changes))

	case minuteMsg:
		if a.store.Today() != a.day {
			return a, tea.Batch(a.rolloverCmd(), minuteTick())
		}
		return a, minuteTick()

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
	case key.Matches(msg, k.Tab3):
		a.tab = TabSettings
	case key.Matches(msg, k.NextTab):
		a.tab = (a.tab + 1) % 3
	case key.Matches(msg, k.PrevTab):
		a.tab = (a.tab + 2) % 3
	case key.Matches(msg, k.Help):
		a.overlays = append(a.overlays, helpOverlay{})
	case key.Matches(msg, k.Undo):
		return a, a.undoCmd(false)
	case key.Matches(msg, k.Redo):
		return a, a.undoCmd(true)
	case key.Matches(msg, k.Palette):
		a.overlays = append(a.overlays, newPalette(a))
	default:
		if a.tab == TabDashboard {
			return a, a.dash.handleKey(msg, a)
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
	row := "  " + th.Accent.Render(gl.Logo) + " habit          "
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
		bs = []key.Binding{k.NextTab, k.Palette, k.Help, k.Quit}
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
		return "\n   " + th.Dim.Render("analytics arrive in M7")
	default:
		return "\n   " + th.Dim.Render("settings arrive in M8")
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
