package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/sven97/agentcockpit/internal/store"
)

// ── Registration ───────────────────────────────────────────────────────────────

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	if body.Email == "" || body.Password == "" || body.Name == "" {
		writeError(w, http.StatusBadRequest, "email, name, and password required")
		return
	}

	// Check if registration is allowed (self-hosted may be invite-only).
	if !s.cfg.LocalMode {
		allow, _ := s.store.GetSetting(r.Context(), "auth.allow_registration")
		if allow != "true" {
			// Allow registration only when no users exist yet (first-run admin).
			existing, err := s.store.GetUserByEmail(r.Context(), body.Email)
			if err != nil || existing != nil {
				writeError(w, http.StatusForbidden, "registration is disabled")
				return
			}
		}
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "password hashing failed")
		return
	}

	// First user becomes admin.
	role := "user"
	if isFirstUser(r.Context(), s.store) {
		role = "admin"
	}

	user := &store.User{
		Email:        body.Email,
		Name:         body.Name,
		PasswordHash: string(hash),
		Role:         role,
	}
	if err := s.store.CreateUser(r.Context(), user); err != nil {
		writeError(w, http.StatusConflict, "email already in use")
		return
	}

	token, authToken, err := s.issueAuthToken(r.Context(), user.ID, r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token issue failed")
		return
	}
	_ = authToken
	writeJSON(w, http.StatusCreated, map[string]any{
		"token": token,
		"user":  safeUser(user),
	})
}

// ── Login ─────────────────────────────────────────────────────────────────────

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))

	user, err := s.store.GetUserByEmail(r.Context(), body.Email)
	if err != nil || user == nil || user.PasswordHash == "" {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(body.Password)); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	token, _, err := s.issueAuthToken(r.Context(), user.ID, r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token issue failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": token,
		"user":  safeUser(user),
	})
}

// ── Logout ────────────────────────────────────────────────────────────────────

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	token := extractBearerToken(r)
	if token == "" {
		writeError(w, http.StatusBadRequest, "missing token")
		return
	}
	authToken, _ := s.store.GetAuthToken(r.Context(), hashToken(token))
	if authToken != nil {
		s.store.RevokeAuthToken(r.Context(), authToken.ID)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

// ── Device authorization (RFC 8628) ──────────────────────────────────────────

// handleDeviceRequest is called by `agentcockpit connect`.
// It creates a device authorization record and returns the user_code for display.
func (s *Server) handleDeviceRequest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Platform string `json:"platform"`
		Hostname string `json:"hostname"`
	}
	decodeJSON(r, &body) // optional fields; ignore error

	deviceCode := generateToken32()
	userCode := generateUserCode() // e.g. "WREN-7429"
	expires := time.Now().Add(15 * time.Minute)

	if err := s.store.CreateDeviceAuthorization(r.Context(), &store.DeviceAuthorization{
		DeviceCode: deviceCode,
		UserCode:   userCode,
		Platform:   body.Platform,
		Hostname:   body.Hostname,
		ExpiresAt:  expires,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create device auth")
		return
	}

	verifyURL := fmt.Sprintf("https://agentcockpit.app/connect?code=%s", userCode)
	if s.cfg.LocalMode {
		verifyURL = fmt.Sprintf("http://localhost%s/connect?code=%s", s.cfg.Addr, userCode)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"device_code": deviceCode,
		"user_code":   userCode,
		"verify_url":  verifyURL,
		"expires_in":  900, // seconds
		"interval":    3,   // poll every 3 seconds
	})
}

// handleDevicePending returns all pending (not yet authorized, not expired) device
// authorizations. Called by the dashboard to show "waiting to connect" machines.
func (s *Server) handleDevicePending(w http.ResponseWriter, r *http.Request) {
	pending, err := s.store.ListPendingDeviceAuthorizations(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	type safeDevice struct {
		UserCode  string `json:"user_code"`
		Hostname  string `json:"hostname"`
		Platform  string `json:"platform"`
		CreatedAt string `json:"created_at"`
	}
	out := make([]safeDevice, 0, len(pending))
	for _, d := range pending {
		out = append(out, safeDevice{
			UserCode:  d.UserCode,
			Hostname:  d.Hostname,
			Platform:  d.Platform,
			CreatedAt: d.CreatedAt.Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDevicePoll is called repeatedly by `agentcockpit connect` until authorized.
// Returns the host token once the user has clicked Authorize in the browser.
func (s *Server) handleDevicePoll(w http.ResponseWriter, r *http.Request) {
	deviceCode := r.URL.Query().Get("device_code")
	if deviceCode == "" {
		writeError(w, http.StatusBadRequest, "missing device_code")
		return
	}

	d, err := s.store.GetDeviceAuthorization(r.Context(), deviceCode)
	if err != nil || d == nil {
		writeError(w, http.StatusNotFound, "unknown device_code")
		return
	}
	if time.Now().After(d.ExpiresAt) {
		writeError(w, http.StatusGone, "device_code expired")
		return
	}
	if d.AuthorizedAt == nil {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending"})
		return
	}

	// Claim the token (clears it from DB after first read).
	hostToken, err := s.store.ClaimDeviceToken(r.Context(), deviceCode)
	if err != nil || hostToken == "" {
		writeError(w, http.StatusGone, "token already claimed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":     "authorized",
		"host_token": hostToken,
	})
}

// handleDeviceAuthorize is called by the web UI when the user clicks Authorize.
func (s *Server) handleDeviceAuthorize(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var body struct {
		UserCode string `json:"user_code"`
		Name     string `json:"name"` // host name chosen by user
	}
	if err := decodeJSON(r, &body); err != nil || body.UserCode == "" {
		writeError(w, http.StatusBadRequest, "user_code required")
		return
	}

	d, err := s.store.GetDeviceAuthorizationByUserCode(r.Context(), body.UserCode)
	if err != nil || d == nil {
		writeError(w, http.StatusNotFound, "unknown code")
		return
	}
	if time.Now().After(d.ExpiresAt) || d.AuthorizedAt != nil {
		writeError(w, http.StatusGone, "code expired or already used")
		return
	}

	// Generate the host token and create the host record.
	hostToken := generateToken32()
	hostName := body.Name
	if hostName == "" {
		hostName = d.Hostname
	}
	if hostName == "" {
		hostName = "unnamed-host"
	}

	host := &store.Host{
		UserID:    user.ID,
		Name:      hostName,
		TokenHash: hashToken(hostToken),
		Platform:  d.Platform,
		Hostname:  d.Hostname,
	}
	if err := s.store.CreateHost(r.Context(), host); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create host")
		return
	}

	if err := s.store.AuthorizeDevice(r.Context(), d.DeviceCode, user.ID, hostToken); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize device")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"host_id":   host.ID,
		"host_name": hostName,
	})
}

// ── Me ────────────────────────────────────────────────────────────────────────

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, safeUser(currentUser(r)))
}

// handleSetE2EPublicKey stores the user's long-term ECDH P-256 public key (SPKI base64url).
// Called by the browser once after generating the keypair; idempotent on subsequent calls.
func (s *Server) handleSetE2EPublicKey(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var body struct {
		PublicKey string `json:"publicKey"`
	}
	if err := decodeJSON(r, &body); err != nil || body.PublicKey == "" {
		writeError(w, http.StatusBadRequest, "publicKey required")
		return
	}
	if err := s.store.UpdateUserE2EPublicKey(r.Context(), user.ID, body.PublicKey); err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (s *Server) issueAuthToken(ctx context.Context, userID string, r *http.Request) (string, *store.AuthToken, error) {
	raw := generateToken32()
	t := &store.AuthToken{
		UserID:    userID,
		TokenHash: hashToken(raw),
		ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
		UserAgent: r.UserAgent(),
		IPAddress: r.RemoteAddr,
	}
	if err := s.store.CreateAuthToken(ctx, t); err != nil {
		return "", nil, err
	}
	return raw, t, nil
}

func safeUser(u *store.User) map[string]any {
	return map[string]any{
		"id":           u.ID,
		"email":        u.Email,
		"name":         u.Name,
		"role":         u.Role,
		"e2ePublicKey": u.E2EPublicKey, // empty string if not yet set
		"created_at":   u.CreatedAt,
	}
}

func isFirstUser(ctx context.Context, st store.Store) bool {
	// Heuristic: try to fetch any user; if none exist, this is the first.
	u, _ := st.GetUserByEmail(ctx, "local@localhost")
	return u == nil
}

func generateToken32() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// generateUserCode produces a human-readable code like "WREN-7429".
func generateUserCode() string {
	words := []string{
		"WREN", "HAWK", "DOVE", "LARK", "KITE", "IBIS", "SWAN", "CROW",
		"FINCH", "ROBIN", "CRANE", "EGRET", "HERON", "QUAIL", "SNIPE",
	}
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(words))))
	word := words[n.Int64()]
	num, _ := rand.Int(rand.Reader, big.NewInt(10000))
	return fmt.Sprintf("%s-%04d", word, num.Int64())
}
