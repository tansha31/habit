package ui

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sahilm/fuzzy"

	"habit/internal/domain"
	"habit/internal/ui/theme"
	"habit/internal/ui/widgets"
)

// paletteOverlay is the command palette (§4.3): one input, four modes by
// first character — fuzzy habit actions, > commands, @ dates, # tag filter.
type paletteOverlay struct {
	input textinput.Model
	items []palItem
	sel   int
	frec  map[string]frecEntry
}

type palItem struct {
	icon  string
	label string
	hint  string // direct keybinding, echoed on the right (§4.3)
	fkey  string // frecency key; "" = not tracked
	run   func(a *App) tea.Cmd
}

type frecEntry struct {
	Count int   `json:"c"`
	Last  int64 `json:"l"` // unix seconds
}

func newPalette(a *App) *paletteOverlay {
	p := &paletteOverlay{input: ti("", 40), frec: map[string]frecEntry{}}
	p.input.Prompt = a.theme.Accent.Render(a.gl.Prompt + " ")
	p.input.Focus()
	if raw := a.store.MetaGet("frecency"); raw != "" {
		json.Unmarshal([]byte(raw), &p.frec)
	}
	p.compute(a)
	return p
}

// score ranks an item: fuzzy relevance first, then frecency (recent +
// frequent choices float up).
func (p *paletteOverlay) score(fuzzyScore int, fkey string) float64 {
	s := float64(fuzzyScore)
	if f, ok := p.frec[fkey]; ok {
		s += float64(min(f.Count, 20)) * 3
		if time.Since(time.Unix(f.Last, 0)) < 24*time.Hour {
			s += 10
		}
	}
	return s
}

func (p *paletteOverlay) compute(a *App) {
	q := p.input.Value()
	p.sel = 0
	switch {
	case strings.HasPrefix(q, ">"):
		p.items = p.commandItems(a, strings.TrimSpace(q[1:]))
	case strings.HasPrefix(q, "@"):
		p.items = p.dateItems(a, strings.TrimSpace(q[1:]))
	case strings.HasPrefix(q, "#"):
		p.items = p.tagItems(a, strings.TrimSpace(q[1:]))
	default:
		p.items = p.habitItems(a, strings.TrimSpace(q))
	}
}

// habitItems: fuzzy over habit names; each match expands to its actions.
func (p *paletteOverlay) habitItems(a *App, q string) []palItem {
	gl := a.gl
	var habits []domain.Habit
	if q == "" {
		habits = a.snap.Habits
	} else {
		names := make([]string, len(a.snap.Habits))
		for i, h := range a.snap.Habits {
			names[i] = h.Name
		}
		for _, m := range fuzzy.Find(q, names) {
			habits = append(habits, a.snap.Habits[m.Index])
		}
	}
	var items []palItem
	for i := range habits {
		h := habits[i]
		id := h.ID
		pause, pauseIcon := "pause", gl.Pause
		if h.Paused() {
			pause, pauseIcon = "resume", gl.Done
		}
		items = append(items,
			palItem{gl.Done, "toggle   " + h.Name, "space", "toggle:" + h.Slug, func(a *App) tea.Cmd {
				if a.dash.selectHabit(id) {
					a.tab = TabDashboard
					return a.dash.toggle(a)
				}
				return nil
			}},
			palItem{gl.Edit, "edit     " + h.Name, "e", "edit:" + h.Slug, func(a *App) tea.Cmd {
				hh := h
				a.overlays = append(a.overlays, newEditor(a, &hh))
				return nil
			}},
			palItem{gl.Focus, "focus    " + h.Name + " in Dashboard", "", "focus:" + h.Slug, func(a *App) tea.Cmd {
				a.dash.selectHabit(id)
				a.tab = TabDashboard
				return nil
			}},
			palItem{pauseIcon, pause + "    " + h.Name, "p", pause + ":" + h.Slug, func(a *App) tea.Cmd {
				if a.dash.selectHabit(id) {
					a.tab = TabDashboard
					return a.dash.pause(a)
				}
				return nil
			}},
		)
	}
	// Order habit blocks by frecency when the query is empty.
	if q == "" {
		sort.SliceStable(items, func(i, j int) bool {
			return p.score(0, items[i].fkey) > p.score(0, items[j].fkey)
		})
	}
	return items
}

func (p *paletteOverlay) commandItems(a *App, q string) []palItem {
	gl := a.gl
	cmds := []palItem{
		{gl.Pending, "new habit", "n", "cmd:new", func(a *App) tea.Cmd {
			a.overlays = append(a.overlays, newEditor(a, nil))
			return nil
		}},
		{gl.Prompt, "undo", "u", "cmd:undo", func(a *App) tea.Cmd { return a.undoCmd(false) }},
		{gl.Prompt, "redo", "ctrl+r", "cmd:redo", func(a *App) tea.Cmd { return a.undoCmd(true) }},
		{gl.Prompt, "export json", "", "cmd:exportjson", func(a *App) tea.Cmd { return exportCmd(a, "json") }},
		{gl.Prompt, "export csv", "", "cmd:exportcsv", func(a *App) tea.Cmd { return exportCmd(a, "csv") }},
		{gl.Prompt, "help", "?", "cmd:help", func(a *App) tea.Cmd {
			a.overlays = append(a.overlays, helpOverlay{})
			return nil
		}},
		{gl.Prompt, "quit", "q", "cmd:quit", func(a *App) tea.Cmd { return tea.Quit }},
	}
	for _, name := range append([]string{"auto"}, theme.Names()...) {
		n := name
		cmds = append(cmds, palItem{gl.Prompt, "theme " + n, "", "cmd:theme:" + n, func(a *App) tea.Cmd {
			cfg := a.conf
			cfg.Theme = n
			a.applyConfig(cfg)
			return tea.Batch(a.Toast("theme "+n), func() tea.Msg {
				if err := cfg.Save(); err != nil {
					return errMsg{err}
				}
				return nil
			})
		}})
	}
	if q == "" {
		return cmds
	}
	labels := make([]string, len(cmds))
	for i, c := range cmds {
		labels[i] = c.label
	}
	var out []palItem
	for _, m := range fuzzy.Find(q, labels) {
		out = append(out, cmds[m.Index])
	}
	return out
}

// dateItems parses @yesterday · @jun 12 · @2026-06-12 · @-3 (§4.3).
func (p *paletteOverlay) dateItems(a *App, q string) []palItem {
	day, ok := parsePaletteDate(q, a.day)
	if !ok {
		return []palItem{{a.gl.Pending, "type a date — yesterday · jun 12 · 2026-06-12 · -3", "", "", nil}}
	}
	pretty := day.Time().Format("Mon · Jan 2 2006")
	items := []palItem{{a.gl.Focus, "open " + pretty + " in Analytics", "", "", func(a *App) tea.Cmd {
		return a.openAnalytics(day)
	}}}
	if day < a.day {
		items = append(items, palItem{a.gl.Done, "log " + pretty + " in Dashboard", "", "", func(a *App) tea.Cmd {
			a.tab = TabDashboard
			return a.dash.gotoDay(a, day)
		}})
	}
	return items
}

var palRelDay = regexp.MustCompile(`^-\d+$`)

func parsePaletteDate(q string, today domain.Day) (domain.Day, bool) {
	q = strings.ToLower(strings.TrimSpace(q))
	switch {
	case q == "":
		return "", false
	case q == "today":
		return today, true
	case q == "yesterday":
		return today.AddDays(-1), true
	case palRelDay.MatchString(q):
		n, _ := strconv.Atoi(q)
		return today.AddDays(n), true
	}
	if t, err := time.Parse("2006-01-02", q); err == nil {
		return domain.Day(t.Format("2006-01-02")), true
	}
	if t, err := time.Parse("jan 2", q); err == nil {
		d := domain.Day(fmt.Sprintf("%s-%s", today[:4], t.Format("01-02")))
		if d > today { // "jun 12" next year? assume the previous occurrence
			d = domain.Day(fmt.Sprintf("%d-%s", t.Year()+today.Time().Year()-t.Year()-1, t.Format("01-02")))
		}
		return d, true
	}
	return "", false
}

func (p *paletteOverlay) tagItems(a *App, q string) []palItem {
	seen := map[string]bool{}
	var tags []string
	for _, h := range a.snap.Habits {
		for _, t := range h.Tags {
			if !seen[t] {
				seen[t] = true
				tags = append(tags, t)
			}
		}
	}
	sort.Strings(tags)
	if q != "" {
		var filtered []string
		for _, m := range fuzzy.Find(q, tags) {
			filtered = append(filtered, m.Str)
		}
		tags = filtered
	}
	var items []palItem
	if a.dash.filterTag != "" {
		items = append(items, palItem{a.gl.Skip, "clear filter #" + a.dash.filterTag, "esc", "", func(a *App) tea.Cmd {
			a.dash.filterTag = ""
			a.dash.rebuild(a)
			return nil
		}})
	}
	for _, t := range tags {
		tag := t
		items = append(items, palItem{"#", "filter #" + tag, "", "tag:" + tag, func(a *App) tea.Cmd {
			a.dash.filterTag = tag
			a.dash.rebuild(a)
			a.tab = TabDashboard
			return a.Toast("filtered to #" + tag + " · " + a.theme.Dim.Render("esc clears"))
		}})
	}
	if len(items) == 0 {
		items = []palItem{{a.gl.Pending, "no tags yet — add them in the habit editor", "", "", nil}}
	}
	return items
}

func (p *paletteOverlay) Update(msg tea.Msg, a *App) (Overlay, tea.Cmd) {
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return p, nil
	}
	switch {
	case key.Matches(kp, a.keys.Esc):
		return nil, nil
	case kp.String() == "enter":
		if p.sel < len(p.items) && p.items[p.sel].run != nil {
			it := p.items[p.sel]
			return nil, tea.Batch(it.run(a), p.bumpFrecency(a, it.fkey))
		}
		return p, nil
	case kp.String() == "down" || kp.String() == "ctrl+j":
		if p.sel < len(p.items)-1 {
			p.sel++
		}
		return p, nil
	case kp.String() == "up" || kp.String() == "ctrl+k":
		if p.sel > 0 {
			p.sel--
		}
		return p, nil
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	p.compute(a)
	return p, cmd
}

func (p *paletteOverlay) bumpFrecency(a *App, fkey string) tea.Cmd {
	if fkey == "" {
		return nil
	}
	f := p.frec[fkey]
	f.Count++
	f.Last = time.Now().Unix()
	p.frec[fkey] = f
	raw, _ := json.Marshal(p.frec)
	s := a.store
	return func() tea.Msg {
		s.MetaSet("frecency", string(raw)) // best-effort; not journaled
		return nil
	}
}

const palMaxRows = 8

func (p *paletteOverlay) View(a *App) string {
	th, gl := a.theme, a.gl
	w := 52
	var b strings.Builder
	b.WriteString(" " + p.input.View() + "\n")
	b.WriteString(th.Faint.Render(strings.Repeat(gl.HRule, w-2)) + "\n")

	start := 0
	if p.sel >= palMaxRows {
		start = p.sel - palMaxRows + 1
	}
	end := min(start+palMaxRows, len(p.items))
	for i := start; i < end; i++ {
		it := p.items[i]
		bar := " "
		style := th.Dim
		if i == p.sel {
			bar = th.Accent.Render(gl.SelBar)
			style = th.Text
		}
		label := style.Render(ansi.Truncate(it.label, w-16, "…"))
		hint := th.Faint.Render(it.hint)
		// Rows must fit the content width (box width minus the border).
		pad := (w - 2) - 5 - lipgloss.Width(label) - lipgloss.Width(hint)
		if pad < 1 {
			pad = 1
		}
		b.WriteString(fmt.Sprintf("%s %s  %s%s%s\n", bar, style.Render(it.icon), label, strings.Repeat(" ", pad), hint))
	}
	b.WriteString("\n" + th.Faint.Render(fmt.Sprintf(" %d matches · ↑↓ select · ↵ run · esc close", len(p.items))))

	box := lipgloss.NewStyle().
		Border(a.border).BorderForeground(th.AccentColor).
		Width(w).Render(strings.TrimRight(b.String(), "\n"))
	return widgets.Shadowed(box, gl.Shadow, th.Faint)
}

// exportCmd writes a full export next to the user's Downloads (palette
// variant of `habit export`, which streams to stdout).
func exportCmd(a *App, format string) tea.Cmd {
	s := a.store
	day := a.day
	return func() tea.Msg {
		home, _ := os.UserHomeDir()
		path := filepath.Join(home, "Downloads", fmt.Sprintf("habit-export-%s.%s", day, format))
		habits, err := s.Habits(true)
		if err != nil {
			return errMsg{err}
		}
		entries, err := s.EntriesRange("0000-01-01", "9999-12-31")
		if err != nil {
			return errMsg{err}
		}
		f, err := os.Create(path)
		if err != nil {
			return errMsg{err}
		}
		defer f.Close()
		if format == "json" {
			enc := json.NewEncoder(f)
			enc.SetIndent("", "  ")
			if err := enc.Encode(map[string]any{"habits": habits, "entries": entries}); err != nil {
				return errMsg{err}
			}
		} else {
			slugs := map[int64]string{}
			for _, h := range habits {
				slugs[h.ID] = h.Slug
			}
			w := csv.NewWriter(f)
			w.Write([]string{"slug", "day", "status", "amount", "skip_reason", "note", "logged_at", "source"})
			for _, e := range entries {
				w.Write([]string{slugs[e.HabitID], string(e.Day), string(e.Status),
					fmt.Sprintf("%g", e.Amount), e.SkipReason, e.Note,
					e.LoggedAt.Format(time.RFC3339), e.Source})
			}
			w.Flush()
			if err := w.Error(); err != nil {
				return errMsg{err}
			}
		}
		return toastMsg{"exported to " + path}
	}
}

// toastMsg lets async commands raise a toast from outside Update.
type toastMsg struct{ text string }
