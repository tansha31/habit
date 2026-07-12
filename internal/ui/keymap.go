package ui

import "charm.land/bubbles/v2/key"

// KeyMap is the single source of truth for bindings (spec §4.2, §11): the
// help bar, the ? overlay, and palette hints all render from here.
type KeyMap struct {
	// Global
	Tab1, Tab2, Tab3 key.Binding
	NextTab, PrevTab key.Binding
	Palette          key.Binding
	Undo, Redo       key.Binding
	Help             key.Binding
	Quit             key.Binding
	Esc              key.Binding

	// Dashboard
	Up, Down, Left, Right key.Binding
	Toggle                key.Binding
	Inc, Dec              key.Binding
	Skip                  key.Binding
	New, Edit             key.Binding
	Archive               key.Binding // dd — double-tap handled in the tab
	MoveUp, MoveDown      key.Binding
	Pause                 key.Binding
	Top, Bottom           key.Binding // gg / G
	PrevDay, NextDay      key.Binding // [ / ] — time travel (backfill)
	Today                 key.Binding

	// Analytics
	PrevHabit, NextHabit key.Binding
	PrevYear, NextYear   key.Binding

	// Settings
	OpenConfig key.Binding
}

func b(help, desc string, keys ...string) key.Binding {
	return key.NewBinding(key.WithKeys(keys...), key.WithHelp(help, desc))
}

var Keys = KeyMap{
	Tab1:    b("1", "dashboard", "1"),
	Tab2:    b("2", "analytics", "2"),
	Tab3:    b("3", "settings", "3"),
	NextTab: b("tab", "next tab", "tab"),
	PrevTab: b("shift+tab", "prev tab", "shift+tab"),
	Palette: b("/", "palette", "/", "ctrl+p"),
	Undo:    b("u", "undo", "u"),
	Redo:    b("ctrl+r", "redo", "ctrl+r"),
	Help:    b("?", "help", "?"),
	Quit:    b("q", "quit", "q", "ctrl+c"),
	Esc:     b("esc", "close", "esc"),

	Up:       b("k", "up", "k", "up"),
	Down:     b("j", "down", "j", "down"),
	Left:     b("h", "collapse", "h", "left"),
	Right:    b("l", "expand", "l", "right"),
	Toggle:   b("space", "done", "space", "enter"),
	Inc:      b("+", "more", "+", "="),
	Dec:      b("-", "less", "-", "_"),
	Skip:     b("s", "skip", "s"),
	New:      b("n", "new", "n"),
	Edit:     b("e", "edit", "e"),
	Archive:  b("dd", "archive", "d"),
	MoveUp:   b("K", "move up", "K", "shift+up"),
	MoveDown: b("J", "move down", "J", "shift+down"),
	Pause:    b("p", "pause", "p"),
	Top:      b("gg", "first", "g"),
	Bottom:   b("G", "last", "G"),
	PrevDay:  b("[", "prev day", "["),
	NextDay:  b("]", "next day", "]"),
	Today:    b("t", "back to today", "t"),

	PrevHabit: b("[", "prev habit", "["),
	NextHabit: b("]", "next habit", "]"),
	PrevYear:  b("y", "prev year", "y"),
	NextYear:  b("Y", "next year", "Y"),

	OpenConfig: b("o", "open config", "o"),
}

// helpSection is one block of the ? overlay.
type helpSection struct {
	Title string
	Keys  []key.Binding
}

func helpSections() []helpSection {
	k := Keys
	return []helpSection{
		{"Global", []key.Binding{k.Tab1, k.Tab2, k.Tab3, k.NextTab, k.Palette, k.Undo, k.Redo, k.Help, k.Quit}},
		{"Dashboard", []key.Binding{k.Up, k.Down, k.Left, k.Right, k.Toggle, k.Inc, k.Dec, k.Skip, k.New, k.Edit, k.Archive, k.MoveUp, k.MoveDown, k.Pause, k.Top, k.Bottom, k.PrevDay, k.NextDay, k.Today}},
		{"Analytics", []key.Binding{k.PrevHabit, k.NextHabit, k.Left, k.Right, k.PrevYear, k.NextYear, k.Toggle}},
	}
}
