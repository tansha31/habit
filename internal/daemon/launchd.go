package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const label = "com.habit.habitd"

func PlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

const plistTmpl = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>daemon</string>
		<string>run</string>
	</array>
	<key>StartInterval</key><integer>1800</integer>
	<key>RunAtLoad</key><true/>
</dict>
</plist>
`

// Install writes the LaunchAgent plist pointing at this binary and loads it.
func Install() error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(PlistPath()), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(PlistPath(), fmt.Appendf(nil, plistTmpl, label, bin), 0o644); err != nil {
		return err
	}
	exec.Command("launchctl", "unload", PlistPath()).Run() // reload if present
	return exec.Command("launchctl", "load", "-w", PlistPath()).Run()
}

func Remove() error {
	exec.Command("launchctl", "unload", "-w", PlistPath()).Run()
	err := os.Remove(PlistPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func Status() string {
	if _, err := os.Stat(PlistPath()); err != nil {
		return "not installed"
	}
	if exec.Command("launchctl", "list", label).Run() == nil {
		return "installed · loaded (every 30 min)"
	}
	return "installed · not loaded"
}
