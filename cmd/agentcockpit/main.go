package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is overridden at build time via -ldflags "-X main.version=x.y.z".
var version = "dev"

var rootCmd = &cobra.Command{
	Use:   "agentcockpit",
	Short: "AI session control plane — run and observe AI coding agents remotely",
}

func init() {
	// Host commands
	rootCmd.AddCommand(connectCmd)
	rootCmd.AddCommand(installCmd)
	rootCmd.AddCommand(uninstallCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(agentCmd)
	rootCmd.AddCommand(_daemonCmd) // hidden — called by launchd/systemd

	// Hook integration (optional — enables tool approval notifications)
	rootCmd.AddCommand(hookCmd)
	rootCmd.AddCommand(hooksCmd)

	// Server (self-hosted relay)
	rootCmd.AddCommand(serveCmd)

	// Admin (self-hosted server management)
	rootCmd.AddCommand(userCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
