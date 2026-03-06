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
	Short: "Centralized AI coding session manager",
}

func init() {
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(connectCmd)
	rootCmd.AddCommand(agentCmd)
	rootCmd.AddCommand(hookCmd)
	rootCmd.AddCommand(hooksCmd)
	rootCmd.AddCommand(sessionCmd)
	rootCmd.AddCommand(userCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
