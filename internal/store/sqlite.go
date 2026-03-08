package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type sqliteStore struct {
	db *sql.DB
}

// NewSQLite opens (or creates) the SQLite database at the given path and
// runs migrations. Caller must call Close() when done.
func NewSQLite(path string) (Store, error) {
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite allows only one writer at a time
	s := &sqliteStore{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *sqliteStore) Close() error { return s.db.Close() }

// ── Migrations ────────────────────────────────────────────────────────────────

func (s *sqliteStore) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// Additive column migrations — idempotent, safe on existing databases.
	for _, stmt := range migrations {
		if _, err := s.db.Exec(stmt); err != nil {
			// SQLite returns "duplicate column name: …" when the column already exists.
			if !strings.Contains(err.Error(), "duplicate column name") {
				return err
			}
		}
	}
	return nil
}

// migrations are ALTER TABLE statements run after schema creation.
// Each is retried idempotently — duplicate-column errors are swallowed.
var migrations = []string{
	`ALTER TABLE users ADD COLUMN e2e_public_key TEXT`,
	`ALTER TABLE users ADD COLUMN e2e_encrypted_privkey TEXT`,
	`ALTER TABLE users ADD COLUMN e2e_pbkdf2_salt TEXT`,
	`ALTER TABLE sessions ADD COLUMN agent_ephemeral_pubkey TEXT`,
	`ALTER TABLE approval_requests ADD COLUMN payload_ciphertext TEXT`,
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
    id            TEXT PRIMARY KEY,
    email         TEXT UNIQUE NOT NULL,
    name          TEXT NOT NULL,
    password_hash TEXT,
    github_id     TEXT UNIQUE,
    role          TEXT NOT NULL DEFAULT 'user',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    deleted_at    TEXT
);

CREATE TABLE IF NOT EXISTS auth_tokens (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id),
    token_hash   TEXT UNIQUE NOT NULL,
    expires_at   TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    last_used_at TEXT,
    user_agent   TEXT,
    ip_address   TEXT,
    revoked_at   TEXT
);

CREATE TABLE IF NOT EXISTS device_authorizations (
    device_code   TEXT PRIMARY KEY,
    user_code     TEXT UNIQUE NOT NULL,
    user_id       TEXT REFERENCES users(id),
    host_token    TEXT,
    platform      TEXT,
    hostname      TEXT,
    expires_at    TEXT NOT NULL,
    authorized_at TEXT,
    created_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS hosts (
    id            TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users(id),
    name          TEXT NOT NULL,
    token_hash    TEXT UNIQUE NOT NULL,
    status        TEXT NOT NULL DEFAULT 'offline',
    platform      TEXT,
    hostname      TEXT,
    agent_version TEXT,
    last_seen_at  TEXT,
    created_at    TEXT NOT NULL,
    deleted_at    TEXT
);

CREATE TABLE IF NOT EXISTS sessions (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users(id),
    host_id     TEXT NOT NULL REFERENCES hosts(id),
    name        TEXT,
    agent_type  TEXT NOT NULL,
    working_dir TEXT NOT NULL,
    command     TEXT NOT NULL,
    env_json    TEXT,
    status      TEXT NOT NULL DEFAULT 'starting',
    exit_code   INTEGER,
    started_at  TEXT,
    stopped_at  TEXT,
    created_at  TEXT NOT NULL,
    deleted_at  TEXT
);

CREATE TABLE IF NOT EXISTS session_events (
    id         TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    seq        INTEGER NOT NULL,
    type       TEXT NOT NULL,
    data       BLOB NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_session_events ON session_events(session_id, seq);

CREATE TABLE IF NOT EXISTS approval_requests (
    id              TEXT PRIMARY KEY,
    session_id      TEXT NOT NULL REFERENCES sessions(id),
    user_id         TEXT NOT NULL REFERENCES users(id),
    tool_name       TEXT NOT NULL,
    tool_input      TEXT NOT NULL,
    risk_level      TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending',
    decision_reason TEXT,
    decided_at      TEXT,
    decided_by      TEXT REFERENCES users(id),
    created_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_approvals_pending ON approval_requests(user_id, status)
    WHERE status = 'pending';

CREATE TABLE IF NOT EXISTS hook_installs (
    id           TEXT PRIMARY KEY,
    host_id      TEXT NOT NULL REFERENCES hosts(id),
    agent_type   TEXT NOT NULL,
    config_path  TEXT NOT NULL,
    config_scope TEXT NOT NULL,
    installed_at TEXT NOT NULL,
    removed_at   TEXT
);

CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    updated_by TEXT REFERENCES users(id)
);
`

// ── Helpers ───────────────────────────────────────────────────────────────────

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func nullStr(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func nullTime(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: t.UTC().Format(time.RFC3339Nano), Valid: true}
}

func parseTime(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s.String)
	if err != nil {
		return nil
	}
	return &t
}

func mustParseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// ── Users ─────────────────────────────────────────────────────────────────────

func (s *sqliteStore) CreateUser(ctx context.Context, u *User) error {
	if u.ID == "" {
		u.ID = newID()
	}
	n := now()
	u.CreatedAt = mustParseTime(n)
	u.UpdatedAt = mustParseTime(n)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users (id, email, name, password_hash, github_id, role, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Email, u.Name, nullStr(u.PasswordHash), nullStr(u.GitHubID),
		u.Role, n, n,
	)
	return err
}

func (s *sqliteStore) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT id, email, name, password_hash, github_id, role, e2e_public_key, e2e_encrypted_privkey, e2e_pbkdf2_salt, created_at, updated_at, deleted_at
		 FROM users WHERE email = ? AND deleted_at IS NULL`, email))
}

func (s *sqliteStore) GetUserByID(ctx context.Context, id string) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT id, email, name, password_hash, github_id, role, e2e_public_key, e2e_encrypted_privkey, e2e_pbkdf2_salt, created_at, updated_at, deleted_at
		 FROM users WHERE id = ? AND deleted_at IS NULL`, id))
}

func (s *sqliteStore) GetUserByGitHubID(ctx context.Context, githubID string) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT id, email, name, password_hash, github_id, role, e2e_public_key, e2e_encrypted_privkey, e2e_pbkdf2_salt, created_at, updated_at, deleted_at
		 FROM users WHERE github_id = ? AND deleted_at IS NULL`, githubID))
}

func (s *sqliteStore) scanUser(row *sql.Row) (*User, error) {
	var u User
	var passwordHash, githubID, e2ePubKey, e2eEncPrivKey, e2eSalt, createdAt, updatedAt, deletedAt sql.NullString
	err := row.Scan(&u.ID, &u.Email, &u.Name, &passwordHash, &githubID,
		&u.Role, &e2ePubKey, &e2eEncPrivKey, &e2eSalt, &createdAt, &updatedAt, &deletedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.PasswordHash = passwordHash.String
	u.GitHubID = githubID.String
	u.E2EPublicKey = e2ePubKey.String
	u.E2EEncryptedPrivKey = e2eEncPrivKey.String
	u.E2EPbkdf2Salt = e2eSalt.String
	if t := parseTime(createdAt); t != nil {
		u.CreatedAt = *t
	}
	if t := parseTime(updatedAt); t != nil {
		u.UpdatedAt = *t
	}
	u.DeletedAt = parseTime(deletedAt)
	return &u, nil
}

func (s *sqliteStore) UpdateUserE2EKeys(ctx context.Context, userID, spkiBase64url, encryptedPrivKey, pbkdf2Salt string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET e2e_public_key = ?, e2e_encrypted_privkey = ?, e2e_pbkdf2_salt = ?, updated_at = ? WHERE id = ?`,
		spkiBase64url, nullStr(encryptedPrivKey), nullStr(pbkdf2Salt), now(), userID)
	return err
}

// ── Auth tokens ───────────────────────────────────────────────────────────────

func (s *sqliteStore) CreateAuthToken(ctx context.Context, t *AuthToken) error {
	if t.ID == "" {
		t.ID = newID()
	}
	t.CreatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO auth_tokens (id, user_id, token_hash, expires_at, created_at, user_agent, ip_address)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.UserID, t.TokenHash,
		t.ExpiresAt.UTC().Format(time.RFC3339Nano),
		now(), nullStr(t.UserAgent), nullStr(t.IPAddress),
	)
	return err
}

func (s *sqliteStore) GetAuthToken(ctx context.Context, tokenHash string) (*AuthToken, error) {
	var t AuthToken
	var expiresAt, createdAt, lastUsed, revoked sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, token_hash, expires_at, created_at, last_used_at, user_agent, ip_address, revoked_at
		FROM auth_tokens WHERE token_hash = ?`, tokenHash).Scan(
		&t.ID, &t.UserID, &t.TokenHash, &expiresAt, &createdAt,
		&lastUsed, &t.UserAgent, &t.IPAddress, &revoked,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if v := parseTime(expiresAt); v != nil {
		t.ExpiresAt = *v
	}
	if v := parseTime(createdAt); v != nil {
		t.CreatedAt = *v
	}
	t.LastUsedAt = parseTime(lastUsed)
	t.RevokedAt = parseTime(revoked)
	return &t, nil
}

func (s *sqliteStore) TouchAuthToken(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE auth_tokens SET last_used_at = ? WHERE id = ?`, now(), id)
	return err
}

func (s *sqliteStore) RevokeAuthToken(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE auth_tokens SET revoked_at = ? WHERE id = ?`, now(), id)
	return err
}

// ── Device authorizations ─────────────────────────────────────────────────────

func (s *sqliteStore) CreateDeviceAuthorization(ctx context.Context, d *DeviceAuthorization) error {
	d.CreatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO device_authorizations
		    (device_code, user_code, platform, hostname, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		d.DeviceCode, d.UserCode, nullStr(d.Platform), nullStr(d.Hostname),
		d.ExpiresAt.UTC().Format(time.RFC3339Nano), now(),
	)
	return err
}

func (s *sqliteStore) GetDeviceAuthorization(ctx context.Context, deviceCode string) (*DeviceAuthorization, error) {
	return s.scanDeviceAuth(s.db.QueryRowContext(ctx,
		`SELECT device_code, user_code, user_id, host_token, platform, hostname, expires_at, authorized_at, created_at
		 FROM device_authorizations WHERE device_code = ?`, deviceCode))
}

func (s *sqliteStore) GetDeviceAuthorizationByUserCode(ctx context.Context, userCode string) (*DeviceAuthorization, error) {
	return s.scanDeviceAuth(s.db.QueryRowContext(ctx,
		`SELECT device_code, user_code, user_id, host_token, platform, hostname, expires_at, authorized_at, created_at
		 FROM device_authorizations WHERE user_code = ?`, userCode))
}

func (s *sqliteStore) scanDeviceAuth(row *sql.Row) (*DeviceAuthorization, error) {
	var d DeviceAuthorization
	var userID, hostToken, platform, hostname, expiresAt, authorizedAt, createdAt sql.NullString
	err := row.Scan(&d.DeviceCode, &d.UserCode, &userID, &hostToken,
		&platform, &hostname, &expiresAt, &authorizedAt, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.UserID = userID.String
	d.HostToken = hostToken.String
	d.Platform = platform.String
	d.Hostname = hostname.String
	if v := parseTime(expiresAt); v != nil {
		d.ExpiresAt = *v
	}
	if v := parseTime(createdAt); v != nil {
		d.CreatedAt = *v
	}
	d.AuthorizedAt = parseTime(authorizedAt)
	return &d, nil
}

func (s *sqliteStore) ListPendingDeviceAuthorizations(ctx context.Context) ([]*DeviceAuthorization, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT device_code, user_code, user_id, host_token, platform, hostname, expires_at, authorized_at, created_at
		FROM device_authorizations
		WHERE authorized_at IS NULL AND expires_at > ?
		ORDER BY created_at ASC`, now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DeviceAuthorization
	for rows.Next() {
		var d DeviceAuthorization
		var userID, hostToken, platform, hostname, expiresAt, authorizedAt, createdAt sql.NullString
		if err := rows.Scan(&d.DeviceCode, &d.UserCode, &userID, &hostToken,
			&platform, &hostname, &expiresAt, &authorizedAt, &createdAt); err != nil {
			return nil, err
		}
		d.UserID = userID.String
		d.Platform = platform.String
		d.Hostname = hostname.String
		if v := parseTime(expiresAt); v != nil {
			d.ExpiresAt = *v
		}
		if v := parseTime(createdAt); v != nil {
			d.CreatedAt = *v
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

func (s *sqliteStore) AuthorizeDevice(ctx context.Context, deviceCode, userID, hostToken string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE device_authorizations
		SET user_id = ?, host_token = ?, authorized_at = ?
		WHERE device_code = ? AND authorized_at IS NULL`,
		userID, hostToken, now(), deviceCode,
	)
	return err
}

func (s *sqliteStore) ClaimDeviceToken(ctx context.Context, deviceCode string) (string, error) {
	var token sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT host_token FROM device_authorizations WHERE device_code = ? AND authorized_at IS NOT NULL`,
		deviceCode).Scan(&token)
	if err == sql.ErrNoRows || !token.Valid {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	// Clear the plaintext token from the DB after it's been claimed
	_, err = s.db.ExecContext(ctx,
		`UPDATE device_authorizations SET host_token = NULL WHERE device_code = ?`, deviceCode)
	return token.String, err
}

// ── Hosts ─────────────────────────────────────────────────────────────────────

func (s *sqliteStore) CreateHost(ctx context.Context, h *Host) error {
	if h.ID == "" {
		h.ID = newID()
	}
	h.CreatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO hosts (id, user_id, name, token_hash, status, platform, hostname, agent_version, created_at)
		VALUES (?, ?, ?, ?, 'offline', ?, ?, ?, ?)`,
		h.ID, h.UserID, h.Name, h.TokenHash,
		nullStr(h.Platform), nullStr(h.Hostname), nullStr(h.AgentVersion), now(),
	)
	return err
}

func (s *sqliteStore) GetHostByTokenHash(ctx context.Context, tokenHash string) (*Host, error) {
	return s.scanHost(s.db.QueryRowContext(ctx,
		`SELECT id, user_id, name, token_hash, status, platform, hostname, agent_version, last_seen_at, created_at, deleted_at
		 FROM hosts WHERE token_hash = ? AND deleted_at IS NULL`, tokenHash))
}

func (s *sqliteStore) GetHostByID(ctx context.Context, id string) (*Host, error) {
	return s.scanHost(s.db.QueryRowContext(ctx,
		`SELECT id, user_id, name, token_hash, status, platform, hostname, agent_version, last_seen_at, created_at, deleted_at
		 FROM hosts WHERE id = ? AND deleted_at IS NULL`, id))
}

func (s *sqliteStore) ListHostsByUser(ctx context.Context, userID string) ([]*Host, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, name, token_hash, status, platform, hostname, agent_version, last_seen_at, created_at, deleted_at
		 FROM hosts WHERE user_id = ? AND deleted_at IS NULL ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hosts []*Host
	for rows.Next() {
		var hh Host
		var platform, hostname, agentVersion, lastSeen, createdAt, deletedAt sql.NullString
		if err := rows.Scan(&hh.ID, &hh.UserID, &hh.Name, &hh.TokenHash, &hh.Status,
			&platform, &hostname, &agentVersion, &lastSeen, &createdAt, &deletedAt); err != nil {
			return nil, err
		}
		hh.Platform = platform.String
		hh.Hostname = hostname.String
		hh.AgentVersion = agentVersion.String
		hh.LastSeenAt = parseTime(lastSeen)
		if v := parseTime(createdAt); v != nil {
			hh.CreatedAt = *v
		}
		hh.DeletedAt = parseTime(deletedAt)
		hosts = append(hosts, &hh)
	}
	return hosts, rows.Err()
}

func (s *sqliteStore) scanHost(row *sql.Row) (*Host, error) {
	if row == nil {
		return nil, nil
	}
	var h Host
	var platform, hostname, agentVersion, lastSeen, createdAt, deletedAt sql.NullString
	err := row.Scan(&h.ID, &h.UserID, &h.Name, &h.TokenHash, &h.Status,
		&platform, &hostname, &agentVersion, &lastSeen, &createdAt, &deletedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	h.Platform = platform.String
	h.Hostname = hostname.String
	h.AgentVersion = agentVersion.String
	h.LastSeenAt = parseTime(lastSeen)
	if v := parseTime(createdAt); v != nil {
		h.CreatedAt = *v
	}
	h.DeletedAt = parseTime(deletedAt)
	return &h, nil
}

func (s *sqliteStore) UpdateHostStatus(ctx context.Context, id, status string, lastSeen time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE hosts SET status = ?, last_seen_at = ? WHERE id = ?`,
		status, lastSeen.UTC().Format(time.RFC3339Nano), id)
	return err
}

func (s *sqliteStore) DeleteHost(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE hosts SET deleted_at = ? WHERE id = ?`, now(), id)
	return err
}

// ── Sessions ──────────────────────────────────────────────────────────────────

func (s *sqliteStore) CreateSession(ctx context.Context, sess *Session) error {
	if sess.ID == "" {
		sess.ID = newID()
	}
	sess.CreatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, host_id, name, agent_type, working_dir, command, env_json, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'starting', ?)`,
		sess.ID, sess.UserID, sess.HostID, nullStr(sess.Name),
		sess.AgentType, sess.WorkingDir, sess.Command,
		nullStr(sess.EnvJSON), now(),
	)
	return err
}

func (s *sqliteStore) GetSession(ctx context.Context, id string) (*Session, error) {
	return s.scanSession(s.db.QueryRowContext(ctx,
		`SELECT id, user_id, host_id, name, agent_type, working_dir, command, env_json,
		        status, exit_code, started_at, stopped_at, created_at, deleted_at, agent_ephemeral_pubkey
		 FROM sessions WHERE id = ? AND deleted_at IS NULL`, id))
}

func (s *sqliteStore) ListSessionsByUser(ctx context.Context, userID string, limit int) ([]*Session, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, host_id, name, agent_type, working_dir, command, env_json,
		        status, exit_code, started_at, stopped_at, created_at, deleted_at, agent_ephemeral_pubkey
		 FROM sessions WHERE user_id = ? AND deleted_at IS NULL
		 ORDER BY created_at DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []*Session
	for rows.Next() {
		var sess Session
		var name, envJSON, exitCode, startedAt, stoppedAt, createdAt, deletedAt, ephPubKey sql.NullString
		if err := rows.Scan(&sess.ID, &sess.UserID, &sess.HostID, &name,
			&sess.AgentType, &sess.WorkingDir, &sess.Command, &envJSON,
			&sess.Status, &exitCode, &startedAt, &stoppedAt, &createdAt, &deletedAt, &ephPubKey); err != nil {
			return nil, err
		}
		sess.Name = name.String
		sess.EnvJSON = envJSON.String
		sess.AgentEphemeralPubKey = ephPubKey.String
		sess.StartedAt = parseTime(startedAt)
		sess.StoppedAt = parseTime(stoppedAt)
		if v := parseTime(createdAt); v != nil {
			sess.CreatedAt = *v
		}
		sess.DeletedAt = parseTime(deletedAt)
		sessions = append(sessions, &sess)
	}
	return sessions, rows.Err()
}

func (s *sqliteStore) ListActiveSessionsByHost(ctx context.Context, hostID string) ([]*Session, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, host_id, name, agent_type, working_dir, command, env_json,
		        status, exit_code, started_at, stopped_at, created_at, deleted_at, agent_ephemeral_pubkey
		 FROM sessions WHERE host_id = ? AND status IN ('starting','running','awaiting_approval') AND deleted_at IS NULL`,
		hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []*Session
	for rows.Next() {
		var sess Session
		var name, envJSON, exitCode, startedAt, stoppedAt, createdAt, deletedAt, ephPubKey sql.NullString
		if err := rows.Scan(&sess.ID, &sess.UserID, &sess.HostID, &name,
			&sess.AgentType, &sess.WorkingDir, &sess.Command, &envJSON,
			&sess.Status, &exitCode, &startedAt, &stoppedAt, &createdAt, &deletedAt, &ephPubKey); err != nil {
			return nil, err
		}
		sess.Name = name.String
		sess.EnvJSON = envJSON.String
		sess.AgentEphemeralPubKey = ephPubKey.String
		sess.StartedAt = parseTime(startedAt)
		sess.StoppedAt = parseTime(stoppedAt)
		if v := parseTime(createdAt); v != nil {
			sess.CreatedAt = *v
		}
		sessions = append(sessions, &sess)
	}
	return sessions, rows.Err()
}

func (s *sqliteStore) scanSession(row *sql.Row) (*Session, error) {
	var sess Session
	var name, envJSON, exitCode, startedAt, stoppedAt, createdAt, deletedAt, ephPubKey sql.NullString
	err := row.Scan(&sess.ID, &sess.UserID, &sess.HostID, &name,
		&sess.AgentType, &sess.WorkingDir, &sess.Command, &envJSON,
		&sess.Status, &exitCode, &startedAt, &stoppedAt, &createdAt, &deletedAt, &ephPubKey)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sess.Name = name.String
	sess.EnvJSON = envJSON.String
	sess.AgentEphemeralPubKey = ephPubKey.String
	sess.StartedAt = parseTime(startedAt)
	sess.StoppedAt = parseTime(stoppedAt)
	if v := parseTime(createdAt); v != nil {
		sess.CreatedAt = *v
	}
	sess.DeletedAt = parseTime(deletedAt)
	return &sess, nil
}

func (s *sqliteStore) SetSessionEphemeralPubKey(ctx context.Context, sessionID, spkiBase64url string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET agent_ephemeral_pubkey = ? WHERE id = ?`,
		spkiBase64url, sessionID)
	return err
}

func (s *sqliteStore) MarkStaleSessionsAsError(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET status = 'error' WHERE status IN ('starting', 'running', 'awaiting_approval') AND deleted_at IS NULL`)
	return err
}

func (s *sqliteStore) UpdateSessionStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET status = ? WHERE id = ?`, status, id)
	return err
}

func (s *sqliteStore) StopSession(ctx context.Context, id string, exitCode int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET status = 'stopped', exit_code = ?, stopped_at = ? WHERE id = ?`,
		exitCode, now(), id)
	return err
}

func (s *sqliteStore) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`,
		now(), id)
	return err
}

func (s *sqliteStore) DeleteSessionsByHost(ctx context.Context, hostID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET deleted_at = ? WHERE host_id = ? AND deleted_at IS NULL`,
		now(), hostID)
	return err
}

// ── Session events ────────────────────────────────────────────────────────────

func (s *sqliteStore) AppendSessionEvent(ctx context.Context, e *SessionEvent) error {
	if e.ID == "" {
		e.ID = newID()
	}
	e.CreatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO session_events (id, session_id, seq, type, data, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		e.ID, e.SessionID, e.Seq, e.Type, e.Data, now(),
	)
	return err
}

func (s *sqliteStore) ListSessionEvents(ctx context.Context, sessionID string, afterSeq int) ([]*SessionEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_id, seq, type, data, created_at
		 FROM session_events WHERE session_id = ? AND seq > ?
		 ORDER BY seq ASC`, sessionID, afterSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []*SessionEvent
	for rows.Next() {
		var e SessionEvent
		var createdAt sql.NullString
		if err := rows.Scan(&e.ID, &e.SessionID, &e.Seq, &e.Type, &e.Data, &createdAt); err != nil {
			return nil, err
		}
		if v := parseTime(createdAt); v != nil {
			e.CreatedAt = *v
		}
		events = append(events, &e)
	}
	return events, rows.Err()
}

// ── Approval requests ─────────────────────────────────────────────────────────

func (s *sqliteStore) CreateApprovalRequest(ctx context.Context, r *ApprovalRequest) error {
	if r.ID == "" {
		r.ID = newID()
	}
	r.CreatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO approval_requests
		  (id, session_id, user_id, tool_name, tool_input, risk_level, payload_ciphertext, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?)`,
		r.ID, r.SessionID, r.UserID,
		nullStr(r.ToolName), nullStr(r.ToolInput), nullStr(r.RiskLevel),
		nullStr(r.PayloadCiphertext), now(),
	)
	return err
}

func (s *sqliteStore) GetApprovalRequest(ctx context.Context, id string) (*ApprovalRequest, error) {
	var r ApprovalRequest
	var toolName, toolInput, riskLevel, payloadCiphertext, reason, decidedAt, decidedBy, createdAt sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, session_id, user_id, tool_name, tool_input, risk_level, payload_ciphertext, status,
		       decision_reason, decided_at, decided_by, created_at
		FROM approval_requests WHERE id = ?`, id).Scan(
		&r.ID, &r.SessionID, &r.UserID,
		&toolName, &toolInput, &riskLevel, &payloadCiphertext, &r.Status,
		&reason, &decidedAt, &decidedBy, &createdAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.ToolName = toolName.String
	r.ToolInput = toolInput.String
	r.RiskLevel = riskLevel.String
	r.PayloadCiphertext = payloadCiphertext.String
	r.DecisionReason = reason.String
	r.DecidedAt = parseTime(decidedAt)
	r.DecidedBy = decidedBy.String
	if v := parseTime(createdAt); v != nil {
		r.CreatedAt = *v
	}
	return &r, nil
}

func (s *sqliteStore) ListPendingApprovals(ctx context.Context, userID string) ([]*ApprovalRequest, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, user_id, tool_name, tool_input, risk_level, payload_ciphertext, status,
		       decision_reason, decided_at, decided_by, created_at
		FROM approval_requests WHERE user_id = ? AND status = 'pending'
		ORDER BY created_at ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var requests []*ApprovalRequest
	for rows.Next() {
		var r ApprovalRequest
		var toolName, toolInput, riskLevel, payloadCiphertext, reason, decidedAt, decidedBy, createdAt sql.NullString
		if err := rows.Scan(&r.ID, &r.SessionID, &r.UserID,
			&toolName, &toolInput, &riskLevel, &payloadCiphertext, &r.Status,
			&reason, &decidedAt, &decidedBy, &createdAt); err != nil {
			return nil, err
		}
		r.ToolName = toolName.String
		r.ToolInput = toolInput.String
		r.RiskLevel = riskLevel.String
		r.PayloadCiphertext = payloadCiphertext.String
		r.DecisionReason = reason.String
		r.DecidedAt = parseTime(decidedAt)
		r.DecidedBy = decidedBy.String
		if v := parseTime(createdAt); v != nil {
			r.CreatedAt = *v
		}
		requests = append(requests, &r)
	}
	return requests, rows.Err()
}

func (s *sqliteStore) ResolveApproval(ctx context.Context, id, decision, reason, decidedByUserID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE approval_requests
		SET status = ?, decision_reason = ?, decided_at = ?, decided_by = ?
		WHERE id = ? AND status = 'pending'`,
		decision, nullStr(reason), now(), nullStr(decidedByUserID), id,
	)
	return err
}

// ── Settings ──────────────────────────────────────────────────────────────────

func (s *sqliteStore) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

func (s *sqliteStore) SetSetting(ctx context.Context, key, value, updatedByUserID string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at, updated_by) VALUES (?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		key, value, now(), nullStr(updatedByUserID),
	)
	return err
}

// ── Maintenance ───────────────────────────────────────────────────────────────

func (s *sqliteStore) PruneSessionEvents(ctx context.Context, olderThan time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM session_events
		WHERE session_id IN (
		    SELECT id FROM sessions WHERE stopped_at < ?
		) AND created_at < ?`,
		olderThan.UTC().Format(time.RFC3339Nano),
		olderThan.UTC().Format(time.RFC3339Nano),
	)
	return err
}
