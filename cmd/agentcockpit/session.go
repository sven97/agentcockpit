package main

import (
	"github.com/spf13/cobra"
)

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Manage AI coding sessions",
}

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List sessions on this host",
	RunE:  runSessionList,
}

var sessionStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start a new session",
	RunE:  runSessionStart,
}

var sessionStopCmd = &cobra.Command{
	Use:   "stop <session-id>",
	Short: "Stop a running session",
	Args:  cobra.ExactArgs(1),
	RunE:  runSessionStop,
}

var (
	sessionAgent string
	sessionDir   string
	sessionName  string
)

func init() {
	sessionStartCmd.Flags().StringVar(&sessionAgent, "agent", "claude-code", "Agent type: claude-code | codex | opencode | custom")
	sessionStartCmd.Flags().StringVar(&sessionDir, "dir", ".", "Working directory")
	sessionStartCmd.Flags().StringVar(&sessionName, "name", "", "Session label")
	sessionCmd.AddCommand(sessionListCmd, sessionStartCmd, sessionStopCmd)
}

func runSessionList(cmd *cobra.Command, args []string) error {
	// TODO: query agent daemon Unix socket for active sessions
	return nil
}

func runSessionStart(cmd *cobra.Command, args []string) error {
	// TODO: send session_create to agent daemon
	return nil
}

func runSessionStop(cmd *cobra.Command, args []string) error {
	// TODO: send session_kill to agent daemon
	return nil
}
