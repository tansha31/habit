// Package widgets holds the small pure render helpers: sparkline, progress
// bar, hairline rules, and the overlay compositor. No models, no state.
package widgets

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Sparkline renders one rune per value, scaled to the series max.
// Returns "" when spark is nil (ASCII mode drops sparklines, §3.6).
func Sparkline(vals []float64, spark []rune) string {
	if len(spark) == 0 || len(vals) == 0 {
		return ""
	}
	max := 0.0
	for _, v := range vals {
		if v > max {
			max = v
		}
	}
	var sb strings.Builder
	for _, v := range vals {
		i := 0
		if max > 0 {
			i = int(v / max * float64(len(spark)-1))
		}
		sb.WriteRune(spark[i])
	}
	return sb.String()
}

// Bar renders a day-progress bar: ▰▰▰▱▱.
func Bar(frac float64, width int, on, off string) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	n := int(frac*float64(width) + 0.5)
	return strings.Repeat(on, n) + strings.Repeat(off, width-n)
}

// Rule renders a group header: "TITLE ────────────── right" (§3.1). The
// styles are applied by the caller on title/right; line is drawn dim here.
func Rule(title, right string, width int, line string, dim lipgloss.Style) string {
	fill := width - lipgloss.Width(title) - lipgloss.Width(right) - 2
	if fill < 1 {
		fill = 1
	}
	return title + " " + dim.Render(strings.Repeat(line, fill)) + " " + right
}

// StampTitle splices a title into a box's top border at column x:
// ╭─ New Habit ─────╮
func StampTitle(topBorder, title string, x int) string {
	w := lipgloss.Width(title)
	return ansi.Truncate(topBorder, x, "") + title + ansi.TruncateLeft(topBorder, x+w, "")
}

// Shadowed adds a drop shadow to a bordered box: ░ down the right edge
// (from the second line) and along the bottom, offset one cell (§3.2).
func Shadowed(box string, shadow string, style lipgloss.Style) string {
	if shadow == "" {
		return box
	}
	lines := strings.Split(box, "\n")
	w := 0
	for _, l := range lines {
		if lw := lipgloss.Width(l); lw > w {
			w = lw
		}
	}
	sh := style.Render(shadow)
	for i, l := range lines {
		pad := strings.Repeat(" ", w-lipgloss.Width(l))
		if i == 0 {
			lines[i] = l + pad + " "
		} else {
			lines[i] = l + pad + sh
		}
	}
	lines = append(lines, " "+style.Render(strings.Repeat(shadow, w)))
	return strings.Join(lines, "\n")
}

// Compose centers box over base within a w×h cell canvas. The base is
// flattened to a dim, colorless layer (strip ANSI, restyle) so the overlay
// reads as floating over a 40%-dimmed surface (§5.2).
func Compose(base, box string, w, h int, dim lipgloss.Style) string {
	baseLines := strings.Split(base, "\n")
	for len(baseLines) < h {
		baseLines = append(baseLines, "")
	}
	baseLines = baseLines[:h]
	for i, l := range baseLines {
		plain := ansi.Strip(l)
		if lw := ansi.StringWidth(plain); lw < w {
			plain += strings.Repeat(" ", w-lw)
		}
		baseLines[i] = dim.Render(ansi.Truncate(plain, w, ""))
	}

	boxLines := strings.Split(box, "\n")
	bw := 0
	for _, l := range boxLines {
		if lw := lipgloss.Width(l); lw > bw {
			bw = lw
		}
	}
	x := (w - bw) / 2
	y := (h - len(boxLines)) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	for i, bl := range boxLines {
		if y+i >= h {
			break
		}
		pad := strings.Repeat(" ", bw-lipgloss.Width(bl))
		row := baseLines[y+i]
		baseLines[y+i] = ansi.Truncate(row, x, "") + bl + pad + ansi.TruncateLeft(row, x+bw, "")
	}
	return strings.Join(baseLines, "\n")
}
