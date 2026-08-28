package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"snapshot-agent/internal/config"
)

// taskName is how the login entry identifies itself to the OS.
const taskName = "snapshot-agent"

// cmdInstall registers the agent to start at login, so nobody has to remember
// to launch it. This is the one manual step there is --- the same shape as
// installing a WakaTime editor plugin once and never thinking about it again.
func cmdInstall(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating this binary: %w", err)
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}

	// A login item runs with no window, so a broken config would fail
	// silently at every login and look like the agent simply doing nothing.
	// Say so now, while there is a terminal to say it to.
	warnIfConfigUnusable()

	switch runtime.GOOS {
	case "windows":
		return installWindows(exe)
	default:
		printUnitFile(exe)
		return errReported
	}
}

// warnIfConfigUnusable reports a config that would stop `run` from starting.
// It warns rather than refuses: installing the login item before writing the
// config is a reasonable order to do things in.
func warnIfConfigUnusable() {
	path, err := configPath()
	if err != nil {
		return
	}
	if _, err := config.Load(path, true); err != nil {
		fmt.Printf("WARNING: the config is not usable yet, so the agent will\n")
		fmt.Printf("         exit silently at login until it is fixed:\n")
		fmt.Printf("           %v\n", err)
		fmt.Printf("         Check it with: snapshot-agent doctor\n\n")
	}
}

// cmdUninstall removes the login entry. It does not touch the config or the
// queue, so reinstalling picks up exactly where it left off.
func cmdUninstall(args []string) error {
	if runtime.GOOS != "windows" {
		fmt.Printf("Remove the service file you installed with `snapshot-agent install`.\n")
		return errReported
	}
	launcher, err := startupEntry()
	if err != nil {
		return err
	}
	if err := os.Remove(launcher); err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("nothing to remove: no login item at %s\n", launcher)
			return nil
		}
		return fmt.Errorf("removing %s: %w", launcher, err)
	}
	fmt.Printf("removed: the agent will no longer start at login\n")
	fmt.Printf("  a running agent keeps running --- stop it with Ctrl+C, or log out\n")
	return nil
}

// startupEntry is the per-user login item. The Startup folder is used rather
// than a scheduled task because `schtasks /sc onlogon` needs Administrator:
// an agent that watches your own folders has no business asking for that.
func startupEntry() (string, error) {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return "", fmt.Errorf("APPDATA is not set; cannot find the Startup folder")
	}
	return filepath.Join(appData, "Microsoft", "Windows", "Start Menu",
		"Programs", "Startup", taskName+".vbs"), nil
}

// installWindows drops a login item in the Startup folder. The agent is a
// console program, so launching the .exe directly would leave a terminal
// window open for as long as it runs; the one-line script below starts it
// with window style 0, which is hidden. Windows runs .vbs files in Startup
// through wscript automatically, so this needs no other moving parts.
func installWindows(exe string) error {
	launcher, err := startupEntry()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(launcher), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(launcher), err)
	}
	script := fmt.Sprintf("CreateObject(\"WScript.Shell\").Run \"\"\"%s\"\" run\", 0, False\r\n", exe)
	if err := os.WriteFile(launcher, []byte(script), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", launcher, err)
	}

	fmt.Printf("installed: starts at every login, hidden, no Administrator needed\n")
	fmt.Printf("  binary:   %s\n", exe)
	fmt.Printf("  login item: %s\n\n", launcher)
	fmt.Printf("Start it now without logging out:\n  wscript \"%s\"\n\n", launcher)
	fmt.Printf("Remove it with:\n  snapshot-agent uninstall\n")
	return nil
}

// printUnitFile writes out the service definition for platforms where there is
// no single command to install one, rather than guessing at the init system.
func printUnitFile(exe string) {
	if runtime.GOOS == "darwin" {
		fmt.Printf(`Save as ~/Library/LaunchAgents/com.snapshot-agent.plist, then:
  launchctl load ~/Library/LaunchAgents/com.snapshot-agent.plist

<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.snapshot-agent</string>
  <key>ProgramArguments</key><array><string>%s</string><string>run</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict></plist>
`, exe)
		return
	}
	fmt.Printf(`Save as ~/.config/systemd/user/snapshot-agent.service, then:
  systemctl --user enable --now snapshot-agent

[Unit]
Description=snapshot-agent

[Service]
ExecStart=%s run
Restart=always

[Install]
WantedBy=default.target
`, exe)
}
