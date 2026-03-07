package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/sven97/agentcockpit/internal/agent"
)

var connectCmd = &cobra.Command{
	Use:   "connect",
	Short: "Connect this machine to AgentCockpit",
	RunE:  runConnect,
}

var connectRelay, connectInvite string

func init() {
	connectCmd.Flags().StringVar(&connectRelay, "relay", "https://agentcockpit.io", "Relay server base URL")
	connectCmd.Flags().StringVar(&connectInvite, "invite", "", "Invite token generated from the dashboard (skips browser step)")
}

func runConnect(cmd *cobra.Command, args []string) error {
	relay := strings.TrimRight(connectRelay, "/")

	if connectInvite != "" {
		return runConnectInvite(relay, connectInvite)
	}
	return runConnectDevice(relay)
}

// runConnectInvite claims a pre-generated invite token — no browser needed.
func runConnectInvite(relay, inviteToken string) error {
	fmt.Printf("\n  AgentCockpit — Connecting %s\n\n", hostname())

	body := fmt.Sprintf(`{"invite_token":%q,"name":%q,"hostname":%q,"platform":%q}`,
		inviteToken, hostname(), hostname(), runtime.GOOS)
	resp, err := http.Post(relay+"/api/hosts/claim", "application/json", strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("cannot reach relay %s: %w", relay, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("claim failed (%s) — token may be expired or already used", resp.Status)
	}

	var cr struct {
		HostToken string `json:"host_token"`
		HostID    string `json:"host_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return fmt.Errorf("decode claim response: %w", err)
	}

	return writeAgentConfig(relay, cr.HostToken)
}

// runConnectDevice is the original browser-based device authorization flow.
func runConnectDevice(relay string) error {
	resp, err := http.Post(relay+"/api/device/request", "application/json",
		strings.NewReader(fmt.Sprintf(`{"hostname":%q,"platform":%q}`,
			hostname(), runtime.GOOS)))
	if err != nil {
		return fmt.Errorf("cannot reach relay %s: %w", relay, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("relay returned %s", resp.Status)
	}

	var dr struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		VerifyURL  string `json:"verify_url"`
		ExpiresIn  int    `json:"expires_in"`
		Interval   int    `json:"interval"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		return fmt.Errorf("decode device response: %w", err)
	}

	fmt.Printf("\n  AgentCockpit — Connect this machine\n\n")
	fmt.Printf("  1. Open this URL in your browser:\n\n")
	fmt.Printf("       %s\n\n", dr.VerifyURL)
	fmt.Printf("  2. Enter code:  %s\n\n", dr.UserCode)

	if opened := openBrowser(dr.VerifyURL); opened {
		fmt.Printf("  (browser opened automatically)\n\n")
	}
	fmt.Printf("  Waiting for authorization")

	interval := time.Duration(dr.Interval) * time.Second
	if interval < time.Second {
		interval = 3 * time.Second
	}
	deadline := time.Now().Add(time.Duration(dr.ExpiresIn) * time.Second)

	var hostToken string
	for time.Now().Before(deadline) {
		time.Sleep(interval)
		fmt.Print(".")
		token, done, err := pollDeviceToken(relay, dr.DeviceCode)
		if err != nil {
			continue
		}
		if done {
			hostToken = token
			break
		}
	}

	if hostToken == "" {
		fmt.Println()
		return fmt.Errorf("authorization timed out — run `agentcockpit connect` again")
	}
	fmt.Println(" authorized!")

	return writeAgentConfig(relay, hostToken)
}

func writeAgentConfig(relay, hostToken string) error {
	cfgPath, err := agentConfigPath()
	if err != nil {
		return err
	}
	cfg := agent.Config{
		RelayURL: relay,
		Token:    hostToken,
		Name:     hostname(),
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(cfgPath, data, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	fmt.Printf("\n  Config saved to %s\n", cfgPath)
	fmt.Printf("  Starting agent...\n\n")
	return nil
}

// pollDeviceToken polls /api/device/token. Returns (token, true, nil) when
// authorized, ("", false, nil) when still pending, ("", false, err) on error.
func pollDeviceToken(relay, deviceCode string) (string, bool, error) {
	resp, err := http.Get(relay + "/api/device/token?device_code=" + deviceCode)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusAccepted {
		return "", false, nil // still pending
	}
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("poll returned %s", resp.Status)
	}

	var pr struct {
		Status    string `json:"status"`
		HostToken string `json:"host_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return "", false, err
	}
	if pr.Status == "authorized" && pr.HostToken != "" {
		return pr.HostToken, true, nil
	}
	return "", false, nil
}

// openBrowser attempts to open a URL in the default browser.
// Returns true if it likely succeeded.
func openBrowser(url string) bool {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd, args = "open", []string{url}
	case "linux":
		cmd, args = "xdg-open", []string{url}
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		return false
	}
	return exec.Command(cmd, args...).Start() == nil
}

func hostname() string {
	h, _ := os.Hostname()
	if h == "" {
		return "unknown"
	}
	return h
}
