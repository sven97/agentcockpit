package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/sven97/agentcockpit/internal/relay"
	"github.com/sven97/agentcockpit/internal/server"
	"github.com/sven97/agentcockpit/internal/store"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the relay server and web UI",
	RunE:  runServe,
}

var serveLocal bool

func init() {
	serveCmd.Flags().BoolVar(&serveLocal, "local", false, "Local mode: no auth, implicit single user")
}

func runServe(cmd *cobra.Command, args []string) error {
	dataDir := envOr("DATA_DIR", defaultDataDir())
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}

	dbPath := filepath.Join(dataDir, "agentcockpit.db")
	log.Printf("opening database: %s", dbPath)
	st, err := store.NewSQLite(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	hub := relay.NewHub(st)

	cfg := server.Config{
		Addr:      ":" + envOr("PORT", "7080"),
		Secret:    envOr("AGENTCOCKPIT_SECRET", "dev-secret-change-me"),
		LocalMode: serveLocal,
		DataDir:   dataDir,
		Version:   version,
	}

	srv := server.New(cfg, st, hub)

	// Wire the approval persist callback to avoid import cycle.
	hub.SetApprovalPersistFn(srv.ApprovalPersistFn())

	return srv.Start()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/agentcockpit"
	}
	return filepath.Join(home, ".local", "share", "agentcockpit")
}
