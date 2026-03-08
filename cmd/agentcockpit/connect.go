package main

import (
	"strings"

	"github.com/spf13/cobra"
)

var connectCmd = &cobra.Command{
	Use:        "connect",
	Short:      "Authorize this machine with AgentCockpit",
	Deprecated: "use `agentcockpit install` (or `agentcockpit install --invite <token>`) instead",
	RunE:       runConnect,
}

var connectRelay, connectInvite string

func init() {
	connectCmd.Flags().StringVar(&connectRelay, "relay", "https://agentcockpit.io", "Relay server URL")
	connectCmd.Flags().StringVar(&connectInvite, "invite", "", "Invite token from the dashboard (skips browser step)")
}

func runConnect(_ *cobra.Command, _ []string) error {
	relay := strings.TrimRight(connectRelay, "/")
	if connectInvite != "" {
		return runConnectInvite(relay, connectInvite)
	}
	return runConnectDevice(relay)
}
