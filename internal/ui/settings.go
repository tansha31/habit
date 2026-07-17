package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"habit/internal/config"
	"habit/internal/store"
	"habit/internal/ui/theme"
	"habit/internal/ui/widgets"
)

// setModel is the Settings tab (§5.5): a friendly editor for config.toml.
// Every change applies on the same frame and writes the file after a 250 ms
// debounce; external edits hot-reload through the config watcher.
type setModel struct {
	sel     int
	saveGen int
}

type setItem struct {
	section string // non-empty on the first item of a section
	label   string
	render  func(a *App) string
	change  func(a *App, dir int) tea.Cmd // nil = display-only row
	note    string
}

// scheduleSave debounces the config write (§5.5: 250 ms).
func (m *setModel) scheduleSave() tea.Cmd {
	m.saveGen++
	gen := m.saveGen
	return tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg { return saveConfMsg{gen} })
}

// changed applies the mutated config and schedules the write.
func (m *setModel) changed(a *App, cfg config.Config) tea.Cmd {
	a.applyConfig(cfg)
	return m.scheduleSave()
}

var accentPresets = []string{"", "#7aa2f7", "#bb9af7", "#9ece6a", "#f7768e", "#e0af68", "#7dcfff", "#fe8019"}

func cycle[T comparable](list []T, cur T, dir int) T {
	for i, v := range list {
		if v == cur {
			return list[(i+dir+len(list))%len(list)]
		}
	}
	return list[0]
}

func onOff(a *App, v bool) string {
	if v {
		return a.theme.Text.Render("(•) on")
	}
	return a.theme.Dim.Render("( ) off")
}

func (m *setModel) items(a *App) []setItem {
	th := a.theme
	cyc := func(s string) string { return th.Dim.Render("‹ ") + th.Text.Render(s) + th.Dim.Render(" ›") }
	toggleBool := func(get func(*config.Config) *bool) func(*App, int) tea.Cmd {
		return func(a *App, _ int) tea.Cmd {
			cfg := a.conf
			p := get(&cfg)
			*p = !*p
			return m.changed(a, cfg)
		}
	}
	return []setItem{
		{section: "APPEARANCE", label: "theme",
			render: func(a *App) string {
				if a.conf.Theme == "auto" {
					return cyc("auto · " + a.theme.Name)
				}
				return cyc(a.conf.Theme)
			},
			change: func(a *App, dir int) tea.Cmd {
				cfg := a.conf
				cfg.Theme = cycle(append([]string{"auto"}, theme.Names()...), cfg.Theme, dir)
				return m.changed(a, cfg)
			}},
		{label: "accent",
			render: func(a *App) string {
				if a.conf.Accent == "" {
					return cyc("theme default")
				}
				return cyc(a.theme.Accent.Render("▓ ") + a.conf.Accent)
			},
			change: func(a *App, dir int) tea.Cmd {
				cfg := a.conf
				cfg.Accent = cycle(accentPresets, cfg.Accent, dir)
				return m.changed(a, cfg)
			}},
		{label: "background",
			render: func(a *App) string {
				if a.conf.Background == "solid" {
					return th.Dim.Render("( ) terminal  ") + th.Text.Render("(•) solid")
				}
				return th.Text.Render("(•) terminal  ") + th.Dim.Render("( ) solid")
			},
			change: func(a *App, _ int) tea.Cmd {
				cfg := a.conf
				if cfg.Background == "solid" {
					cfg.Background = "terminal"
				} else {
					cfg.Background = "solid"
				}
				return m.changed(a, cfg)
			}},
		{label: "borders",
			render: func(a *App) string { return cyc(a.conf.Borders) },
			change: func(a *App, dir int) tea.Cmd {
				cfg := a.conf
				cfg.Borders = cycle([]string{"rounded", "square", "ascii"}, cfg.Borders, dir)
				return m.changed(a, cfg)
			}},

		{section: "BEHAVIOR", label: "day rollover",
			render: func(a *App) string { return cyc(fmt.Sprintf("%02d:00", a.conf.RolloverHour)) },
			change: func(a *App, dir int) tea.Cmd {
				cfg := a.conf
				cfg.RolloverHour = (cfg.RolloverHour + dir + 24) % 24
				return tea.Batch(m.changed(a, cfg), a.loadSnap())
			}},
		{label: "week starts",
			render: func(a *App) string { return cyc(strings.Title(a.conf.WeekStart)) },
			change: func(a *App, _ int) tea.Cmd {
				cfg := a.conf
				if cfg.WeekStart == "sunday" {
					cfg.WeekStart = "monday"
				} else {
					cfg.WeekStart = "sunday"
				}
				return tea.Batch(m.changed(a, cfg), a.loadSnap())
			}},
		{label: "freeze tokens", note: "earn 1 / 10 completions · cap 3",
			render: func(a *App) string { return onOff(a, a.conf.FreezeTokens) },
			change: toggleBool(func(c *config.Config) *bool { return &c.FreezeTokens })},
		{label: "milestone marks",
			render: func(a *App) string { return onOff(a, a.conf.MilestoneMarks) },
			change: toggleBool(func(c *config.Config) *bool { return &c.MilestoneMarks })},
		{label: "at-risk nudge", note: "tint after 21:00",
			render: func(a *App) string { return onOff(a, a.conf.AtRiskNudge) },
			change: toggleBool(func(c *config.Config) *bool { return &c.AtRiskNudge })},

		{section: "NOTIFICATIONS", label: "habitd agent", note: "› habit daemon install",
			render: func(a *App) string {
				home, _ := os.UserHomeDir()
				if _, err := os.Stat(home + "/Library/LaunchAgents/com.habit.habitd.plist"); err == nil {
					return th.Ok.Render("installed")
				}
				return th.Dim.Render("not installed")
			}},
		{label: "default times",
			render: func(a *App) string {
				var parts []string
				for _, g := range a.snap.Groups {
					if g.Reminder != "" {
						parts = append(parts, g.Name+" "+g.Reminder)
					}
				}
				return th.Dim.Render(strings.Join(parts, " · "))
			}},

		{section: "DATA", label: "database",
			render: func(a *App) string {
				size := ""
				if fi, err := os.Stat(a.store.Path()); err == nil {
					size = fmt.Sprintf("   %.1f MB", float64(fi.Size())/1e6)
				}
				return th.Dim.Render(shortenHome(a.store.Path()) + size)
			}},
		{label: "config", note: "o open in $EDITOR",
			render: func(a *App) string { return th.Dim.Render(shortenHome(config.Path())) }},
		{label: "export",
			render: func(a *App) string {
				return th.Dim.Render("› json   › csv") + "   " + th.Faint.Render("space runs json, l runs csv")
			},
			change: func(a *App, dir int) tea.Cmd {
				if dir > 0 {
					return exportCmd(a, "csv")
				}
				return exportCmd(a, "json")
			}},
		{label: "reset data", note: "logs or everything",
			render: func(a *App) string { return th.Danger.Render("› reset…") },
			change: func(a *App, _ int) tea.Cmd {
				a.overlays = append(a.overlays, &resetOverlay{})
				return nil // the overlay drives the confirm + wipe
			}},
	}
}

func shortenHome(p string) string {
	if home, err := os.UserHomeDir(); err == nil {
		return strings.Replace(p, home, "~", 1)
	}
	return p
}

func (m *setModel) selectable(items []setItem) []int {
	var out []int
	for i := range items {
		if items[i].change != nil {
			out = append(out, i)
		}
	}
	return out
}

func (m *setModel) handleKey(msg tea.KeyPressMsg, a *App) tea.Cmd {
	items := m.items(a)
	sel := m.selectable(items)
	pos := 0
	for i, s := range sel {
		if s == m.sel {
			pos = i
		}
	}
	k := a.keys
	switch {
	case key.Matches(msg, k.Down):
		m.sel = sel[min(pos+1, len(sel)-1)]
	case key.Matches(msg, k.Up):
		m.sel = sel[max(pos-1, 0)]
	case key.Matches(msg, k.Right):
		return items[m.sel].change(a, 1)
	case key.Matches(msg, k.Left):
		return items[m.sel].change(a, -1)
	case key.Matches(msg, k.Toggle):
		return items[m.sel].change(a, 1)
	case key.Matches(msg, k.OpenConfig):
		return openInEditor(a)
	}
	return nil
}

// openInEditor suspends the TUI for $EDITOR, else hands the file to `open`.
func openInEditor(a *App) tea.Cmd {
	// Make sure the file exists before an editor opens it.
	if _, err := os.Stat(config.Path()); err != nil {
		a.conf.Save()
	}
	if ed := os.Getenv("EDITOR"); ed != "" {
		c := exec.Command(ed, config.Path())
		return tea.ExecProcess(c, func(err error) tea.Msg {
			if err != nil {
				return errMsg{err}
			}
			return confChangedMsg{}
		})
	}
	c := exec.Command("open", "-t", config.Path())
	return func() tea.Msg {
		if err := c.Run(); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

func (m *setModel) view(a *App) string {
	th := a.theme
	items := m.items(a)
	if sel := m.selectable(items); len(sel) > 0 {
		found := false
		for _, s := range sel {
			found = found || s == m.sel
		}
		if !found {
			m.sel = sel[0]
		}
	}

	padTo := func(s string, w int) string {
		if p := w - lipgloss.Width(s); p > 0 {
			return s + strings.Repeat(" ", p)
		}
		return s
	}

	var lines []string
	for i, it := range items {
		if it.section != "" {
			if i > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, "   "+th.Dim.Render(it.section))
		}
		bar := " "
		labelStyle := th.Dim
		if i == m.sel && it.change != nil {
			bar = th.Accent.Render(a.gl.SelBar)
			labelStyle = th.Text
		}
		row := fmt.Sprintf("   %s %s%s", bar, padTo(labelStyle.Render(it.label), 16), it.render(a))
		if it.note != "" {
			row = padTo(row, 58) + th.Faint.Render(it.note)
		}
		lines = append(lines, row)
	}

	// Live theme preview card beside APPEARANCE (§5.5).
	preview := m.previewCard(a)
	for i, pl := range preview {
		if 1+i < len(lines) {
			lines[1+i] = padTo(lines[1+i], a.w-lipgloss.Width(pl)-3) + pl
		}
	}
	return "\n" + strings.Join(lines, "\n")
}

// ---- reset-data confirm (Settings → DATA) ----

// resetOverlay is the two-step guard for the one-way data wipe: stage 0 picks
// the scope, stage 1 confirms. This confirm is the deliberate exception to the
// "everything is undoable" invariant — the wipe clears the undo journal itself,
// so it can't be undone (see store.Reset).
type resetOverlay struct {
	stage int // 0 = pick scope, 1 = confirm
	mode  store.ResetMode
}

func (o *resetOverlay) Update(msg tea.Msg, a *App) (Overlay, tea.Cmd) {
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return o, nil
	}
	if key.Matches(kp, a.keys.Esc) {
		return nil, nil
	}
	if o.stage == 0 {
		switch kp.String() {
		case "l":
			o.mode, o.stage = store.ResetLogs, 1
		case "d":
			o.mode, o.stage = store.ResetAll, 1
		}
		return o, nil
	}
	if kp.String() == "y" {
		mode := o.mode
		toast := "logged data cleared"
		if mode == store.ResetAll {
			toast = "all data deleted"
		}
		return nil, tea.Batch(
			a.mutate(func(s *store.Store) error { return s.Reset(mode) }),
			a.Toast(toast),
		)
	}
	return o, nil
}

func (o *resetOverlay) View(a *App) string {
	th := a.theme
	var b strings.Builder
	if o.stage == 0 {
		b.WriteString(th.Text.Render("Reset data") + "\n\n")
		b.WriteString(fmt.Sprintf("  %s  %s\n", th.Accent.Render("l"), th.Dim.Render("clear logged data (keep habits)")))
		b.WriteString(fmt.Sprintf("  %s  %s\n", th.Accent.Render("d"), th.Dim.Render("delete everything (habits + logs)")))
		b.WriteString("\n" + th.Faint.Render("esc cancel"))
	} else {
		what := "all logged data"
		if o.mode == store.ResetAll {
			what = "ALL habits and logs"
		}
		b.WriteString(th.Danger.Render("Delete "+what+"?") + "\n\n")
		b.WriteString(th.Dim.Render("This cannot be undone.") + "\n\n")
		b.WriteString(fmt.Sprintf("  %s confirm   %s", th.Danger.Render("y"), th.Faint.Render("esc cancel")))
	}
	box := lipgloss.NewStyle().
		Border(a.border).BorderForeground(th.AccentColor).
		Padding(0, 2).Render(b.String())
	return widgets.Shadowed(box, a.gl.Shadow, th.Faint)
}

func (m *setModel) previewCard(a *App) []string {
	th, gl := a.theme, a.gl
	rows := []string{
		fmt.Sprintf("  %s  Meditate    %s 47d", th.Ok.Render(gl.Done), th.Accent.Render(gl.Milestone)),
		fmt.Sprintf("  %s  Read  12 / 20 min", th.Warn.Render(gl.Partial)),
		"  " + th.Accent.Render(widgets.Bar(0.67, 7, gl.BarOn, gl.BarOff)) + "  67%",
	}
	box := lipgloss.NewStyle().
		Border(a.border).BorderForeground(th.AccentColor).
		Padding(0, 1).Render(strings.Join(rows, "\n"))
	lines := strings.Split(box, "\n")
	lines[0] = widgets.StampTitle(lines[0], th.Dim.Render(" preview "), 2)
	return lines
}
