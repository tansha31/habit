package theme

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// lum is WCAG relative luminance of a #rrggbb color.
func lum(t *testing.T, hex string) float64 {
	t.Helper()
	lin := func(s string) float64 {
		v, err := strconv.ParseUint(s, 16, 8)
		if err != nil {
			t.Fatalf("bad hex %q: %v", hex, err)
		}
		c := float64(v) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	if len(hex) != 7 || hex[0] != '#' {
		t.Fatalf("bad hex %q", hex)
	}
	return 0.2126*lin(hex[1:3]) + 0.7152*lin(hex[3:5]) + 0.0722*lin(hex[5:7])
}

func contrast(t *testing.T, a, b string) float64 {
	la, lb := lum(t, a), lum(t, b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

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
				if got := contrast(t, fields[tok], p.Bg); got < want {
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
