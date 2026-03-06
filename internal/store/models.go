package store

import "time"

type User struct {
	ID           string
	Email        string
	Name         string
	PasswordHash string // bcrypt; empty if OAuth-only
	GitHubID     string
	Role         string // "user" | "admin"
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    *time.Time
}

type AuthToken struct {
	ID          string
	UserID      string
	TokenHash   string // SHA-256 of the raw token
	ExpiresAt   time.Time
	CreatedAt   time.Time
	LastUsedAt  *time.Time
	UserAgent   string
	IPAddress   string
	RevokedAt   *time.Time
}

type DeviceAuthorization struct {
	DeviceCode   string
	UserCode     string // human-readable: WREN-7429
	UserID       string // empty until authorized
	HostToken    string // plaintext, returned once then cleared
	Platform     string
	Hostname     string
	ExpiresAt    time.Time
	AuthorizedAt *time.Time
	CreatedAt    time.Time
}

type Host struct {
	ID           string
	UserID       string
	Name         string
	TokenHash    string // SHA-256 of the host bearer token
	Status       string // "online" | "offline" | "reconnecting"
	Platform     string // "darwin" | "linux"
	Hostname     string
	AgentVersion string
	LastSeenAt   *time.Time
	CreatedAt    time.Time
	DeletedAt    *time.Time
}

type Session struct {
	ID          string
	UserID      string
	HostID      string
	Name        string
	AgentType   string // "claude-code" | "codex" | "opencode" | "custom"
	WorkingDir  string
	Command     string
	EnvJSON     string // JSON map of extra env vars (no secrets)
	Status      string // "starting" | "running" | "awaiting_approval" | "stopped" | "error"
	ExitCode    *int
	StartedAt   *time.Time
	StoppedAt   *time.Time
	CreatedAt   time.Time
	DeletedAt   *time.Time
}

type SessionEvent struct {
	ID        string
	SessionID string
	Seq       int
	Type      string // "output" | "approval_request" | "approval_response" | "status_change" | "error"
	Data      []byte // raw PTY bytes for "output"; JSON for others
	CreatedAt time.Time
}

type ApprovalRequest struct {
	ID             string
	SessionID      string
	UserID         string
	ToolName       string // "Bash" | "Write" | "Edit" | ...
	ToolInput      string // raw JSON from hook stdin
	RiskLevel      string // "read" | "write" | "execute" | "destructive"
	Status         string // "pending" | "approved" | "rejected"
	DecisionReason string
	DecidedAt      *time.Time
	DecidedBy      string // user ID
	CreatedAt      time.Time
}
