package store

import (
	"context"
	"time"
)

// Store is the persistence interface. Implementations: SQLite (default), Postgres (future).
type Store interface {
	// Users
	CreateUser(ctx context.Context, u *User) error
	GetUserByEmail(ctx context.Context, email string) (*User, error)
	GetUserByID(ctx context.Context, id string) (*User, error)
	GetUserByGitHubID(ctx context.Context, githubID string) (*User, error)
	UpdateUserE2EKeys(ctx context.Context, userID, spkiBase64url, encryptedPrivKey, pbkdf2Salt string) error

	// Auth tokens (browser sessions)
	CreateAuthToken(ctx context.Context, t *AuthToken) error
	GetAuthToken(ctx context.Context, tokenHash string) (*AuthToken, error)
	TouchAuthToken(ctx context.Context, id string) error
	RevokeAuthToken(ctx context.Context, id string) error

	// Device authorization (RFC 8628)
	CreateDeviceAuthorization(ctx context.Context, d *DeviceAuthorization) error
	GetDeviceAuthorization(ctx context.Context, deviceCode string) (*DeviceAuthorization, error)
	GetDeviceAuthorizationByUserCode(ctx context.Context, userCode string) (*DeviceAuthorization, error)
	ListPendingDeviceAuthorizations(ctx context.Context) ([]*DeviceAuthorization, error)
	AuthorizeDevice(ctx context.Context, deviceCode, userID, hostToken string) error
	ClaimDeviceToken(ctx context.Context, deviceCode string) (string, error) // returns plaintext token, sets it to NULL

	// Hosts
	CreateHost(ctx context.Context, h *Host) error
	GetHostByTokenHash(ctx context.Context, tokenHash string) (*Host, error)
	GetHostByID(ctx context.Context, id string) (*Host, error)
	ListHostsByUser(ctx context.Context, userID string) ([]*Host, error)
	UpdateHostStatus(ctx context.Context, id, status string, lastSeen time.Time) error
	DeleteHost(ctx context.Context, id string) error

	// Sessions
	CreateSession(ctx context.Context, s *Session) error
	GetSession(ctx context.Context, id string) (*Session, error)
	ListSessionsByUser(ctx context.Context, userID string, limit int) ([]*Session, error)
	ListActiveSessionsByHost(ctx context.Context, hostID string) ([]*Session, error)
	UpdateSessionStatus(ctx context.Context, id, status string) error
	StopSession(ctx context.Context, id string, exitCode int) error
	DeleteSession(ctx context.Context, id string) error
	DeleteSessionsByHost(ctx context.Context, hostID string) error
	SetSessionEphemeralPubKey(ctx context.Context, sessionID, spkiBase64url string) error

	// Session events (append-only)
	AppendSessionEvent(ctx context.Context, e *SessionEvent) error
	ListSessionEvents(ctx context.Context, sessionID string, afterSeq int) ([]*SessionEvent, error)

	// Approval requests
	CreateApprovalRequest(ctx context.Context, r *ApprovalRequest) error
	GetApprovalRequest(ctx context.Context, id string) (*ApprovalRequest, error)
	ListPendingApprovals(ctx context.Context, userID string) ([]*ApprovalRequest, error)
	ResolveApproval(ctx context.Context, id, decision, reason, decidedByUserID string) error

	// Settings
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value, updatedByUserID string) error

	// Maintenance
	PruneSessionEvents(ctx context.Context, olderThan time.Time) error
	MarkStaleSessionsAsError(ctx context.Context) error
	Close() error
}
