// Package theme maps the semantic color tokens (spec §3.4) onto lipgloss
// styles, and owns the glyph/border fallback tables (§3.2–3.3). Components
// never touch raw hex values — only tokens. Color-profile degradation
// (truecolor → 256 → 16 → mono, NO_COLOR) is handled by the Bubble Tea v2
// renderer downsampling at output time.
package theme

import (
	"embed"
	"fmt"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/BurntSushi/toml"
)

//go:embed themes/*.toml
var bundled embed.FS

// UserThemeDir holds user palettes with the same TOML shape (§3.5).
func UserThemeDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "habit", "themes")
}

type palette struct {
	Name         string `toml:"name"`
	Accent       string `toml:"accent"`
	AccentSubtle string `toml:"accent_subtle"`
	Fg           string `toml:"fg"`
	FgDim        string `toml:"fg_dim"`
	FgFaint      string `toml:"fg_faint"`
	Ok           string `toml:"ok"`
	Warn         string `toml:"warn"`
	Danger       string `toml:"danger"`
	Freeze       string `toml:"freeze"`
	BgRaised     string `toml:"bg_raised"`
	Bg           string `toml:"bg"`
}

type Theme struct {
	Name                     string
	AccentColor, BgRaised    color.Color
	Bg                       color.Color // for background = "solid" mode
	Text, Dim, Faint         lipgloss.Style
	Accent, Subtle           lipgloss.Style
	Ok, Warn, Danger, Freeze lipgloss.Style
}

// Names lists bundled and user themes, sorted, deduped (user wins).
func Names() []string {
	seen := map[string]bool{}
	entries, _ := bundled.ReadDir("themes")
	for _, e := range entries {
		seen[strings.TrimSuffix(e.Name(), ".toml")] = true
	}
	userFiles, _ := os.ReadDir(UserThemeDir())
	for _, e := range userFiles {
		if strings.HasSuffix(e.Name(), ".toml") {
			seen[strings.TrimSuffix(e.Name(), ".toml")] = true
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// relLum is WCAG relative luminance of a #rrggbb color.
func relLum(hex string) float64 {
	lin := func(s string) float64 {
		v, _ := strconv.ParseUint(s, 16, 8)
		c := float64(v) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	if len(hex) != 7 {
		return 0
	}
	return 0.2126*lin(hex[1:3]) + 0.7152*lin(hex[3:5]) + 0.0722*lin(hex[5:7])
}

func contrast(a, b string) float64 {
	la, lb := relLum(a), relLum(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// legible blends hex toward black (light bg) or white (dark bg) until it
// reaches min contrast against bg, keeping the hue. An accent chosen for the
// opposite polarity (e.g. a dark-theme pastel on paper) stays recognizable
// instead of washing out.
func legible(hex, bg string, min float64) string {
	if len(hex) != 7 || len(bg) != 7 || contrast(hex, bg) >= min {
		return hex
	}
	target := 0.0 // blend toward black on a light bg
	if relLum(bg) < 0.5 {
		target = 255 // toward white on a dark bg
	}
	ch := func(s string, t float64) float64 {
		v, _ := strconv.ParseUint(s, 16, 8)
		return float64(v) + (target-float64(v))*t
	}
	for t := 0.05; t <= 1; t += 0.05 {
		mixed := fmt.Sprintf("#%02x%02x%02x",
			int(ch(hex[1:3], t)), int(ch(hex[3:5], t)), int(ch(hex[5:7], t)))
		if contrast(mixed, bg) >= min {
			return mixed
		}
	}
	return hex // degenerate bg (mid-gray); leave it alone
}

// Load resolves a theme by name — user dir first, then bundled. accent
// overrides the palette accent when non-empty (§3.5).
func Load(name, accent string) (Theme, error) {
	var p palette
	data, err := os.ReadFile(filepath.Join(UserThemeDir(), name+".toml"))
	if err != nil {
		if data, err = bundled.ReadFile("themes/" + name + ".toml"); err != nil {
			return Theme{}, fmt.Errorf("unknown theme %q", name)
		}
	}
	if err := toml.Unmarshal(data, &p); err != nil {
		return Theme{}, fmt.Errorf("theme %s: %w", name, err)
	}
	if accent != "" {
		p.Accent = accent
	}
	p.Accent = legible(p.Accent, p.Bg, 3.0) // §3.4: accent must never wash out
	c := func(hex string) color.Color { return lipgloss.Color(hex) }
	fg := func(hex string) lipgloss.Style { return lipgloss.NewStyle().Foreground(c(hex)) }
	return Theme{
		Name:        name,
		AccentColor: c(p.Accent),
		BgRaised:    c(p.BgRaised),
		Bg:          c(p.Bg),
		Text:        fg(p.Fg),
		Dim:         fg(p.FgDim),
		Faint:       fg(p.FgFaint),
		Accent:      fg(p.Accent),
		Subtle:      fg(p.AccentSubtle),
		Ok:          fg(p.Ok),
		Warn:        fg(p.Warn),
		Danger:      fg(p.Danger),
		Freeze:      fg(p.Freeze),
	}, nil
}

// Default never fails: tokyo-night is compiled in.
func Default() Theme {
	t, err := Load("tokyo-night", "")
	if err != nil {
		panic(err) // embed is broken — unreachable in a working build
	}
	return t
}

// Glyphs is the status/data glyph table with its ASCII fallback (§3.3).
type Glyphs struct {
	Logo, Done, Pending, Partial, Week, Skip, Freeze, Pause, Milestone string
	SelBar, HRule, TabRule, Shadow                                     string
	BarOn, BarOff                                                      string
	Edit, Focus, Prompt, Backfill                                      string
	Spark                                                              []rune // nil in ASCII mode: sparklines are omitted
	Heat                                                               []rune
}

func GlyphSet(ascii bool) Glyphs {
	if ascii {
		return Glyphs{
			Logo: "*", Done: "x", Pending: "o", Partial: "%", Week: "*",
			Skip: "-", Freeze: "#", Pause: "=", Milestone: "^",
			SelBar: ">", HRule: "-", TabRule: "=", Shadow: ":",
			BarOn: "#", BarOff: ".",
			Edit: "*", Focus: "@", Prompt: ">", Backfill: "<",
			Heat: []rune(".-=%#"),
		}
	}
	return Glyphs{
		Logo: "⬢", Done: "✓", Pending: "○", Partial: "◐", Week: "●",
		Skip: "✕", Freeze: "❄", Pause: "‖", Milestone: "◆",
		SelBar: "▌", HRule: "─", TabRule: "━", Shadow: "░",
		BarOn: "▰", BarOff: "▱",
		Edit: "✎", Focus: "◎", Prompt: "❯", Backfill: "↩",
		Spark: []rune("▁▂▃▄▅▆▇█"),
		Heat:  []rune("·░▒▓█"),
	}
}

// Border returns the overlay border set for a mode (§3.2).
func Border(mode string) lipgloss.Border {
	switch mode {
	case "square":
		return lipgloss.NormalBorder()
	case "ascii":
		return lipgloss.Border{
			Top: "-", Bottom: "-", Left: "|", Right: "|",
			TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+",
		}
	default:
		return lipgloss.RoundedBorder()
	}
}

// ASCIIOnly sniffs the locale; config can force ASCII regardless (M8).
func ASCIIOnly() bool {
	for _, v := range []string{os.Getenv("LC_ALL"), os.Getenv("LC_CTYPE"), os.Getenv("LANG")} {
		if v != "" {
			return !strings.Contains(strings.ToUpper(v), "UTF-8") && !strings.Contains(strings.ToUpper(v), "UTF8")
		}
	}
	return false // no locale set: assume UTF-8 (macOS default)
}
