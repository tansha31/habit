package theme

import (
	"fmt"
	"image/color"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestBundledThemesParseAndContrast guards against "washed out" palettes: every
// bundled theme must keep its foreground roles legible against its own bg.
// Thresholds are calibrated so upstream light and dark palettes pass, while a
// dark-theme pastel on a light bg (contrast ≈ 1.6–1.9) fails.
func TestBundledThemesParseAndContrast(t *testing.T) {
	entries, err := bundled.ReadDir("themes")
	if err != nil || len(entries) == 0 {
		t.Fatalf("no bundled themes: %v", err)
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".toml")
		t.Run(name, func(t *testing.T) {
			data, err := bundled.ReadFile("themes/" + e.Name())
			if err != nil {
				t.Fatal(err)
			}
			var p palette
			if err := toml.Unmarshal(data, &p); err != nil {
				t.Fatal(err)
			}
			fields := map[string]string{
				"accent": p.Accent, "accent_subtle": p.AccentSubtle,
				"fg": p.Fg, "fg_dim": p.FgDim, "fg_faint": p.FgFaint,
				"ok": p.Ok, "warn": p.Warn, "danger": p.Danger, "freeze": p.Freeze,
				"bg_raised": p.BgRaised, "bg": p.Bg,
			}
			for tok, hex := range fields {
				if len(hex) != 7 || hex[0] != '#' {
					t.Fatalf("%s: %s = %q, want #rrggbb", name, tok, hex)
				}
			}
			if p.BgRaised == p.Bg {
				t.Errorf("%s: bg_raised must differ from bg", name)
			}
			min := map[string]float64{
				"fg":     4.4,
				"fg_dim": 2.2,
				"accent": 2.0, "ok": 2.0, "warn": 2.0, "danger": 2.0, "freeze": 2.0,
			}
			for tok, want := range min {
				if got := contrast(fields[tok], p.Bg); got < want {
					t.Errorf("%s: contrast(%s %s, bg %s) = %.2f, want ≥ %.1f",
						name, tok, fields[tok], p.Bg, got, want)
				}
			}
			if _, err := Load(name, ""); err != nil {
				t.Errorf("Load(%s): %v", name, err)
			}
		})
	}
}

func hexOf(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
}

// TestAccentOverrideStaysLegible: any accent override — including every
// Settings preset, all of which are dark-theme pastels — must land at ≥3:1
// against every bundled theme's bg after Load's legibility clamp.
func TestAccentOverrideStaysLegible(t *testing.T) {
	// Mirrors ui.accentPresets (settings.go); "" = theme default.
	presets := []string{"", "#7aa2f7", "#bb9af7", "#9ece6a", "#f7768e", "#e0af68", "#7dcfff", "#fe8019"}
	entries, _ := bundled.ReadDir("themes")
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".toml")
		data, _ := bundled.ReadFile("themes/" + e.Name())
		var p palette
		if err := toml.Unmarshal(data, &p); err != nil {
			t.Fatal(err)
		}
		for _, preset := range presets {
			th, err := Load(name, preset)
			if err != nil {
				t.Fatalf("Load(%s, %q): %v", name, preset, err)
			}
			if got := contrast(hexOf(th.AccentColor), p.Bg); got < 3.0 {
				t.Errorf("%s accent %q → %s: contrast vs bg %s = %.2f, want ≥ 3.0",
					name, preset, hexOf(th.AccentColor), p.Bg, got)
			}
		}
	}
}
