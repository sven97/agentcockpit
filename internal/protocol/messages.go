// Package protocol defines the WebSocket message types exchanged between
// the relay server and host agents.
package protocol

import "encoding/json"

const (
	// Host → Server
	TypeHostHello       = "host_hello"
	TypeSessionStarted  = "session_started"
	TypeSessionStopped  = "session_stopped"
	TypeApprovalRequest = "approval_request"

	// Server → Host
	TypeApprovalResponse = "approval_response"
	TypeSessionCreate    = "session_create"
	TypeSessionKill      = "session_kill"
	TypeStdinData        = "stdin_data"
	TypeServerShutdown   = "server_shutdown"
	TypeHostRemoved      = "host_removed" // host was deleted; agent should stop and remove config

	// Binary frame prefix byte for PTY output
	FramePTY = 0x01

	// Risk levels
	RiskRead        = "read"
	RiskWrite       = "write"
	RiskExecute     = "execute"
	RiskDestructive = "destructive"
)

type HostHello struct {
	Type         string `json:"type"`
	HostID       string `json:"hostId"`
	Name         string `json:"name"`
	Platform     string `json:"platform"`
	AgentVersion string `json:"agentVersion"`
}

type SessionStarted struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	AgentType string `json:"agentType"`
	CWD       string `json:"cwd"`
	PID       int    `json:"pid"`
}

type SessionStopped struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	ExitCode  int    `json:"exitCode"`
}

type ApprovalRequest struct {
	Type              string          `json:"type"`
	RequestID         string          `json:"requestId"`
	SessionID         string          `json:"sessionId"`
	ToolName          string          `json:"toolName"`
	ToolInput         json.RawMessage `json:"toolInput"`
	RiskLevel         string          `json:"riskLevel"`
	InputTokens       int             `json:"inputTokens,omitempty"`
	ContextWindowSize int             `json:"contextWindowSize,omitempty"`
}

type ApprovalResponse struct {
	Type      string `json:"type"`
	RequestID string `json:"requestId"`
	Decision  string `json:"decision"` // "allow" | "deny"
	Reason    string `json:"reason,omitempty"`
}

type SessionCreate struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	AgentType string `json:"agentType"`
	CWD       string `json:"cwd"`
	Command   string `json:"command"`
}

type SessionKill struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
}

type StdinData struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	Data      []byte `json:"data"`
}

type ServerShutdown struct {
	Type        string `json:"type"`
	ReconnectIn int    `json:"reconnectIn"` // seconds
}
