package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/sven97/agentcockpit/internal/agent"
)

// _daemonCmd is the internal command invoked by launchd / systemd.
// It is intentionally hidden — users never run this directly.
var _daemonCmd = &cobra.Command{
	Use:    "_daemon",
	Hidden: true,
	Short:  "Run the host daemon (called by the system service, not by users)",
	RunE:   runDaemon,
}

func runDaemon(_ *cobra.Command, _ []string) error {
	cfgPath, err := agentConfigPath()
	if err != nil {
		return err
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return fmt.Errorf("not configured — run `agentcockpit install` first")
	}

	var cfg agent.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("corrupt config — run `agentcockpit install` again")
	}
	cfg.AgentVersion = version
	cfg.ConfigPath = cfgPath

	log.Printf("starting daemon (relay=%s name=%q)", cfg.RelayURL, cfg.Name)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-quit; cancel() }()

	if err := agent.New(cfg).Run(ctx); err != nil && err != context.Canceled {
		return err
	}
	return nil
}

// ── Config path helpers (shared with install.go) ──────────────────────────────

func agentConfigPath() (string, error) {
	dir, err := agentConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "agent.json"), nil
}

func agentConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "agentcockpit")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}
	return dir, nil
}
