package main

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/sven97/agentcockpit/internal/agent"
)

var hookCmd = &cobra.Command{
	Use:   "hook",
	Short: "Hook shim — called by Claude Code per tool use (not for direct use)",
	RunE:  runHook,
}

// HookInput is the JSON Claude Code writes to stdin for PreToolUse hooks.
type HookInput struct {
	SessionID     string             `json:"session_id"`
	HookEvent     string             `json:"hook_event_name"`
	ToolName      string             `json:"tool_name"`
	ToolInput     json.RawMessage    `json:"tool_input"`
	CWD           string             `json:"cwd"`
	ContextWindow *HookContextWindow `json:"context_window,omitempty"`
}

type HookContextWindow struct {
	CurrentUsage struct {
		InputTokens int `json:"input_tokens"`
	} `json:"current_usage"`
	ContextWindowSize int `json:"context_window_size"`
}

// HookOutput is the JSON we write to stdout to allow or deny the tool call.
type HookOutput struct {
	HookSpecificOutput HookDecision `json:"hookSpecificOutput"`
}

type HookDecision struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"` // "allow" | "deny"
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

func runHook(cmd *cobra.Command, args []string) error {
	// 1. Read the hook payload from Claude Code via stdin.
	var input HookInput
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		return fmt.Errorf("decode hook input: %w", err)
	}

	// 2. Build the request for the daemon.
	requestID := newHookRequestID()
	req := agent.HookRequest{
		RequestID: requestID,
		SessionID: input.SessionID,
		ToolName:  input.ToolName,
		ToolInput: input.ToolInput,
		RiskLevel: classifyRisk(input.ToolName, input.ToolInput),
	}
	if cw := input.ContextWindow; cw != nil {
		req.InputTokens = cw.CurrentUsage.InputTokens
		req.ContextWindowSize = cw.ContextWindowSize
	}

	// 3. Try to open /dev/tty for interactive local approval.
	// When the user is at a terminal they can approve/deny right there; the
	// dashboard receives a notification in parallel but does not gate the call.
	tty, ttyErr := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if ttyErr == nil {
		defer tty.Close()
		req.HasTTY = true
		// Fire-and-forget: send dashboard notification (best-effort).
		if conn, err := net.DialTimeout("unix", "/tmp/agentcockpit-agent.sock", time.Second); err == nil {
			json.NewEncoder(conn).Encode(req) //nolint:errcheck
			conn.Close()
		}
		// Prompt locally and return the user's decision.
		return promptLocalApproval(tty, input.HookEvent, req)
	}

	// 4. Headless path: connect to daemon and block until browser approves/denies.
	conn, err := net.DialTimeout("unix", "/tmp/agentcockpit-agent.sock", 2*time.Second)
	if err != nil {
		// Daemon not running — fail open so the agent is not stuck.
		return writeDecision(input.HookEvent, "allow", "")
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return writeDecision(input.HookEvent, "allow", "")
	}

	// Block until the daemon writes back the browser decision (up to 5 min).
	var resp agent.HookResponse
	conn.SetDeadline(time.Now().Add(5 * time.Minute)) //nolint:errcheck
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		// Timeout or error — fail open.
		return writeDecision(input.HookEvent, "allow", "")
	}
	return writeDecision(input.HookEvent, resp.Decision, resp.Reason)
}

// promptLocalApproval shows a terminal prompt and returns the user's decision.
func promptLocalApproval(tty *os.File, hookEvent string, req agent.HookRequest) error {
	riskColor := "\033[33m" // yellow — execute
	switch req.RiskLevel {
	case "read":
		riskColor = "\033[32m" // green
	case "destructive":
		riskColor = "\033[31m" // red
	}
	fmt.Fprintf(tty, "\n\033[1m▸ AgentCockpit: %s\033[0m %s[%s]\033[0m\n  Allow? [Y/n] ",
		req.ToolName, riskColor, req.RiskLevel)

	reader := bufio.NewReader(tty)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))

	if line == "n" || line == "no" {
		return writeDecision(hookEvent, "deny", "rejected at terminal")
	}
	return writeDecision(hookEvent, "allow", "")
}

func writeDecision(hookEvent, decision, reason string) error {
	out := HookOutput{
		HookSpecificOutput: HookDecision{
			HookEventName:            hookEvent,
			PermissionDecision:       decision,
			PermissionDecisionReason: reason,
		},
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}

// classifyRisk assigns a risk level based on tool name and input heuristics.
func classifyRisk(toolName string, input json.RawMessage) string {
	switch toolName {
	case "Read", "Glob", "Grep", "LS":
		return "read"
	case "Write", "Edit", "MultiEdit", "NotebookEdit":
		return "write"
	case "WebFetch", "WebSearch":
		return "execute"
	case "Bash":
		// Heuristic: look for destructive patterns in the command string.
		var inp struct {
			Command string `json:"command"`
		}
		json.Unmarshal(input, &inp)
		if isDestructive(inp.Command) {
			return "destructive"
		}
		return "execute"
	}
	return "execute"
}

var destructivePatterns = []string{
	"rm ", "rm\t", "rmdir", "git push --force", "git push -f",
	"DROP ", "TRUNCATE ", "DELETE FROM", "format ", "mkfs",
	"> /dev/", "dd if=", "chmod -R 777",
}

func isDestructive(cmd string) bool {
	for _, p := range destructivePatterns {
		for i := 0; i <= len(cmd)-len(p); i++ {
			if cmd[i:i+len(p)] == p {
				return true
			}
		}
	}
	return false
}

func newHookRequestID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}
