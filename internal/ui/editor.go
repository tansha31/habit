package ui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"habit/internal/domain"
	"habit/internal/store"
	"habit/internal/ui/widgets"
)

// editorOverlay is the habit editor modal (§5.3): n = new, e = edit.
type editorOverlay struct {
	orig  *domain.Habit // nil when creating
	kind  domain.Kind
	sched domain.Schedule

	name, target, unit, perweek, tags, reminder textinput.Model
	groups                                      []domain.Group
	gIdx                                        int

	focus  int
	errTxt string
}

// Field order; hidden fields are skipped during navigation.
const (
	fName = iota
	fType
	fTarget
	fUnit
	fSched
	fPerWeek
	fGroup
	fTags
	fReminder
	fCount
)

func ti(value string, width int) textinput.Model {
	m := textinput.New()
	m.Prompt = ""
	m.SetWidth(width)
	m.SetValue(value)
	return m
}

func newEditor(a *App, h *domain.Habit) *editorOverlay {
	e := &editorOverlay{
		orig:   h,
		kind:   domain.Check,
		sched:  domain.Daily,
		groups: a.snap.Groups,
	}
	e.name = ti("", 28)
	e.target = ti("", 8)
	e.unit = ti("", 8)
	e.perweek = ti("3", 4)
	e.tags = ti("", 28)
	e.reminder = ti("", 8)
	if h != nil {
		e.kind, e.sched = h.Kind, h.Schedule
		e.name.SetValue(h.Name)
		if h.Target > 0 {
			e.target.SetValue(strconv.FormatFloat(h.Target, 'g', -1, 64))
		}
		e.unit.SetValue(h.Unit)
		if h.PerWeek > 0 {
			e.perweek.SetValue(strconv.Itoa(h.PerWeek))
		}
		if len(h.Tags) > 0 {
			e.tags.SetValue("#" + strings.Join(h.Tags, " #"))
		}
		e.reminder.SetValue(h.Reminder)
		for i, g := range e.groups {
			if g.ID == h.GroupID {
				e.gIdx = i
			}
		}
	}
	e.name.Focus()
	return e
}

func (e *editorOverlay) fieldVisible(f int) bool {
	switch f {
	case fTarget, fUnit:
		return e.kind == domain.Quantified
	case fPerWeek:
		return e.sched == domain.Weekly
	}
	return true
}

func (e *editorOverlay) inputFor(f int) *textinput.Model {
	switch f {
	case fName:
		return &e.name
	case fTarget:
		return &e.target
	case fUnit:
		return &e.unit
	case fPerWeek:
		return &e.perweek
	case fTags:
		return &e.tags
	case fReminder:
		return &e.reminder
	}
	return nil
}

func (e *editorOverlay) setFocus(f int) {
	for i := 0; i < fCount; i++ {
		if in := e.inputFor(i); in != nil {
			in.Blur()
		}
	}
	e.focus = f
	if in := e.inputFor(f); in != nil {
		in.Focus()
	}
}

func (e *editorOverlay) moveFocus(delta int) {
	f := e.focus
	for {
		f = (f + delta + fCount) % fCount
		if e.fieldVisible(f) {
			e.setFocus(f)
			return
		}
	}
}

func (e *editorOverlay) Update(msg tea.Msg, a *App) (Overlay, tea.Cmd) {
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return e, nil
	}
	switch {
	case key.Matches(kp, a.keys.Esc):
		return nil, nil
	case kp.String() == "enter":
		return e.save(a)
	case kp.String() == "tab" || kp.String() == "down":
		e.moveFocus(1)
		return e, nil
	case kp.String() == "shift+tab" || kp.String() == "up":
		e.moveFocus(-1)
		return e, nil
	}

	// Radios and cyclers.
	toggle := kp.String() == "space" || kp.String() == "h" || kp.String() == "l" ||
		kp.String() == "left" || kp.String() == "right"
	switch e.focus {
	case fType:
		if toggle {
			if e.kind == domain.Check {
				e.kind = domain.Quantified
			} else {
				e.kind = domain.Check
			}
		}
		return e, nil
	case fSched:
		if toggle {
			if e.sched == domain.Daily {
				e.sched = domain.Weekly
			} else {
				e.sched = domain.Daily
			}
		}
		return e, nil
	case fGroup:
		if toggle && len(e.groups) > 0 {
			dir := 1
			if kp.String() == "h" || kp.String() == "left" {
				dir = -1
			}
			e.gIdx = (e.gIdx + dir + len(e.groups)) % len(e.groups)
		}
		return e, nil
	}

	// Everything else types into the focused input.
	if in := e.inputFor(e.focus); in != nil {
		var cmd tea.Cmd
		*in, cmd = in.Update(msg)
		return e, cmd
	}
	return e, nil
}

var reminderRe = regexp.MustCompile(`^\d{1,2}:\d{2}$`)

// save validates, closes the modal on the same frame, and queues the write
// (§5.3: save is optimistic; the toast confirms).
func (e *editorOverlay) save(a *App) (Overlay, tea.Cmd) {
	name := strings.TrimSpace(e.name.Value())
	if name == "" {
		e.errTxt = "name is required"
		e.setFocus(fName)
		return e, nil
	}
	h := domain.Habit{Kind: e.kind, Schedule: e.sched, Name: name, Step: 1}
	if e.orig != nil {
		h = *e.orig
		h.Name, h.Kind, h.Schedule = name, e.kind, e.sched
	}
	if e.kind == domain.Quantified {
		t, err := strconv.ParseFloat(strings.TrimSpace(e.target.Value()), 64)
		if err != nil || t <= 0 {
			e.errTxt = "target must be a positive number"
			e.setFocus(fTarget)
			return e, nil
		}
		h.Target, h.Unit = t, strings.TrimSpace(e.unit.Value())
	} else {
		h.Target, h.Unit = 0, ""
	}
	if e.sched == domain.Weekly {
		n, err := strconv.Atoi(strings.TrimSpace(e.perweek.Value()))
		if err != nil || n < 1 || n > 7 {
			e.errTxt = "per week must be 1–7"
			e.setFocus(fPerWeek)
			return e, nil
		}
		h.PerWeek = n
	} else {
		h.PerWeek = 0
	}
	if r := strings.TrimSpace(e.reminder.Value()); r != "" && !reminderRe.MatchString(r) {
		e.errTxt = "reminder must be HH:MM"
		e.setFocus(fReminder)
		return e, nil
	} else {
		h.Reminder = r
	}
	h.Tags = nil
	for _, t := range strings.Fields(e.tags.Value()) {
		if t = strings.TrimPrefix(t, "#"); t != "" {
			h.Tags = append(h.Tags, t)
		}
	}
	if len(e.groups) > 0 {
		h.GroupID = e.groups[e.gIdx].ID
	}

	var toast string
	var mut tea.Cmd
	if e.orig == nil {
		toast = fmt.Sprintf("added %s · %s", h.Name, undoHint(a))
		mut = a.mutate(func(s *store.Store) error {
			hh := h
			return s.CreateHabit(&hh)
		})
	} else {
		toast = fmt.Sprintf("saved %s · %s", h.Name, undoHint(a))
		mut = a.mutate(func(s *store.Store) error { return s.UpdateHabit(h) })
	}
	return nil, tea.Batch(a.Toast(toast), mut)
}

func (e *editorOverlay) View(a *App) string {
	th := a.theme
	label := func(f int, s string) string {
		if f == e.focus {
			return th.Accent.Render(fmt.Sprintf("%-10s", s))
		}
		return th.Dim.Render(fmt.Sprintf("%-10s", s))
	}
	radio := func(on bool, s string) string {
		if on {
			return th.Text.Render("(•) " + s)
		}
		return th.Dim.Render("( ) " + s)
	}
	if a.gl.Spark == nil { // ASCII mode
		radio = func(on bool, s string) string {
			if on {
				return th.Text.Render("(*) " + s)
			}
			return th.Dim.Render("( ) " + s)
		}
	}

	var b strings.Builder
	row := func(s string) { b.WriteString("  " + s + "\n") }

	row(label(fName, "Name") + e.name.View())
	row("")
	row(label(fType, "Type") + radio(e.kind == domain.Check, "check") + "     " + radio(e.kind == domain.Quantified, "quantified"))
	if e.kind == domain.Quantified {
		row(label(fTarget, "Target") + e.target.View() + "  " + th.Dim.Render("unit") + "  " + e.unit.View())
	}
	row(label(fSched, "Schedule") + radio(e.sched == domain.Daily, "daily") + "     " + radio(e.sched == domain.Weekly, "N × per week"))
	if e.sched == domain.Weekly {
		row(label(fPerWeek, "Per week") + e.perweek.View())
	}
	row("")
	groupName := "—"
	if len(e.groups) > 0 {
		groupName = e.groups[e.gIdx].Name
	}
	gStyle := th.Text
	if e.focus == fGroup {
		gStyle = th.Accent
	}
	row(label(fGroup, "Group") + gStyle.Render("‹ "+groupName+" ›"))
	row(label(fTags, "Tags") + e.tags.View())
	row(label(fReminder, "Reminder") + e.reminder.View() + "   " + th.Faint.Render("requires habitd"))
	row("")
	footer := th.Faint.Render("esc cancel") + strings.Repeat(" ", 18) + th.Accent.Render("↵ save")
	if e.errTxt != "" {
		footer = th.Danger.Render(e.errTxt)
	}
	row(footer)

	title := " New Habit "
	if e.orig != nil {
		title = " Edit " + e.orig.Name + " "
	}
	box := lipgloss.NewStyle().
		Border(a.border).BorderForeground(th.AccentColor).
		Padding(0, 1).Render(strings.TrimRight(b.String(), "\n"))
	// Stamp the title into the top border.
	lines := strings.Split(box, "\n")
	if len(lines) > 0 {
		lines[0] = widgets.StampTitle(lines[0], th.Text.Render(title), 2)
	}
	return widgets.Shadowed(strings.Join(lines, "\n"), a.gl.Shadow, th.Faint)
}
