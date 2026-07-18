package ui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"habit/internal/domain"
	"habit/internal/ui/widgets"
)

// anaModel is the Analytics tab (§5.4): a 53×7 GitHub-style heatmap over a
// year of logical days, plus a stats card. habitIdx 0 = the "All habits"
// aggregate; i+1 = snap.Habits[i]. The spec's month panning is replaced by
// a day cursor — a full year already fits at 80 columns.
type anaModel struct {
	habitIdx int
	year     int
	selDay   domain.Day

	habits     []domain.Habit // incl. archived: their history still counts
	entries    []domain.Entry // selected year's window
	loadedYear int

	pendingDetail bool // open the day popover once data arrives (@date)

	// Layout cache for mouse hits, rebuilt every render.
	heatY, heatX int
	gridStart    domain.Day
	weeks        int
}

type anaMsg struct {
	year    int
	habits  []domain.Habit
	entries []domain.Entry
}

func (m *anaModel) ensure(a *App) tea.Cmd {
	if m.year == 0 {
		m.year = a.day.Time().Year()
		m.selDay = a.day
	}
	if m.loadedYear == m.year {
		return nil
	}
	return m.load(a)
}

func (m *anaModel) load(a *App) tea.Cmd {
	s, year := a.store, m.year
	return func() tea.Msg {
		habits, err := s.Habits(true)
		if err != nil {
			return errMsg{err}
		}
		from := domain.Day(fmt.Sprintf("%d-01-01", year))
		to := domain.Day(fmt.Sprintf("%d-12-31", year))
		entries, err := s.EntriesRange(from, to)
		if err != nil {
			return errMsg{err}
		}
		return anaMsg{year, habits, entries}
	}
}

// habit returns the focused habit, or nil for the aggregate view.
func (m *anaModel) habit(a *App) *domain.Habit {
	if m.habitIdx == 0 || m.habitIdx > len(a.snap.Habits) {
		return nil
	}
	return &a.snap.Habits[m.habitIdx-1]
}

func (m *anaModel) handleKey(msg tea.KeyPressMsg, a *App) tea.Cmd {
	k := a.keys
	moveSel := func(days int) {
		d := m.selDay.AddDays(days)
		if d > a.day {
			d = a.day
		}
		if d.Time().Year() != m.year {
			m.year = d.Time().Year()
			m.selDay = d
			return // year changed: reload below
		}
		m.selDay = d
	}
	switch {
	case key.Matches(msg, k.NextHabit):
		m.habitIdx = (m.habitIdx + 1) % (len(a.snap.Habits) + 1)
	case key.Matches(msg, k.PrevHabit):
		m.habitIdx = (m.habitIdx + len(a.snap.Habits)) % (len(a.snap.Habits) + 1)
	case key.Matches(msg, k.PrevYear):
		m.year--
		m.clampSelToYear(a)
	case key.Matches(msg, k.NextYear):
		if m.year < a.day.Time().Year() {
			m.year++
			m.clampSelToYear(a)
		}
	case key.Matches(msg, k.Left):
		moveSel(-7)
	case key.Matches(msg, k.Right):
		moveSel(7)
	case key.Matches(msg, k.Up):
		moveSel(-1)
	case key.Matches(msg, k.Down):
		moveSel(1)
	case key.Matches(msg, k.Toggle):
		a.overlays = append(a.overlays, newDayDetail(a, m.selDay))
		return nil
	}
	if m.loadedYear != m.year {
		return m.load(a)
	}
	return nil
}

func (m *anaModel) clampSelToYear(a *App) {
	d := domain.Day(strconv.Itoa(m.year) + string(m.selDay[4:]))
	if d > a.day {
		d = a.day
	}
	m.selDay = d
}

func (m *anaModel) click(a *App, x, y int) tea.Cmd {
	row := y - m.heatY
	col := x - m.heatX
	if row < 0 || row > 6 || col < 0 || col >= m.weeks {
		return nil
	}
	d := m.gridStart.AddDays(col*7 + row)
	if d > a.day || d.Time().Year() != m.year {
		return nil
	}
	m.selDay = d
	a.overlays = append(a.overlays, newDayDetail(a, d))
	return nil
}

// ---- data crunching ----

// dayScores computes the aggregate score per day of the loaded year.
func (m *anaModel) dayScores(a *App, upTo domain.Day) map[domain.Day]float64 {
	byDay := map[domain.Day][]domain.Entry{}
	for _, e := range m.entries {
		byDay[e.Day] = append(byDay[e.Day], e)
	}
	scores := map[domain.Day]float64{}
	for d := domain.Day(fmt.Sprintf("%d-01-01", m.year)); d <= upTo && string(d[:4]) == strconv.Itoa(m.year); d = d.AddDays(1) {
		scores[d] = domain.DayScore(m.habits, byDay[d], d)
	}
	return scores
}

func pct(f float64) string { return fmt.Sprintf("%.0f%%", f*100) }

// rate is the mean aggregate score over the trailing window (bounded by
// the loaded year — January windows clip, which is fine for a stats card).
func rate(scores map[domain.Day]float64, today domain.Day, window int) float64 {
	sum, n := 0.0, 0
	for i := 0; i < window; i++ {
		if s, ok := scores[today.AddDays(-i)]; ok {
			sum += s
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// ---- view ----

func (m *anaModel) view(a *App) string {
	if m.loadedYear != m.year {
		return "\n   " + a.theme.Dim.Render("crunching…")
	}
	th := a.theme
	var b strings.Builder

	// Selector row: ‹ [ All habits ] ›                       ‹ 2026 ›
	name := "All habits"
	if h := m.habit(a); h != nil {
		name = h.Name
	}
	left := fmt.Sprintf("   %s %s %s", th.Dim.Render("‹"), th.Accent.Render("[ "+name+" ]"), th.Dim.Render("›"))
	right := fmt.Sprintf("%s %s %s", th.Dim.Render("‹"), th.Text.Render(strconv.Itoa(m.year)), th.Dim.Render("›"))
	pad := a.w - lipgloss.Width(left) - lipgloss.Width(right) - 3
	if pad < 1 {
		pad = 1
	}
	b.WriteString("\n" + left + strings.Repeat(" ", pad) + right + "\n\n")

	m.renderHeatmap(a, &b)
	b.WriteString("\n")
	m.renderStats(a, &b)
	return b.String()
}

func (m *anaModel) renderHeatmap(a *App, b *strings.Builder) {
	th, gl := a.theme, a.gl
	ws := a.store.Opt().WeekStart
	jan1 := domain.Day(fmt.Sprintf("%d-01-01", m.year))
	dec31 := domain.Day(fmt.Sprintf("%d-12-31", m.year))
	m.gridStart = jan1.WeekStart(ws)
	m.weeks = domain.DaysBetween(m.gridStart, dec31)/7 + 1
	if maxWeeks := a.w - 12; m.weeks > maxWeeks && maxWeeks > 0 {
		m.weeks = maxWeeks // ponytail: clip trailing weeks under 65 cols; panning if anyone asks
	}

	// Month labels at each month's first week column.
	labels := make([]byte, m.weeks)
	for i := range labels {
		labels[i] = ' '
	}
	labelRow := strings.Repeat(" ", m.weeks)
	for mo := 1; mo <= 12; mo++ {
		first := domain.Day(fmt.Sprintf("%d-%02d-01", m.year, mo))
		col := domain.DaysBetween(m.gridStart, first.WeekStart(ws)) / 7
		if col >= 0 && col+3 <= m.weeks {
			labelRow = labelRow[:col] + first.Time().Format("Jan") + labelRow[col+3:]
		}
	}
	b.WriteString("         " + th.Dim.Render(labelRow) + "\n")

	// Cell painters.
	h := m.habit(a)
	var scores map[domain.Day]float64
	byDay := map[domain.Day]*domain.Entry{}
	if h == nil {
		scores = m.dayScores(a, a.day)
	} else {
		for i := range m.entries {
			if m.entries[i].HabitID == h.ID {
				byDay[m.entries[i].Day] = &m.entries[i]
			}
		}
	}
	cell := func(d domain.Day) (string, lipgloss.Style) {
		if d > a.day || d < jan1 || d > dec31 {
			return " ", th.Faint
		}
		if h != nil {
			e := byDay[d]
			switch {
			case e == nil:
				return string(gl.Heat[0]), th.Faint
			case e.Status == domain.StatusDone:
				return string(gl.Heat[4]), th.Accent
			case e.Status == domain.StatusPartial:
				frac := 0.0
				if h.Target > 0 {
					frac = min(1, e.Amount/h.Target)
				}
				return string(gl.Heat[1+int(frac*2)]), th.Accent
			case e.Status == domain.StatusFreeze:
				return gl.Freeze, th.Freeze
			case e.Status == domain.StatusPause:
				return gl.Pause, th.Dim
			default: // skip
				return string(gl.Heat[0]), th.Faint
			}
		}
		s := scores[d]
		switch {
		case s == 0:
			return string(gl.Heat[0]), th.Faint
		case s <= 0.33:
			return string(gl.Heat[1]), th.Subtle
		case s <= 0.66:
			return string(gl.Heat[2]), th.Subtle
		case s < 1:
			return string(gl.Heat[3]), th.Accent
		default:
			return string(gl.Heat[4]), th.Accent
		}
	}

	m.heatY = 6 // header(2) + blank + selector + blank + label row
	m.heatX = 9
	dayNames := [7]string{}
	for i := 0; i < 7; i++ {
		dayNames[i] = m.gridStart.AddDays(i).Time().Format("Mon")
	}
	for r := 0; r < 7; r++ {
		label := "   "
		if r%2 == 0 {
			label = dayNames[r]
		}
		b.WriteString("    " + th.Dim.Render(label) + "  ")
		// Group consecutive same-style cells to keep repaints cheap.
		var run strings.Builder
		var runStyle lipgloss.Style
		runActive := false
		flush := func() {
			if runActive {
				b.WriteString(runStyle.Render(run.String()))
				run.Reset()
			}
		}
		for w := 0; w < m.weeks; w++ {
			d := m.gridStart.AddDays(w*7 + r)
			ch, style := cell(d)
			if d == m.selDay {
				flush()
				b.WriteString(style.Reverse(true).Render(ch))
				runActive = false
				continue
			}
			if !runActive || !sameStyle(style, runStyle) {
				flush()
				runStyle, runActive = style, true
			}
			run.WriteString(ch)
		}
		flush()
		b.WriteString("\n")
	}

	// Legend + selected day (§5.4).
	sel := m.selDay.Time().Format("Jan 2")
	var selTxt string
	if h == nil {
		selTxt = fmt.Sprintf("%s %s", sel, pct(m.dayScoreOf(a, m.selDay)))
	} else {
		selTxt = sel
	}
	legend := fmt.Sprintf("    %s 0%%   %s ≤33%%   %s ≤66%%   %s ≤99%%   %s 100%%",
		string(gl.Heat[0]), string(gl.Heat[1]), string(gl.Heat[2]), string(gl.Heat[3]), string(gl.Heat[4]))
	if h != nil {
		legend = fmt.Sprintf("    %s miss   %s partial   %s done   %s frozen   %s paused",
			string(gl.Heat[0]), string(gl.Heat[1]), string(gl.Heat[4]), gl.Freeze, gl.Pause)
	}
	pad := a.w - lipgloss.Width(legend) - len(selTxt) - 12
	if pad < 1 {
		pad = 1
	}
	b.WriteString("\n" + th.Dim.Render(legend) + strings.Repeat(" ", pad) + th.Text.Render("selected "+selTxt) + "\n")
}

func sameStyle(a, b lipgloss.Style) bool {
	return a.GetForeground() == b.GetForeground()
}

func (m *anaModel) dayScoreOf(a *App, d domain.Day) float64 {
	var es []domain.Entry
	for _, e := range m.entries {
		if e.Day == d {
			es = append(es, e)
		}
	}
	return domain.DayScore(m.habits, es, d)
}

func (m *anaModel) renderStats(a *App, b *strings.Builder) {
	th, gl := a.theme, a.gl
	h := m.habit(a)
	today := a.day

	title := " All habits "
	if h != nil {
		title = " " + h.Name + " "
	}

	// Rates.
	var r30, r90, rYear float64
	if h == nil {
		scores := m.dayScores(a, today)
		r30, r90, rYear = rate(scores, today, 30), rate(scores, today, 90), rate(scores, today, 365)
	} else {
		done := map[domain.Day]bool{}
		for _, e := range m.entries {
			if e.HabitID == h.ID && e.Status == domain.StatusDone {
				done[e.Day] = true
			}
		}
		hr := func(window int) float64 {
			n := 0
			for i := 0; i < window; i++ {
				if done[today.AddDays(-i)] {
					n++
				}
			}
			return float64(n) / float64(window)
		}
		r30, r90, rYear = hr(30), hr(90), hr(365)
	}

	// Right column facts.
	var rightTop, rightBottom string
	if h == nil {
		perfect := 0
		for _, s := range m.dayScores(a, today) {
			if s >= 0.999 {
				perfect++
			}
		}
		frozen := 0
		for _, e := range m.entries {
			if e.Status == domain.StatusFreeze {
				frozen++
			}
		}
		rightTop = fmt.Sprintf("perfect days  %d", perfect)
		rightBottom = fmt.Sprintf("freezes used  %d", frozen)
	} else {
		st := a.snap.Streaks[h.ID]
		unit := "d"
		if h.Schedule == domain.Weekly {
			unit = "w"
		}
		rightTop = fmt.Sprintf("current  %d%s", st.Current, unit)
		rightBottom = fmt.Sprintf("best     %d%s", st.Best, unit)
	}

	// Weekday bars + skip reasons.
	wdScore := [7]float64{}
	wdCount := [7]int{}
	if h == nil {
		for d, s := range m.dayScores(a, today) {
			wd := int(d.Weekday())
			wdScore[wd] += s
			wdCount[wd]++
		}
	} else {
		start := domain.Day(fmt.Sprintf("%d-01-01", m.year))
		doneBy := map[domain.Day]bool{}
		for _, e := range m.entries {
			if e.HabitID == h.ID && e.Status == domain.StatusDone {
				doneBy[e.Day] = true
			}
		}
		for d := start; d <= today && d.Time().Year() == m.year; d = d.AddDays(1) {
			wd := int(d.Weekday())
			wdCount[wd]++
			if doneBy[d] {
				wdScore[wd]++
			}
		}
	}
	skips := map[string]int{}
	for _, e := range m.entries {
		if e.Status == domain.StatusSkip && (h == nil || e.HabitID == h.ID) {
			r := e.SkipReason
			if r == "" {
				r = "none"
			}
			skips[r]++
		}
	}
	type sk struct {
		reason string
		n      int
	}
	var sks []sk
	for r, n := range skips {
		sks = append(sks, sk{r, n})
	}
	sort.Slice(sks, func(i, j int) bool { return sks[i].n > sks[j].n })

	barGlyph, capGlyph := "▇", "▏"
	if gl.Spark == nil {
		barGlyph, capGlyph = "#", "|"
	}

	// %-Ns pads by byte length; styled strings need display-width padding.
	padTo := func(s string, w int) string {
		if p := w - lipgloss.Width(s); p > 0 {
			return s + strings.Repeat(" ", p)
		}
		return s
	}

	var body strings.Builder
	line1 := fmt.Sprintf("rate     %s 30d · %s 90d · %s year", pct(r30), pct(r90), pct(rYear))
	body.WriteString("  " + padTo(th.Text.Render(line1), 48) + th.Dim.Render(rightTop) + "\n")
	var line2 string
	if h == nil {
		var bestName string
		best := 0
		for _, hb := range a.snap.Habits {
			if st := a.snap.Streaks[hb.ID]; st.Best > best {
				best, bestName = st.Best, hb.Name
			}
		}
		if bestName != "" {
			line2 = fmt.Sprintf("longest chain  %s %s %dd", bestName, gl.Milestone, best)
		}
	}
	body.WriteString("  " + padTo(th.Dim.Render(line2), 48) + th.Dim.Render(rightBottom) + "\n")

	// Weekday bars + skip reasons need ~9 more rows; drop them on short
	// terminals (graceful truncation, §10).
	if a.h >= 32 {
		body.WriteString("\n")

		// Order weekday rows from the configured week start.
		ws := int(a.store.Opt().WeekStart)
		for i := 0; i < 7; i++ {
			wd := (ws + i) % 7
			f := 0.0
			if wdCount[wd] > 0 {
				f = wdScore[wd] / float64(wdCount[wd])
			}
			bar := strings.Repeat(barGlyph, int(f*10+0.5))
			row := fmt.Sprintf("%s %s%s %s", time.Weekday(wd).String()[:3],
				th.Accent.Render(bar), th.Faint.Render(capGlyph), th.Dim.Render(pct(f)))
			skipCol := ""
			if i == 0 {
				skipCol = th.Dim.Render("skip reasons")
			} else if i-1 < len(sks) {
				s := sks[i-1]
				skipCol = padTo(th.Dim.Render(s.reason), 9) +
					th.Warn.Render(strings.Repeat(barGlyph, min(s.n, 12))) + fmt.Sprintf(" %d", s.n)
			}
			body.WriteString("  " + padTo(row, 28) + skipCol + "\n")
		}
	}

	boxW := min(a.w-6, 72)
	box := lipgloss.NewStyle().
		Border(a.border).BorderForeground(th.AccentColor).
		Width(boxW).Render(strings.TrimRight(body.String(), "\n"))
	lines := strings.Split(box, "\n")
	lines[0] = widgets.StampTitle(lines[0], th.Text.Render(title), 2)
	for _, l := range lines {
		b.WriteString("   " + l + "\n")
	}
}

// ---- day-detail popover (§5.4) ----

type dayDetailOverlay struct {
	day  domain.Day
	rows []string
}

func newDayDetail(a *App, day domain.Day) *dayDetailOverlay {
	th, gl := a.theme, a.gl
	o := &dayDetailOverlay{day: day}
	byID := map[int64]domain.Habit{}
	for _, h := range a.ana.habits {
		byID[h.ID] = h
	}
	glyphFor := map[domain.Status]string{
		domain.StatusDone: th.Ok.Render(gl.Done), domain.StatusPartial: th.Warn.Render(gl.Partial),
		domain.StatusSkip: th.Danger.Render(gl.Skip), domain.StatusFreeze: th.Freeze.Render(gl.Freeze),
		domain.StatusPause: th.Dim.Render(gl.Pause),
	}
	for _, e := range a.ana.entries {
		if e.Day != day {
			continue
		}
		h, ok := byID[e.HabitID]
		if !ok {
			continue
		}
		detail := ""
		switch {
		case e.Status == domain.StatusPartial || (e.Status == domain.StatusDone && h.Kind == domain.Quantified):
			detail = fmt.Sprintf("%g", e.Amount)
			if h.Target > 0 && e.Status == domain.StatusPartial {
				detail += fmt.Sprintf(" / %g", h.Target)
			}
			detail += " " + h.Unit
		case e.Status == domain.StatusSkip && e.SkipReason != "":
			detail = "(" + e.SkipReason + ")"
		}
		if e.Note != "" {
			detail += "  " + th.Dim.Render(e.Note)
		}
		if e.Source == "backfill" {
			detail += "  " + th.Dim.Render(gl.Backfill+" backfilled")
		}
		o.rows = append(o.rows, fmt.Sprintf("%s  %-20s %s", glyphFor[e.Status], h.Name, th.Dim.Render(detail)))
	}
	if len(o.rows) == 0 {
		o.rows = []string{th.Dim.Render("no entries this day")}
	}
	return o
}

func (o *dayDetailOverlay) Update(msg tea.Msg, a *App) (Overlay, tea.Cmd) {
	if kp, ok := msg.(tea.KeyPressMsg); ok {
		if key.Matches(kp, a.keys.Esc, a.keys.Toggle, a.keys.Quit) {
			return nil, nil
		}
	}
	return o, nil
}

func (o *dayDetailOverlay) View(a *App) string {
	th := a.theme
	var b strings.Builder
	for _, r := range o.rows {
		b.WriteString("  " + r + "\n")
	}
	b.WriteString("\n" + th.Dim.Render("  esc close"))
	box := lipgloss.NewStyle().
		Border(a.border).BorderForeground(th.AccentColor).
		Padding(0, 1).Render(strings.TrimRight(b.String(), "\n"))
	lines := strings.Split(box, "\n")
	title := " " + o.day.Time().Format("Mon · Jan 2 2006") + " "
	lines[0] = widgets.StampTitle(lines[0], th.Text.Render(title), 2)
	return widgets.Shadowed(strings.Join(lines, "\n"), a.gl.Shadow, th.Faint)
}

// openAnalytics jumps to a specific day (palette @date, §4.3).
func (a *App) openAnalytics(day domain.Day) tea.Cmd {
	a.tab = TabAnalytics
	a.ana.selDay = day
	a.ana.year = day.Time().Year()
	if a.ana.loadedYear == a.ana.year {
		a.overlays = append(a.overlays, newDayDetail(a, day))
		return nil
	}
	a.ana.pendingDetail = true
	return a.ana.load(a)
}
