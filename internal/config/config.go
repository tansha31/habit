// Package config owns ~/.config/habit/config.toml — human-editable, hot
// reloaded by the TUI, and the source of truth Settings merely edits (§5.5).
package config

import (
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Theme          string `toml:"theme"`
	Accent         string `toml:"accent"`     // hex override; "" = theme default
	Background     string `toml:"background"` // "terminal" | "solid"
	Borders        string `toml:"borders"`    // "rounded" | "square" | "ascii"
	RolloverHour   int    `toml:"rollover_hour"`
	WeekStart      string `toml:"week_start"` // "monday" | "sunday"
	FreezeTokens   bool   `toml:"freeze_tokens"`
	MilestoneMarks bool   `toml:"milestone_marks"`
	AtRiskNudge    bool   `toml:"at_risk_nudge"`
	QuietFrom      string `toml:"quiet_from"` // habitd quiet hours
	QuietTo        string `toml:"quiet_to"`
}

func Default() Config {
	return Config{
		Theme:          "auto", // light/dark default picked from the terminal background
		Background:     "terminal",
		Borders:        "rounded",
		RolloverHour:   3,
		WeekStart:      "monday",
		FreezeTokens:   true,
		MilestoneMarks: true,
		AtRiskNudge:    true,
		QuietFrom:      "22:00",
		QuietTo:        "08:00",
	}
}

func Path() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "habit", "config.toml")
}

// Load reads the config, filling gaps with defaults; a missing file is not
// an error — first run simply gets the defaults.
func Load() (Config, error) {
	c := Default()
	data, err := os.ReadFile(Path())
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	err = toml.Unmarshal(data, &c)
	return c, err
}

func (c Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		return err
	}
	f, err := os.Create(Path())
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(c)
}

func (c Config) WeekStartDay() time.Weekday {
	if c.WeekStart == "sunday" {
		return time.Sunday
	}
	return time.Monday
}
