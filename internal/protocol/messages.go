// Package protocol defines the WebSocket message types exchanged between
// the relay server and host agents.
package protocol

import "encoding/json"

const (
	// Host → Server
	TypeHostHello       = "host_hello"
	TypeSessionList     = "session_list" // sent after host_hello to report active sessions on reconnect
	TypeSessionStarted  = "session_started"
	TypeSessionStopped  = "session_stopped"
	TypeApprovalRequest = "approval_request"

	// Server → Host
	TypeApprovalResponse = "approval_response"
	TypeSessionCreate    = "session_create"
	TypeSessionKill      = "session_kill"
	TypeStdinData        = "stdin_data"
	TypeSessionResize    = "session_resize"
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

// SessionList is sent by the agent immediately after host_hello to report which
// sessions are still running. The server uses this to restore session status
// after a reconnect (instead of leaving them as "error" from MarkStaleSessionsAsError).
type SessionList struct {
	Type     string   `json:"type"`
	Sessions []string `json:"sessions"` // active session IDs
}

type HostHello struct {
	Type         string `json:"type"`
	HostID       string `json:"hostId"`
	Name         string `json:"name"`
	Platform     string `json:"platform"`
	AgentVersion string `json:"agentVersion"`
}

type SessionStarted struct {
	Type                 string `json:"type"`
	SessionID            string `json:"sessionId"`
	AgentType            string `json:"agentType"`
	CWD                  string `json:"cwd"`
	PID                  int    `json:"pid"`
	AgentEphemeralPubKey string `json:"agentEphemeralPubKey,omitempty"` // SPKI base64url; set when E2E active
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
	// Plaintext fields — populated only when E2E is NOT active.
	ToolName          string          `json:"toolName,omitempty"`
	ToolInput         json.RawMessage `json:"toolInput,omitempty"`
	RiskLevel         string          `json:"riskLevel,omitempty"`
	// E2E encrypted payload — base64url [12-byte IV][AES-GCM ciphertext].
	// Decrypts to JSON: {"toolName":"…","toolInput":{…},"riskLevel":"…"}
	EncryptedPayload  string          `json:"enc,omitempty"`
	// Token accounting stays plaintext (not sensitive).
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
	Type          string `json:"type"`
	SessionID     string `json:"sessionId"`
	AgentType     string `json:"agentType"`
	CWD           string `json:"cwd"`
	Command       string `json:"command"`
	UserE2EPubKey string `json:"userE2EPubKey,omitempty"` // SPKI base64url of user's long-term public key
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

type SessionResize struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	Cols      uint16 `json:"cols"`
	Rows      uint16 `json:"rows"`
}

type ServerShutdown struct {
	Type        string `json:"type"`
	ReconnectIn int    `json:"reconnectIn"` // seconds
}
