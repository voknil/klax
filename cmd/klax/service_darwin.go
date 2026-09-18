//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
)

func runServiceStart() {
	if _, err := os.Stat(launchAgentPath()); err != nil {
		fmt.Fprintf(os.Stderr, "failed: LaunchAgent not installed\nTry 'klax install' first, or 'klax start --foreground'\n")
		os.Exit(1)
	}
	// Bootstrap loads the plist (RunAtLoad starts it); ignore "already loaded".
	exec.Command("launchctl", "bootstrap", launchdDomain(), launchAgentPath()).Run()
	// kickstart guarantees it is running even if it was loaded but stopped.
	cmd := exec.Command("launchctl", "kickstart", launchdTarget())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "failed: %v\nTry 'klax install' first, or 'klax start --foreground'\n", err)
		os.Exit(1)
	}
	fmt.Println("klax started")
}

func runServiceCtl(action string) {
	switch action {
	case "stop":
		// bootout unloads so KeepAlive does not relaunch — matches `systemctl stop`.
		runPassthrough(exec.Command("launchctl", "bootout", launchdTarget()))
	case "restart":
		runServiceRestart()
	default:
		runPassthrough(exec.Command("launchctl", action, launchdTarget()))
	}
}

// runServiceRestart restarts the agent the way `systemctl --user restart` does:
// it also starts one that is currently stopped. SIGTERM alone only works while
// the job is loaded — after `klax stop` (bootout) it fails, which used to leave
// the daemon silently down.
func runServiceRestart() {
	// SIGTERM lets the daemon drain in-flight work (its signal handler turns it
	// into a graceful drain); KeepAlive then relaunches it.
	if err := exec.Command("launchctl", "kill", "SIGTERM", launchdTarget()).Run(); err == nil {
		fmt.Println("klax restarted")
		return
	}
	// Not loaded (or not running): load the plist and make sure it is up.
	if _, err := os.Stat(launchAgentPath()); err != nil {
		fmt.Fprintf(os.Stderr, "failed: LaunchAgent not installed\nTry 'klax install' first, or 'klax start --foreground'\n")
		os.Exit(1)
	}
	exec.Command("launchctl", "bootstrap", launchdDomain(), launchAgentPath()).Run()
	cmd := exec.Command("launchctl", "kickstart", launchdTarget())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "failed: %v\nTry 'klax start --foreground'\n", err)
		os.Exit(1)
	}
	fmt.Println("klax restarted")
}

func runPassthrough(cmd *exec.Cmd) {
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()
}

func runStatus() {
	cmd := exec.Command("launchctl", "print", launchdTarget())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()
}
