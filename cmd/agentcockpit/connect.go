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
	Short: "Connect this machine to AgentCockpit (device authorization flow)",
	RunE:  runConnect,
}

var connectRelay string

func init() {
	connectCmd.Flags().StringVar(&connectRelay, "relay", "https://agentcockpit.io", "Relay server base URL")
}

func runConnect(cmd *cobra.Command, args []string) error {
	relay := strings.TrimRight(connectRelay, "/")

	// 1. Request a device code from the relay.
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

	// 2. Prompt user to open browser.
	fmt.Printf("\n  AgentCockpit — Connect this machine\n\n")
	fmt.Printf("  1. Open this URL in your browser:\n\n")
	fmt.Printf("       %s\n\n", dr.VerifyURL)
	fmt.Printf("  2. Enter code:  %s\n\n", dr.UserCode)

	// Try to open the browser automatically.
	if opened := openBrowser(dr.VerifyURL); opened {
		fmt.Printf("  (browser opened automatically)\n\n")
	}
	fmt.Printf("  Waiting for authorization")

	// 3. Poll until authorized or expired.
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
			// Non-fatal poll error — keep trying.
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

	// 4. Write ~/.config/agentcockpit/agent.json
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

	fmt.Printf("\n  Config saved to %s\n\n", cfgPath)
	fmt.Printf("  Start the agent daemon:\n\n")
	fmt.Printf("    agentcockpit agent\n\n")
	fmt.Printf("  Then install Claude Code hooks:\n\n")
	fmt.Printf("    agentcockpit hooks setup\n\n")
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
