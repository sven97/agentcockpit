package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"os"

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
	SessionID string          `json:"session_id"`
	HookEvent string          `json:"hook_event_name"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	CWD       string          `json:"cwd"`
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

	// 3. Connect to the daemon's Unix socket and send the request.
	conn, err := net.Dial("unix", "/tmp/agentcockpit-agent.sock")
	if err != nil {
		// Daemon not running — fail open (allow) so Claude Code isn't blocked.
		fmt.Fprintf(os.Stderr, "agentcockpit: daemon not running, allowing tool call\n")
		return writeDecision(input.HookEvent, "allow", "daemon unavailable")
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return writeDecision(input.HookEvent, "allow", "daemon write error")
	}

	// 4. Block waiting for the daemon to relay the decision back.
	var resp agent.HookResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return writeDecision(input.HookEvent, "allow", "daemon read error")
	}

	// 5. Write decision to stdout and exit with the appropriate code.
	if err := writeDecision(input.HookEvent, resp.Decision, resp.Reason); err != nil {
		return err
	}
	if resp.Decision == "deny" {
		os.Exit(2) // Claude Code treats exit code 2 as a block.
	}
	return nil
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
