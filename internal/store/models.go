package store

import "time"

type User struct {
	ID             string
	Email          string
	Name           string
	PasswordHash   string // bcrypt; empty if OAuth-only
	GitHubID       string
	Role           string // "user" | "admin"
	E2EPublicKey        string // SPKI base64url of user's ECDH P-256 long-term public key; empty if not yet set
	E2EEncryptedPrivKey string // PKCS#8 private key encrypted with PBKDF2+AES-256-GCM wrapping key; base64
	E2EPbkdf2Salt       string // base64 random 16-byte salt for PBKDF2 wrapping key derivation
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      *time.Time
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
	ID                   string
	UserID               string
	HostID               string
	Name                 string
	AgentType            string // "claude-code" | "codex" | "opencode" | "custom"
	WorkingDir           string
	Command              string
	EnvJSON              string // JSON map of extra env vars (no secrets)
	Status               string // "starting" | "running" | "awaiting_approval" | "stopped" | "error"
	ExitCode             *int
	StartedAt            *time.Time
	StoppedAt            *time.Time
	CreatedAt            time.Time
	DeletedAt            *time.Time
	AgentEphemeralPubKey string // SPKI base64url of per-session agent ephemeral P-256 public key; empty if E2E not active
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
	ID                 string
	SessionID          string
	UserID             string
	ToolName           string // plaintext only when E2E is NOT active
	ToolInput          string // plaintext only when E2E is NOT active
	RiskLevel          string // plaintext only when E2E is NOT active
	PayloadCiphertext  string // base64url [12-byte IV][AES-GCM ciphertext]; set when E2E is active
	Status             string // "pending" | "approved" | "rejected"
	DecisionReason     string
	DecidedAt          *time.Time
	DecidedBy          string // user ID
	CreatedAt          time.Time
}
