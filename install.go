package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"snapshot-agent/internal/config"
)

// taskName is how the login entry identifies itself to the OS.
const taskName = "snapshot-agent"

// launchdLabel names the macOS LaunchAgent.
const launchdLabel = "com.snapshot-agent"

// cmdInstall registers the agent to start at login, so nobody has to remember
// to launch it --- the same shape as installing a WakaTime editor plugin once
// and never thinking about it again.
func cmdInstall(args []string) error {
	exe, err := selfPath()
	if err != nil {
		return err
	}

	// A login item runs with no window, so a broken config would fail
	// silently at every login and look like the agent simply doing nothing.
	// Say so now, while there is a terminal to say it to.
	warnIfConfigUnusable()

	return installLoginItem(exe)
}

func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locating this binary: %w", err)
	}
	return filepath.Abs(exe)
}

// installLoginItem registers exe to run at login and starts it now, without
// Administrator or sudo on any platform.
func installLoginItem(exe string) error {
	switch runtime.GOOS {
	case "windows":
		return installWindows(exe)
	case "darwin":
		return installLaunchd(exe)
	case "linux":
		return installSystemd(exe)
	default:
		fmt.Printf("no login-item support on %s; start `%s run` however you start daemons\n", runtime.GOOS, exe)
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

// cmdUninstall removes the login entry and stops the background agent where
// the OS manages it. It does not touch the config or the queue, so
// reinstalling picks up exactly where it left off.
func cmdUninstall(args []string) error {
	switch runtime.GOOS {
	case "windows":
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
		fmt.Printf("  a running agent keeps running --- stop it in Task Manager, or log out\n")
		return nil
	case "darwin":
		plist, err := launchdPlist()
		if err != nil {
			return err
		}
		_ = exec.Command("launchctl", "unload", "-w", plist).Run()
		return removeLoginFile(plist)
	case "linux":
		unit, err := systemdUnit()
		if err != nil {
			return err
		}
		_ = exec.Command("systemctl", "--user", "disable", "--now", taskName+".service").Run()
		return removeLoginFile(unit)
	default:
		fmt.Printf("nothing installed on %s\n", runtime.GOOS)
		return nil
	}
}

func removeLoginFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing %s: %w", path, err)
	}
	fmt.Printf("removed: the agent is stopped and will no longer start at login\n")
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

// installWindows drops a login item in the Startup folder and runs it once so
// the agent is going now, not only after the next login. The agent is a
// console program, so launching the .exe directly would leave a terminal
// window open for as long as it runs; the one-line script below starts it
// with window style 0, which is hidden. Windows runs .vbs files in Startup
// through wscript automatically, so this needs no other moving parts.
//
// Starting it twice is harmless: the second copy cannot lock the queue file
// and exits.
func installWindows(exe string) error {
	launcher, err := startupEntry()
	if err != nil {
		return err
	}
	script := fmt.Sprintf("CreateObject(\"WScript.Shell\").Run \"\"\"%s\"\" run\", 0, False\r\n", exe)
	if err := writeLoginFile(launcher, script); err != nil {
		return err
	}
	if err := exec.Command("wscript", launcher).Start(); err != nil {
		return fmt.Errorf("starting the agent: %w", err)
	}

	fmt.Printf("installed: running now, and at every login, hidden\n")
	fmt.Printf("  binary:     %s\n", exe)
	fmt.Printf("  login item: %s\n", launcher)
	return nil
}

func launchdPlist() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"), nil
}

// installLaunchd writes a per-user LaunchAgent and (re)loads it, which starts
// the agent now and at every login. Output goes to a log file, since a
// LaunchAgent has no terminal.
func installLaunchd(exe string) error {
	plist, err := launchdPlist()
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	logPath := filepath.Join(home, "Library", "Logs", taskName+".log")
	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array><string>%s</string><string>run</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, launchdLabel, exe, logPath, logPath)
	if err := writeLoginFile(plist, body); err != nil {
		return err
	}
	// Unload first so a reinstall picks up a moved or upgraded binary.
	_ = exec.Command("launchctl", "unload", plist).Run()
	if out, err := exec.Command("launchctl", "load", "-w", plist).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load: %v: %s", err, out)
	}
	fmt.Printf("installed: running now, and at every login\n")
	fmt.Printf("  binary: %s\n  agent:  %s\n  log:    %s\n", exe, plist, logPath)
	return nil
}

func systemdUnit() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "systemd", "user", taskName+".service"), nil
}

// installSystemd writes a user unit and enables it, which starts the agent
// now and at every login. Logs go to the journal.
func installSystemd(exe string) error {
	unit, err := systemdUnit()
	if err != nil {
		return err
	}
	body := fmt.Sprintf(`[Unit]
Description=snapshot-agent (flockatime)

[Service]
ExecStart="%s" run
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
`, exe)
	if err := writeLoginFile(unit, body); err != nil {
		return err
	}
	steps := [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", taskName + ".service"},
		// restart, not start: a reinstall must pick up the new binary.
		{"systemctl", "--user", "restart", taskName + ".service"},
	}
	for _, s := range steps {
		if out, err := exec.Command(s[0], s[1:]...).CombinedOutput(); err != nil {
			fmt.Printf("wrote %s, but `%s` failed: %v\n%s\n", unit, strings.Join(s, " "), err, out)
			fmt.Printf("without a systemd user session, start it yourself: %s run &\n", exe)
			return errReported
		}
	}
	fmt.Printf("installed: running now, and at every login\n")
	fmt.Printf("  binary: %s\n  unit:   %s\n  logs:   journalctl --user -u %s\n", exe, unit, taskName)
	return nil
}

func writeLoginFile(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
