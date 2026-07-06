//go:build windows

// Command tray is the Windows system-tray companion for timereporting-assistant.
// It starts at login (registered via a per-user registry Run key), shows a
// once-per-day reminder toast when worklogs are incomplete, and lets the user
// open the review UI or the mock Jira inspect page from the tray menu.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/kwkgaya/timereporting-assistant/internal/applog"
	"github.com/kwkgaya/timereporting-assistant/internal/config"
	"github.com/kwkgaya/timereporting-assistant/internal/trayapp"
)

// version is set at build time via -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	cfgPath := flag.String("config", config.DefaultPath(), "path to config JSON file")
	autoStartFlag := flag.String("autostart", "", "register|unregister autostart")
	flag.Parse()

	if *autoStartFlag != "" {
		switch *autoStartFlag {
		case "register":
			if err := trayapp.RegisterAutoStart(); err != nil {
				fmt.Fprintf(os.Stderr, "autostart register: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("Auto-start registered.")
		case "unregister":
			if err := trayapp.UnregisterAutoStart(); err != nil {
				fmt.Fprintf(os.Stderr, "autostart unregister: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("Auto-start removed.")
		default:
			fmt.Fprintf(os.Stderr, "autostart: must be register or unregister\n")
			os.Exit(1)
		}
		return
	}

	defer applog.Setup("tray")()
	trayapp.Run(version, *cfgPath)
}
