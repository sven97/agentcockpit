package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var hooksCmd = &cobra.Command{
	Use:   "hooks",
	Short: "Manage AI agent hook integrations (tool approval notifications)",
}

var hooksSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Inject AgentCockpit as a PreToolUse hook in Claude Code",
	RunE:  runHooksSetup,
}

var hooksRemoveCmd = &cobra.Command{
	Use:   "remove",
	Short: "Remove AgentCockpit hooks from Claude Code settings",
	RunE:  runHooksRemove,
}

var hooksStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current hook installation state",
	RunE:  runHooksStatus,
}

var (
	hooksYes   bool
	hooksScope string // "user" | "project"
)

func init() {
	hooksSetupCmd.Flags().BoolVarP(&hooksYes, "yes", "y", false, "Apply without prompting")
	hooksSetupCmd.Flags().StringVar(&hooksScope, "scope", "user", "Settings scope: user or project")
	hooksRemoveCmd.Flags().BoolVarP(&hooksYes, "yes", "y", false, "Apply without prompting")
	hooksRemoveCmd.Flags().StringVar(&hooksScope, "scope", "user", "Settings scope: user or project")
	hooksCmd.AddCommand(hooksSetupCmd, hooksRemoveCmd, hooksStatusCmd)
}

// ── Setup ─────────────────────────────────────────────────────────────────────

func runHooksSetup(cmd *cobra.Command, args []string) error {
	settingsPath, err := claudeSettingsPath(hooksScope)
	if err != nil {
		return err
	}

	binPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot determine binary path: %w", err)
	}

	// Load existing settings (create empty if missing).
	before, err := loadJSON(settingsPath)
	if err != nil {
		return err
	}

	// Check if already installed.
	if hookInstalled(before, binPath) {
		fmt.Printf("Hook already installed in %s\n", settingsPath)
		return nil
	}

	after := addHook(before, binPath)

	beforeStr := prettyJSON(before)
	afterStr := prettyJSON(after)

	if beforeStr == afterStr {
		fmt.Println("No changes needed.")
		return nil
	}

	fmt.Printf("Settings file: %s\n\n", settingsPath)
	printDiff(beforeStr, afterStr)

	if !hooksYes {
		if !confirm("Apply these changes?") {
			fmt.Println("Aborted.")
			return nil
		}
	}

	// Backup original if it existed.
	if _, statErr := os.Stat(settingsPath); statErr == nil {
		backupPath := settingsPath + ".bak"
		if err := os.WriteFile(backupPath, []byte(beforeStr), 0600); err != nil {
			return fmt.Errorf("backup failed: %w", err)
		}
		fmt.Printf("Backup saved to %s\n", backupPath)
	}

	if err := os.MkdirAll(filepath.Dir(settingsPath), 0700); err != nil {
		return fmt.Errorf("create settings dir: %w", err)
	}
	if err := os.WriteFile(settingsPath, []byte(afterStr+"\n"), 0600); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	fmt.Printf("Hook installed. Tool calls will now send approval notifications to AgentCockpit.\n")
	return nil
}

// ── Remove ────────────────────────────────────────────────────────────────────

func runHooksRemove(cmd *cobra.Command, args []string) error {
	settingsPath, err := claudeSettingsPath(hooksScope)
	if err != nil {
		return err
	}

	before, err := loadJSON(settingsPath)
	if err != nil {
		return err
	}

	after, removed := removeHook(before)
	if !removed {
		fmt.Println("No AgentCockpit hook found — nothing to remove.")
		return nil
	}

	beforeStr := prettyJSON(before)
	afterStr := prettyJSON(after)

	fmt.Printf("Settings file: %s\n\n", settingsPath)
	printDiff(beforeStr, afterStr)

	if !hooksYes {
		if !confirm("Apply these changes?") {
			fmt.Println("Aborted.")
			return nil
		}
	}

	if err := os.WriteFile(settingsPath, []byte(afterStr+"\n"), 0600); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	fmt.Println("Hook removed.")
	return nil
}

// ── Status ────────────────────────────────────────────────────────────────────

func runHooksStatus(cmd *cobra.Command, args []string) error {
	binPath, _ := os.Executable()
	for _, scope := range []string{"user", "project"} {
		path, err := claudeSettingsPath(scope)
		if err != nil {
			continue
		}
		settings, err := loadJSON(path)
		if os.IsNotExist(err) {
			fmt.Printf("[%s] %s — not found\n", scope, path)
			continue
		}
		if err != nil {
			fmt.Printf("[%s] %s — read error: %v\n", scope, path, err)
			continue
		}
		if hookInstalled(settings, "") {
			fmt.Printf("[%s] %s — ✓ installed\n", scope, path)
		} else if binPath != "" && hookInstalled(settings, binPath) {
			fmt.Printf("[%s] %s — ✓ installed\n", scope, path)
		} else {
			fmt.Printf("[%s] %s — not installed\n", scope, path)
		}
	}
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// claudeSettingsPath returns the Claude Code settings.json path for the given scope.
func claudeSettingsPath(scope string) (string, error) {
	switch scope {
	case "project":
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".claude", "settings.json"), nil
	default: // "user"
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".claude", "settings.json"), nil
	}
}

// hookCommand returns the command string for the hook entry.
func hookCommand(binPath string) string {
	if binPath == "" {
		return "agentcockpit hook"
	}
	return binPath + " hook"
}

// hookInstalled checks whether our hook entry is already present.
func hookInstalled(settings map[string]any, binPath string) bool {
	cmd := hookCommand(binPath)
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return false
	}
	preToolUse, _ := hooks["PreToolUse"].([]any)
	for _, entry := range preToolUse {
		m, _ := entry.(map[string]any)
		if m == nil {
			continue
		}
		inner, _ := m["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if hm == nil {
				continue
			}
			if hm["command"] == cmd || strings.HasSuffix(fmt.Sprint(hm["command"]), " hook") {
				return true
			}
		}
	}
	return false
}

// addHook merges our PreToolUse entry into settings and returns the new map.
func addHook(settings map[string]any, binPath string) map[string]any {
	// Deep-copy via JSON round-trip to avoid mutating the original.
	out := jsonClone(settings)

	hooksSection, _ := out["hooks"].(map[string]any)
	if hooksSection == nil {
		hooksSection = map[string]any{}
	}

	preToolUse, _ := hooksSection["PreToolUse"].([]any)

	entry := map[string]any{
		"matcher": "",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": hookCommand(binPath),
			},
		},
	}
	preToolUse = append(preToolUse, entry)
	hooksSection["PreToolUse"] = preToolUse
	out["hooks"] = hooksSection
	return out
}

// removeHook removes all AgentCockpit hook entries and returns (newSettings, wasRemoved).
func removeHook(settings map[string]any) (map[string]any, bool) {
	out := jsonClone(settings)
	hooksSection, _ := out["hooks"].(map[string]any)
	if hooksSection == nil {
		return out, false
	}
	preToolUse, _ := hooksSection["PreToolUse"].([]any)
	if len(preToolUse) == 0 {
		return out, false
	}

	var kept []any
	removed := false
	for _, entry := range preToolUse {
		m, _ := entry.(map[string]any)
		if m == nil {
			kept = append(kept, entry)
			continue
		}
		inner, _ := m["hooks"].([]any)
		var keptInner []any
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if hm == nil {
				keptInner = append(keptInner, h)
				continue
			}
			if strings.HasSuffix(fmt.Sprint(hm["command"]), " hook") {
				removed = true
			} else {
				keptInner = append(keptInner, h)
			}
		}
		if len(keptInner) > 0 {
			m["hooks"] = keptInner
			kept = append(kept, m)
		} else if !removed {
			kept = append(kept, m)
		}
	}
	hooksSection["PreToolUse"] = kept
	if len(kept) == 0 {
		delete(hooksSection, "PreToolUse")
	}
	if len(hooksSection) == 0 {
		delete(out, "hooks")
	}
	return out, removed
}

// loadJSON reads a JSON file into a map. Returns empty map if file doesn't exist.
func loadJSON(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, nil
}

func prettyJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func jsonClone(v map[string]any) map[string]any {
	b, _ := json.Marshal(v)
	var out map[string]any
	json.Unmarshal(b, &out)
	return out
}

// printDiff prints a simple +/- line diff between two multi-line strings.
func printDiff(before, after string) {
	bLines := strings.Split(before, "\n")
	aLines := strings.Split(after, "\n")
	bSet := make(map[string]bool, len(bLines))
	aSet := make(map[string]bool, len(aLines))
	for _, l := range bLines {
		bSet[l] = true
	}
	for _, l := range aLines {
		aSet[l] = true
	}
	fmt.Println("--- before")
	fmt.Println("+++ after")
	for _, l := range bLines {
		if !aSet[l] {
			fmt.Println("- " + l)
		}
	}
	for _, l := range aLines {
		if !bSet[l] {
			fmt.Println("+ " + l)
		}
	}
	fmt.Println()
}

func confirm(prompt string) bool {
	fmt.Printf("%s [y/N] ", prompt)
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		ans := strings.TrimSpace(strings.ToLower(scanner.Text()))
		return ans == "y" || ans == "yes"
	}
	return false
}
