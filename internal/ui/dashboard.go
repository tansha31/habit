package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"habit/internal/domain"
	"habit/internal/store"
	"habit/internal/ui/widgets"
)

// dashRow is one habit's presentation state today. Optimistic mutations
// edit it in place; the fsnotify reload replaces it with DB truth moments
// later (spec §6.1).
type dashRow struct {
	h        domain.Habit
	entry    *domain.Entry // today's entry, nil = pending
	spark    []float64     // last 14 days, oldest first
	weekDone int
	streak   domain.Streak
}

func (r *dashRow) status() domain.Status {
	if r.entry != nil {
		return r.entry.Status
	}
	return ""
}

func (r *dashRow) resolved() bool { // nothing left to do today
	switch r.status() {
	case domain.StatusDone, domain.StatusSkip, domain.StatusFreeze:
		return true
	}
	return r.h.Paused()
}

type dashGroup struct {
	g    domain.Group
	rows []dashRow
}

func (g *dashGroup) doneCount() int {
	n := 0
	for i := range g.rows {
		if g.rows[i].status() == domain.StatusDone {
			n++
		}
	}
	return n
}

type dashModel struct {
	groups     []dashGroup
	collapsed  map[int64]bool
	selG, selR int
	pending    rune           // half of a dd / gg chord
	booted     bool           // launch focus + auto-collapse applied once
	filterTag  string         // # palette filter; esc clears (§4.3)
	rowLines   map[int][2]int // render line → (group, row) for mouse
}

// rebuild derives the dashboard from a fresh snapshot, preserving collapse
// state and selection (by habit ID) across reloads.
func (d *dashModel) rebuild(a *App) {
	snap := a.snap
	if d.collapsed == nil {
		d.collapsed = map[int64]bool{}
	}
	var prevSel int64
	if r := d.selected(); r != nil {
		prevSel = r.h.ID
	}

	today := snap.Day
	weekStart := today.WeekStart(a.store.Opt().WeekStart)
	sparkFrom := today.AddDays(-13)

	byHabit := map[int64][]domain.Entry{}
	for _, e := range snap.Entries {
		byHabit[e.HabitID] = append(byHabit[e.HabitID], e)
	}

	d.groups = d.groups[:0]
	for _, g := range snap.Groups {
		dg := dashGroup{g: g}
		for _, h := range snap.Habits {
			if h.GroupID != g.ID || (d.filterTag != "" && !hasTag(h, d.filterTag)) ||
				!h.ActiveOn(today) { // time travel: hide habits born later
				continue
			}
			r := dashRow{h: h, streak: snap.Streaks[h.ID], spark: make([]float64, 14)}
			for i := range byHabit[h.ID] {
				e := &byHabit[h.ID][i]
				if e.Day == today {
					r.entry = e
				}
				if e.Status == domain.StatusDone && e.Day >= weekStart {
					r.weekDone++
				}
				if idx := domain.DaysBetween(sparkFrom, e.Day); idx >= 0 && idx < 14 {
					r.spark[idx] = domain.EntryScore(h, *e)
				}
			}
			dg.rows = append(dg.rows, r)
		}
		if len(dg.rows) > 0 {
			d.groups = append(d.groups, dg)
		}
	}

	if !d.booted && len(d.groups) > 0 {
		d.booted = true
		for _, g := range d.groups {
			if g.doneCount() == len(g.rows) {
				d.collapsed[g.g.ID] = true
			}
		}
		d.focusLaunch()
		return
	}
	// Restore selection by habit ID, else clamp to a visible row.
	if prevSel != 0 {
		for gi := range d.groups {
			for ri := range d.groups[gi].rows {
				if d.groups[gi].rows[ri].h.ID == prevSel {
					d.selG, d.selR = gi, ri
					return
				}
			}
		}
	}
	d.clampSelection()
}

func hasTag(h domain.Habit, tag string) bool {
	for _, t := range h.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// selectHabit moves the selection to a habit by ID, expanding its group.
func (d *dashModel) selectHabit(id int64) bool {
	for gi := range d.groups {
		for ri := range d.groups[gi].rows {
			if d.groups[gi].rows[ri].h.ID == id {
				delete(d.collapsed, d.groups[gi].g.ID)
				d.selG, d.selR = gi, ri
				return true
			}
		}
	}
	return false
}

// focusLaunch: current time-of-day group, first incomplete habit (§1.1).
func (d *dashModel) focusLaunch() {
	block := "Evening"
	switch h := time.Now().Hour(); {
	case h < 12:
		block = "Morning"
	case h < 17:
		block = "Afternoon"
	}
	try := func(gi int) bool {
		if d.collapsed[d.groups[gi].g.ID] {
			return false
		}
		for ri := range d.groups[gi].rows {
			if !d.groups[gi].rows[ri].resolved() {
				d.selG, d.selR = gi, ri
				return true
			}
		}
		return false
	}
	for gi := range d.groups {
		if strings.EqualFold(d.groups[gi].g.Name, block) && try(gi) {
			return
		}
	}
	for gi := range d.groups {
		if try(gi) {
			return
		}
	}
	d.clampSelection()
}

func (d *dashModel) selected() *dashRow {
	if d.selG < len(d.groups) && d.selR >= 0 && d.selR < len(d.groups[d.selG].rows) {
		return &d.groups[d.selG].rows[d.selR]
	}
	return nil
}

// visible returns selectable (group,row) pairs in display order. A
// collapsed group contributes its header as row -1 — otherwise it could
// never be reached to expand again.
func (d *dashModel) visible() [][2]int {
	var out [][2]int
	for gi := range d.groups {
		if d.collapsed[d.groups[gi].g.ID] {
			out = append(out, [2]int{gi, -1})
			continue
		}
		for ri := range d.groups[gi].rows {
			out = append(out, [2]int{gi, ri})
		}
	}
	return out
}

func (d *dashModel) clampSelection() {
	vis := d.visible()
	if len(vis) == 0 {
		d.selG, d.selR = 0, 0
		return
	}
	for _, v := range vis {
		if v[0] == d.selG && v[1] == d.selR {
			return
		}
	}
	// Nearest: first row of the selected group or the first visible row.
	for _, v := range vis {
		if v[0] == d.selG {
			d.selG, d.selR = v[0], v[1]
			return
		}
	}
	d.selG, d.selR = vis[0][0], vis[0][1]
}

func (d *dashModel) move(delta int) {
	vis := d.visible()
	for i, v := range vis {
		if v[0] == d.selG && v[1] == d.selR {
			i += delta
			if i < 0 {
				i = 0
			}
			if i >= len(vis) {
				i = len(vis) - 1
			}
			d.selG, d.selR = vis[i][0], vis[i][1]
			return
		}
	}
	d.clampSelection()
}

// advance moves selection to the next unresolved habit after a check-off.
func (d *dashModel) advance() {
	vis := d.visible()
	start := -1
	for i, v := range vis {
		if v[0] == d.selG && v[1] == d.selR {
			start = i
			break
		}
	}
	for off := 1; off <= len(vis); off++ {
		v := vis[(start+off)%len(vis)]
		if v[1] >= 0 && !d.groups[v[0]].rows[v[1]].resolved() {
			d.selG, d.selR = v[0], v[1]
			return
		}
	}
}

// ---- key handling ----

func (d *dashModel) handleKey(msg tea.KeyPressMsg, a *App) tea.Cmd {
	k := a.keys
	// dd / gg chords.
	if d.pending != 0 {
		p := d.pending
		d.pending = 0
		if p == 'd' && msg.String() == "d" {
			return d.archive(a)
		}
		if p == 'g' && msg.String() == "g" {
			d.move(-1 << 20)
			return nil
		}
		// fall through: the second key is handled normally below
	}

	r := d.selected()
	switch {
	case key.Matches(msg, k.Down):
		d.move(1)
	case key.Matches(msg, k.Up):
		d.move(-1)
	case key.Matches(msg, k.Bottom):
		d.move(1 << 20)
	case key.Matches(msg, k.Top):
		d.pending = 'g'
	case key.Matches(msg, k.Archive):
		d.pending = 'd'
	case key.Matches(msg, k.Left):
		if r != nil {
			d.collapsed[d.groups[d.selG].g.ID] = true
			d.selR = -1 // stay on the collapsed header
		}
	case key.Matches(msg, k.Right):
		if d.selG < len(d.groups) {
			delete(d.collapsed, d.groups[d.selG].g.ID)
			if d.selR < 0 {
				d.selR = 0
			}
		}
	case key.Matches(msg, k.Toggle):
		if r == nil && d.selR == -1 && d.selG < len(d.groups) { // header: expand
			delete(d.collapsed, d.groups[d.selG].g.ID)
			d.selR = 0
			return nil
		}
		return d.toggle(a)
	case key.Matches(msg, k.Inc):
		return d.nudge(a, +1)
	case key.Matches(msg, k.Dec):
		return d.nudge(a, -1)
	case key.Matches(msg, k.Skip):
		if r != nil && !r.h.Paused() {
			a.overlays = append(a.overlays, skipOverlay{habit: r.h})
		}
	case key.Matches(msg, k.Pause):
		return d.pause(a)
	case key.Matches(msg, k.MoveDown):
		return d.reorder(a, 1)
	case key.Matches(msg, k.MoveUp):
		return d.reorder(a, -1)
	case key.Matches(msg, k.New):
		a.overlays = append(a.overlays, newEditor(a, nil))
	case key.Matches(msg, k.Edit):
		if r != nil {
			h := r.h
			a.overlays = append(a.overlays, newEditor(a, &h))
		}
	case key.Matches(msg, k.PrevDay):
		return d.gotoDay(a, a.viewDay.AddDays(-1))
	case key.Matches(msg, k.NextDay):
		return d.gotoDay(a, a.viewDay.AddDays(1))
	case key.Matches(msg, k.Today):
		return d.gotoDay(a, a.day)
	case key.Matches(msg, k.Esc):
		if d.filterTag != "" {
			d.filterTag = ""
			d.rebuild(a)
			return a.Toast(a.theme.Dim.Render("filter cleared"))
		}
		if a.viewDay != a.day {
			return d.gotoDay(a, a.day)
		}
	}
	return nil
}

// gotoDay time-travels the dashboard to day (clamped at today) and reloads
// the snapshot window around it — every action then edits that day.
// Deliberately NOT gated on a.pending: the view is switching days, so there
// is no on-screen optimistic state to clobber, snapMsg drops stale results
// by day, and a queued write that errors (no fsnotify event) would otherwise
// leave the header and rows disagreeing forever.
func (d *dashModel) gotoDay(a *App, day domain.Day) tea.Cmd {
	if day > a.day || day == a.viewDay {
		return nil
	}
	a.viewDay = day
	return a.loadSnap()
}

// ---- mutations (optimistic in-memory + queued persistence) ----

func undoHint(a *App) string { return a.theme.Dim.Render("u undo") }

// toggle: check habits flip done; quantified habits log the full target;
// done anything reverts to pending (§4.2).
func (d *dashModel) toggle(a *App) tea.Cmd {
	r := d.selected()
	if r == nil || r.h.Paused() {
		return nil
	}
	if r.status() == domain.StatusDone {
		return d.clear(a, r)
	}
	amount := r.h.Target // full target in one stroke (§6.3); 0 for check habits
	return d.setEntry(a, r, domain.Entry{
		HabitID: r.h.ID, Day: a.viewDay, Status: domain.StatusDone,
		Amount: amount, LoggedAt: time.Now(), Source: "tui",
	})
}

// nudge: +/- by the habit's step (quantified only).
func (d *dashModel) nudge(a *App, dir float64) tea.Cmd {
	r := d.selected()
	if r == nil || r.h.Kind != domain.Quantified || r.h.Paused() {
		return nil
	}
	cur := 0.0
	if r.entry != nil {
		cur = r.entry.Amount
	}
	amount := cur + dir*r.h.Step
	if amount <= 0 {
		return d.clear(a, r)
	}
	e := domain.Entry{
		HabitID: r.h.ID, Day: a.viewDay, Status: r.h.StatusFor(amount),
		Amount: amount, LoggedAt: time.Now(), Source: "tui",
	}
	if r.entry != nil {
		e.Note = r.entry.Note
	}
	return d.setEntry(a, r, e)
}

// setEntry applies e optimistically, builds the toast, queues the write,
// and auto-advances on completion.
func (d *dashModel) setEntry(a *App, r *dashRow, e domain.Entry) tea.Cmd {
	wasDone := r.status() == domain.StatusDone
	r.entry = &e
	r.spark[13] = domain.EntryScore(r.h, e)

	var toast string
	gl, th := a.gl, a.theme
	switch e.Status {
	case domain.StatusDone:
		if !wasDone {
			if a.viewDay != a.day {
				// Backfill: the true streak effect (resurrection, extension)
				// only falls out of the store recompute — the reload shows it.
				toast = fmt.Sprintf("%s %s · %s · %s", th.Ok.Render(gl.Done),
					r.h.Name, a.viewDay.Time().Format("Jan 2"), undoHint(a))
			} else {
				old := r.streak.Current
				if r.h.Schedule == domain.Weekly {
					r.weekDone++
					if r.weekDone == r.h.PerWeek {
						r.streak.Current++
					}
				} else if r.streak.LastDay != a.day {
					if r.streak.LastDay == a.day.AddDays(-1) {
						r.streak.Current++
					} else {
						r.streak.Current = 1
					}
					r.streak.LastDay = a.day
				}
				if r.streak.Current > r.streak.Best {
					r.streak.Best = r.streak.Current
				}
				toast = fmt.Sprintf("%s %s · streak %s · %s",
					th.Ok.Render(gl.Done), r.h.Name, streakText(r), undoHint(a))
				if m := domain.MilestoneCrossed(old, r.streak.Current); m != 0 {
					toast = fmt.Sprintf("%s %s · %s %d %s! · %s",
						th.Ok.Render(gl.Done), r.h.Name,
						th.Accent.Render(gl.Milestone), m, "milestone", undoHint(a))
				}
			}
			d.advance()
		} else {
			toast = fmt.Sprintf("%s %s %g %s · %s", th.Ok.Render(gl.Done), r.h.Name, e.Amount, r.h.Unit, undoHint(a))
		}
	case domain.StatusPartial:
		toast = fmt.Sprintf("%s %s %g / %g %s · %s",
			th.Warn.Render(gl.Partial), r.h.Name, e.Amount, r.h.Target, r.h.Unit, undoHint(a))
	case domain.StatusSkip:
		reason := e.SkipReason
		if reason == "" {
			reason = "no reason"
		}
		toast = fmt.Sprintf("%s skipped %s (%s) · %s", th.Danger.Render(gl.Skip), r.h.Name, reason, undoHint(a))
		d.advance()
	}
	return tea.Batch(a.Toast(toast), a.mutate(func(s *store.Store) error { return s.SetEntry(e) }))
}

func (d *dashModel) clear(a *App, r *dashRow) tea.Cmd {
	if r.entry == nil {
		return nil
	}
	if r.status() == domain.StatusDone && a.viewDay == a.day {
		if r.h.Schedule == domain.Weekly {
			r.weekDone--
			if r.weekDone == r.h.PerWeek-1 && r.streak.Current > 0 {
				r.streak.Current--
			}
		} else if r.streak.Current > 0 {
			r.streak.Current-- // approximate; the reload recomputes exactly
		}
	}
	id, day := r.h.ID, r.entry.Day
	r.entry = nil
	r.spark[13] = 0
	toast := fmt.Sprintf("%s %s unchecked · %s", a.theme.Dim.Render(a.gl.Pending), r.h.Name, undoHint(a))
	return tea.Batch(a.Toast(toast), a.mutate(func(s *store.Store) error { return s.ClearEntry(id, day) }))
}

func (d *dashModel) pause(a *App) tea.Cmd {
	r := d.selected()
	if r == nil {
		return nil
	}
	h := r.h
	var toast string
	if h.Paused() {
		h.PausedAt = nil
		toast = fmt.Sprintf("resumed %s · %s", h.Name, undoHint(a))
	} else {
		now := time.Now()
		h.PausedAt = &now
		toast = fmt.Sprintf("%s paused %s · %s", a.theme.Dim.Render(a.gl.Pause), h.Name, undoHint(a))
	}
	r.h = h
	return tea.Batch(a.Toast(toast), a.mutate(func(s *store.Store) error { return s.UpdateHabit(h) }))
}

func (d *dashModel) archive(a *App) tea.Cmd {
	r := d.selected()
	if r == nil {
		return nil
	}
	h := r.h
	now := time.Now()
	h.ArchivedAt = &now
	// Optimistic removal from the group.
	g := &d.groups[d.selG]
	g.rows = append(g.rows[:d.selR], g.rows[d.selR+1:]...)
	d.clampSelection()
	toast := fmt.Sprintf("archived %s · %s", h.Name, undoHint(a))
	return tea.Batch(a.Toast(toast), a.mutate(func(s *store.Store) error { return s.UpdateHabit(h) }))
}

func (d *dashModel) reorder(a *App, delta int) tea.Cmd {
	g := &d.groups[d.selG]
	j := d.selR + delta
	if j < 0 || j >= len(g.rows) {
		return nil
	}
	x, y := g.rows[d.selR].h, g.rows[j].h
	g.rows[d.selR], g.rows[j] = g.rows[j], g.rows[d.selR]
	g.rows[d.selR].h.Position, g.rows[j].h.Position = x.Position, y.Position
	d.selR = j
	return a.mutate(func(s *store.Store) error { return s.SwapPositions(x, y) })
}

// ---- view ----

func streakText(r *dashRow) string {
	unit := "d"
	if r.h.Schedule == domain.Weekly {
		unit = "w"
	}
	return fmt.Sprintf("%d%s", r.streak.Current, unit)
}

func (d *dashModel) view(a *App) string {
	th, gl := a.theme, a.gl
	w := a.w
	var lines []string
	d.rowLines = map[int][2]int{}

	// Day summary: done / total across non-paused habits (§5.1).
	done, total := 0, 0
	for gi := range d.groups {
		for ri := range d.groups[gi].rows {
			r := &d.groups[gi].rows[ri]
			if r.h.Paused() {
				continue
			}
			total++
			if r.status() == domain.StatusDone {
				done++
			}
		}
	}
	frac := 0.0
	if total > 0 {
		frac = float64(done) / float64(total)
	}
	label := th.Text.Render("Today")
	if a.viewDay != a.day {
		ago := domain.DaysBetween(a.viewDay, a.day)
		label = th.Warn.Render(fmt.Sprintf("%s · %dd ago", a.viewDay.Time().Format("Mon · Jan 2"), ago)) +
			th.Dim.Render(" · t today")
	}
	summary := "   " + label + th.Text.Render(fmt.Sprintf(" · %d of %d   ", done, total)) +
		th.Accent.Render(widgets.Bar(frac, 21, gl.BarOn, gl.BarOff)) +
		th.Text.Render(fmt.Sprintf("  %2.0f%%", frac*100))
	if d.filterTag != "" {
		summary += "   " + th.Accent.Render("#"+d.filterTag)
	}
	freeze := ""
	if a.snap.Freeze > 0 {
		freeze = fmt.Sprintf("%s %d", th.Freeze.Render(gl.Freeze), a.snap.Freeze)
	}
	if pad := w - lipgloss.Width(summary) - lipgloss.Width(freeze) - 3; pad > 0 && freeze != "" {
		summary += strings.Repeat(" ", pad) + freeze
	}
	lines = append(lines, "", summary, "")

	showSpark := w >= 76 && gl.Spark != nil
	showMeta := w >= 60

	for gi := range d.groups {
		g := &d.groups[gi]
		fracTxt := fmt.Sprintf("%d/%d", g.doneCount(), len(g.rows))
		titleStyle := th.Dim
		if gi == d.selG && d.selR == -1 {
			titleStyle = th.Accent // selected collapsed header
		}
		header := "   " + widgets.Rule(
			titleStyle.Render(strings.ToUpper(g.g.Name)), th.Dim.Render(fracTxt),
			w-6, gl.HRule, th.Faint)
		d.rowLines[len(lines)] = [2]int{gi, -1}
		lines = append(lines, header)
		if d.collapsed[g.g.ID] {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, "")
		for ri := range g.rows {
			d.rowLines[len(lines)] = [2]int{gi, ri}
			lines = append(lines, d.rowView(a, &g.rows[ri], gi == d.selG && ri == d.selR, showSpark, showMeta))
		}
		lines = append(lines, "")
	}

	// Keep the selection in view when the list outgrows the body.
	// ponytail: window slicing, no viewport component — revisit if habit
	// lists grow beyond a screen routinely.
	bodyH := a.h - 7 // header 2 + toast/rule/help 3 + body padding 2
	if len(lines) > bodyH && bodyH > 0 {
		selLine := 0
		for ln, v := range d.rowLines {
			if v[0] == d.selG && v[1] == d.selR {
				selLine = ln
			}
		}
		start := selLine - bodyH/2
		if start < 0 {
			start = 0
		}
		if start > len(lines)-bodyH {
			start = len(lines) - bodyH
		}
		lines = lines[start : start+bodyH]
		remap := map[int][2]int{}
		for ln, v := range d.rowLines {
			remap[ln-start] = v
		}
		d.rowLines = remap
	}
	return strings.Join(lines, "\n")
}

func (d *dashModel) rowView(a *App, r *dashRow, selected, showSpark, showMeta bool) string {
	th, gl := a.theme, a.gl

	bar := " "
	if selected {
		bar = th.Accent.Render(gl.SelBar)
	}

	glyph := th.Dim.Render(gl.Pending)
	switch {
	case r.h.Paused():
		glyph = th.Dim.Render(gl.Pause)
	case r.status() == domain.StatusDone:
		glyph = th.Ok.Render(gl.Done)
	case r.status() == domain.StatusPartial:
		glyph = th.Warn.Render(gl.Partial)
	case r.status() == domain.StatusSkip:
		glyph = th.Danger.Render(gl.Skip)
	case r.status() == domain.StatusFreeze:
		glyph = th.Freeze.Render(gl.Freeze)
	case r.h.Schedule == domain.Weekly && r.weekDone > 0:
		glyph = th.Accent.Render(gl.Week)
	}

	// Completed rows recede (§5.1); the selected row stays bright.
	nameStyle := th.Text
	if r.resolved() && !selected {
		nameStyle = th.Dim
	}
	nameW := 20
	name := nameStyle.Render(ansi.Truncate(r.h.Name, nameW, "…"))
	namePad := strings.Repeat(" ", max(nameW-lipgloss.Width(name), 0))

	meta := ""
	switch {
	case r.h.Schedule == domain.Weekly:
		meta = fmt.Sprintf("%d / %d this wk", r.weekDone, r.h.PerWeek)
	case r.h.Kind == domain.Quantified:
		cur := 0.0
		if r.entry != nil {
			cur = r.entry.Amount
		}
		if r.status() == domain.StatusDone {
			meta = fmt.Sprintf("%g %s", cur, r.h.Unit)
		} else {
			meta = fmt.Sprintf("%g / %g %s", cur, r.h.Target, r.h.Unit)
		}
	}
	metaCell := ""
	if showMeta {
		metaCell = th.Dim.Render(meta) + strings.Repeat(" ", max(15-lipgloss.Width(meta), 0))
	}

	sparkCell := ""
	if showSpark {
		sparkCell = th.Subtle.Render(widgets.Sparkline(r.spark, gl.Spark)) + "   "
	}

	// Streak cell: ◆ from 30 up; warn tint when tonight's streak is at risk.
	streakStyle := th.Dim
	if a.conf.AtRiskNudge && !r.resolved() && r.streak.Current >= 7 && time.Now().Hour() >= 21 {
		streakStyle = th.Warn
	}
	streak := streakStyle.Render(streakText(r))
	if a.conf.MilestoneMarks && r.streak.Current >= 30 {
		streak = th.Accent.Render(gl.Milestone) + " " + streak
	}
	streakCell := strings.Repeat(" ", max(6-lipgloss.Width(streak), 0)) + streak

	return fmt.Sprintf("   %s %s  %s%s  %s%s%s", bar, glyph, name, namePad, metaCell, sparkCell, streakCell)
}

// ---- skip-reason picker (§6.4) ----

type skipOverlay struct{ habit domain.Habit }

var skipChoices = []struct {
	key, reason, label string
}{
	{"t", "tired", "tired"},
	{"v", "travel", "travel"},
	{"s", "sick", "sick"},
	{"o", "other", "other"},
	{"n", "", "no reason"},
}

func (o skipOverlay) Update(msg tea.Msg, a *App) (Overlay, tea.Cmd) {
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return o, nil
	}
	if key.Matches(kp, a.keys.Esc) {
		return nil, nil
	}
	for _, c := range skipChoices {
		if kp.String() == c.key {
			d := &a.dash
			if r := d.selected(); r != nil && r.h.ID == o.habit.ID {
				return nil, d.setEntry(a, r, domain.Entry{
					HabitID: o.habit.ID, Day: a.viewDay, Status: domain.StatusSkip,
					SkipReason: c.reason, LoggedAt: time.Now(), Source: "tui",
				})
			}
			return nil, nil
		}
	}
	return o, nil
}

func (o skipOverlay) View(a *App) string {
	th := a.theme
	var b strings.Builder
	b.WriteString(th.Text.Render("Skip "+o.habit.Name) + "\n\n")
	for _, c := range skipChoices {
		b.WriteString(fmt.Sprintf("  %s  %s\n", th.Accent.Render(c.key), th.Dim.Render(c.label)))
	}
	b.WriteString("\n" + th.Dim.Render("esc cancel"))
	box := lipgloss.NewStyle().
		Border(a.border).BorderForeground(th.AccentColor).
		Padding(0, 2).Render(b.String())
	return widgets.Shadowed(box, a.gl.Shadow, th.Faint)
}
