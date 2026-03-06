package main

import (
	"github.com/spf13/cobra"
)

var hooksCmd = &cobra.Command{
	Use:   "hooks",
	Short: "Manage Claude Code / Codex hook installations",
}

var hooksSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Inject AgentCockpit as a PreToolUse hook",
	RunE:  runHooksSetup,
}

var hooksRemoveCmd = &cobra.Command{
	Use:   "remove",
	Short: "Remove AgentCockpit hooks and restore backups",
	RunE:  runHooksRemove,
}

var hooksStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current hook installation state",
	RunE:  runHooksStatus,
}

var hooksYes bool

func init() {
	hooksSetupCmd.Flags().BoolVar(&hooksYes, "yes", false, "Apply without prompting")
	hooksCmd.AddCommand(hooksSetupCmd, hooksRemoveCmd, hooksStatusCmd)
}

func runHooksSetup(cmd *cobra.Command, args []string) error {
	// TODO:
	// 1. Detect ~/.claude/settings.json
	// 2. Compute diff adding PreToolUse hook entry
	// 3. Show diff, prompt unless --yes
	// 4. Backup original to .bak
	// 5. Write updated settings
	// 6. Report hook_install to daemon → server
	return nil
}

func runHooksRemove(cmd *cobra.Command, args []string) error {
	// TODO: restore from .bak or remove hook entry
	return nil
}

func runHooksStatus(cmd *cobra.Command, args []string) error {
	// TODO: show which agent configs have hooks installed
	return nil
}
