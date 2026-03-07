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

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Run the host agent daemon",
	RunE:  runAgent,
}

func runAgent(cmd *cobra.Command, args []string) error {
	cfgPath, err := agentConfigPath()
	if err != nil {
		return err
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("not connected — run `agentcockpit connect` first")
		}
		return fmt.Errorf("read config: %w", err)
	}

	var cfg agent.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse config %s: %w", cfgPath, err)
	}
	if cfg.RelayURL == "" || cfg.Token == "" {
		return fmt.Errorf("invalid config — run `agentcockpit connect` again")
	}
	cfg.AgentVersion = version
	cfg.ConfigPath = cfgPath

	log.Printf("starting agent daemon (relay=%s name=%q)", cfg.RelayURL, cfg.Name)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-quit
		log.Printf("shutting down agent")
		cancel()
	}()

	err = agent.New(cfg).Run(ctx)
	if err == context.Canceled {
		return nil
	}
	return err
}

// agentConfigPath returns the path to agent.json, creating the dir if needed.
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
