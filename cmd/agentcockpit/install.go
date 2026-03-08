package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/sven97/agentcockpit/internal/agent"
)

// ── install ───────────────────────────────────────────────────────────────────

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Authorize this machine and install the background daemon",
	Long: `Connects this machine to AgentCockpit and installs a startup service
so the daemon runs automatically on login — no manual restarts needed.

On macOS, this installs a launchd agent (~Library/LaunchAgents).
On Linux, this installs a systemd user service.`,
	RunE: runInstall,
}

var installRelay, installInvite string

func init() {
	installCmd.Flags().StringVar(&installRelay, "relay", "https://agentcockpit.io", "Relay server URL")
	installCmd.Flags().StringVar(&installInvite, "invite", "", "Invite token from the dashboard (skips browser step)")
}

func runInstall(_ *cobra.Command, _ []string) error {
	relay := strings.TrimRight(installRelay, "/")

	// Step 1: auth.
	// Always claim an invite token when one is given — it registers a new host
	// even if agentcockpit was previously installed on this machine.
	// Only skip auth when no invite is provided and a config already exists.
	cfgPath, _ := agentConfigPath()
	_, statErr := os.Stat(cfgPath)
	if installInvite != "" {
		fmt.Println()
		if err := runConnectInvite(relay, installInvite); err != nil {
			return err
		}
	} else if os.IsNotExist(statErr) {
		fmt.Println()
		if err := runConnectDevice(relay); err != nil {
			return err
		}
	} else {
		fmt.Println("  Already authorized — skipping auth.")
	}

	// Step 2: install system service
	binPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot determine binary path: %w", err)
	}
	// Resolve symlinks so the service path is stable
	if resolved, err := filepath.EvalSymlinks(binPath); err == nil {
		binPath = resolved
	}

	fmt.Printf("  Installing startup service...")
	if err := installService(binPath); err != nil {
		fmt.Println(" failed")
		return fmt.Errorf("install service: %w", err)
	}
	fmt.Println(" done")
	fmt.Println()
	fmt.Println("  The AgentCockpit daemon is running and will start automatically on login.")
	fmt.Println()
	fmt.Printf("  Check status:  agentcockpit status\n")
	fmt.Printf("  View logs:     tail -f /tmp/agentcockpit.log\n")
	fmt.Printf("  Uninstall:     agentcockpit uninstall\n\n")
	return nil
}

// ── uninstall ─────────────────────────────────────────────────────────────────

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Stop and remove the background daemon",
	RunE:  runUninstall,
}

func runUninstall(_ *cobra.Command, _ []string) error {
	if err := uninstallService(); err != nil {
		return err
	}
	fmt.Println("  Daemon removed. Run `agentcockpit install` to set it up again.")
	return nil
}

// ── status ────────────────────────────────────────────────────────────────────

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon and connection status",
	RunE:  runStatus,
}

func runStatus(_ *cobra.Command, _ []string) error {
	cfgPath, _ := agentConfigPath()
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		fmt.Println("  Status: not installed  (run `agentcockpit install`)")
		return nil
	}

	var cfg agent.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		fmt.Println("  Status: config corrupt  (run `agentcockpit install`)")
		return nil
	}

	fmt.Printf("  Host:   %s\n", cfg.Name)
	fmt.Printf("  Relay:  %s\n", cfg.RelayURL)
	fmt.Printf("  Config: %s\n", cfgPath)

	running, pid := serviceRunning()
	if running {
		fmt.Printf("  Daemon: running (pid %s)\n", pid)
	} else {
		fmt.Println("  Daemon: stopped  (run `agentcockpit install` or check service logs)")
	}
	fmt.Println()
	return nil
}

// ── Platform service management ───────────────────────────────────────────────

const serviceLabel = "io.agentcockpit"

func servicePlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist"), nil
}

func systemdUnitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", serviceLabel+".service"), nil
}

func installService(binPath string) error {
	switch runtime.GOOS {
	case "darwin":
		return installLaunchd(binPath)
	case "linux":
		return installSystemd(binPath)
	default:
		fmt.Printf("\n  Note: automatic startup not supported on %s.\n", runtime.GOOS)
		fmt.Printf("  Run manually: %s _daemon\n", binPath)
		return nil
	}
}

func uninstallService() error {
	switch runtime.GOOS {
	case "darwin":
		return uninstallLaunchd()
	case "linux":
		return uninstallSystemd()
	default:
		fmt.Println("  Nothing to remove for this platform.")
		return nil
	}
}

func serviceRunning() (bool, string) {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("launchctl", "list", serviceLabel).Output()
		if err != nil {
			return false, ""
		}
		// launchctl list returns JSON with PID field when running
		var info struct {
			PID int `json:"PID"`
		}
		if err := json.Unmarshal(out, &info); err == nil && info.PID > 0 {
			return true, fmt.Sprintf("%d", info.PID)
		}
		return false, ""
	case "linux":
		out, err := exec.Command("systemctl", "--user", "is-active", serviceLabel).Output()
		if err != nil {
			return false, ""
		}
		return strings.TrimSpace(string(out)) == "active", ""
	default:
		return false, ""
	}
}

// ── macOS launchd ─────────────────────────────────────────────────────────────

func launchdPlist(binPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>_daemon</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/tmp/agentcockpit.log</string>
  <key>StandardErrorPath</key><string>/tmp/agentcockpit.log</string>
</dict>
</plist>
`, serviceLabel, binPath)
}

func installLaunchd(binPath string) error {
	plistPath, err := servicePlistPath()
	if err != nil {
		return err
	}

	// Unload existing service if present
	exec.Command("launchctl", "unload", plistPath).Run() //nolint:errcheck

	if err := os.MkdirAll(filepath.Dir(plistPath), 0755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	if err := os.WriteFile(plistPath, []byte(launchdPlist(binPath)), 0644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	if out, err := exec.Command("launchctl", "load", plistPath).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load: %w\n%s", err, out)
	}
	return nil
}

func uninstallLaunchd() error {
	plistPath, err := servicePlistPath()
	if err != nil {
		return err
	}
	exec.Command("launchctl", "unload", plistPath).Run() //nolint:errcheck
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove plist: %w", err)
	}
	return nil
}

// ── Linux systemd ─────────────────────────────────────────────────────────────

func systemdUnit(binPath string) string {
	return fmt.Sprintf(`[Unit]
Description=AgentCockpit host daemon
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s _daemon
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=default.target
`, binPath)
}

func installSystemd(binPath string) error {
	unitPath, err := systemdUnitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0755); err != nil {
		return fmt.Errorf("create systemd dir: %w", err)
	}
	if err := os.WriteFile(unitPath, []byte(systemdUnit(binPath)), 0644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}
	exec.Command("systemctl", "--user", "daemon-reload").Run()               //nolint:errcheck
	exec.Command("systemctl", "--user", "enable", serviceLabel).Run()        //nolint:errcheck
	if out, err := exec.Command("systemctl", "--user", "restart", serviceLabel).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl restart: %w\n%s", err, out)
	}
	return nil
}

func uninstallSystemd() error {
	exec.Command("systemctl", "--user", "stop", serviceLabel).Run()          //nolint:errcheck
	exec.Command("systemctl", "--user", "disable", serviceLabel).Run()       //nolint:errcheck
	unitPath, err := systemdUnitPath()
	if err != nil {
		return err
	}
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit: %w", err)
	}
	exec.Command("systemctl", "--user", "daemon-reload").Run() //nolint:errcheck
	return nil
}

// ── Auth helpers (device flow + invite flow) ──────────────────────────────────

func runConnectInvite(relay, inviteToken string) error {
	fmt.Printf("  Connecting %s via invite token...\n\n", hostname())

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
	}
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return fmt.Errorf("decode claim response: %w", err)
	}
	return writeAgentConfig(relay, cr.HostToken)
}

func runConnectDevice(relay string) error {
	resp, err := http.Post(relay+"/api/device/request", "application/json",
		strings.NewReader(fmt.Sprintf(`{"hostname":%q,"platform":%q}`, hostname(), runtime.GOOS)))
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

	fmt.Printf("  Authorize this machine:\n\n")
	fmt.Printf("    1. Open:  %s\n\n", dr.VerifyURL)
	fmt.Printf("    2. Code:  %s\n\n", dr.UserCode)

	if openBrowser(dr.VerifyURL) {
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
		return fmt.Errorf("authorization timed out — run `agentcockpit install` again")
	}
	fmt.Println(" authorized!")
	return writeAgentConfig(relay, hostToken)
}

func pollDeviceToken(relay, deviceCode string) (string, bool, error) {
	resp, err := http.Get(relay + "/api/device/token?device_code=" + deviceCode)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted {
		return "", false, nil
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
	fmt.Printf("  Authorized as %q\n", cfg.Name)
	return nil
}

func openBrowser(url string) bool {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd, args = "open", []string{url}
	case "linux":
		cmd, args = "xdg-open", []string{url}
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
