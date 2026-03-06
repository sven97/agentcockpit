package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/sven97/agentcockpit/internal/store"
)

type contextKey string

const ctxKeyUser contextKey = "user"

// requireAuth wraps a handler and requires a valid browser session token.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.LocalMode {
			// In local mode, attach the implicit user and skip token check.
			user, err := s.store.GetUserByEmail(r.Context(), "local@localhost")
			if err != nil || user == nil {
				writeError(w, http.StatusInternalServerError, "local user missing")
				return
			}
			next(w, r.WithContext(context.WithValue(r.Context(), ctxKeyUser, user)))
			return
		}

		token := extractBearerToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "missing token")
			return
		}

		authToken, err := s.store.GetAuthToken(r.Context(), hashToken(token))
		if err != nil || authToken == nil {
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		if authToken.RevokedAt != nil || time.Now().After(authToken.ExpiresAt) {
			writeError(w, http.StatusUnauthorized, "token expired")
			return
		}

		user, err := s.store.GetUserByID(r.Context(), authToken.UserID)
		if err != nil || user == nil {
			writeError(w, http.StatusUnauthorized, "user not found")
			return
		}

		// Touch last_used_at asynchronously — don't block the request.
		go s.store.TouchAuthToken(context.Background(), authToken.ID)

		next(w, r.WithContext(context.WithValue(r.Context(), ctxKeyUser, user)))
	}
}

// requireBrowserAuth returns an http.HandlerFunc (for WebSocket upgrade paths).
func (s *Server) requireBrowserAuth(next http.HandlerFunc) http.HandlerFunc {
	return s.requireAuth(next)
}

// currentUser retrieves the authenticated user from the request context.
func currentUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(ctxKeyUser).(*store.User)
	return u
}

// extractBearerToken reads a Bearer token from the Authorization header or
// a `token` query param (used by WebSocket clients that can't set headers).
func extractBearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return r.URL.Query().Get("token")
}

// hashToken returns the hex-encoded SHA-256 of a raw token string.
func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// ── JSON helpers ──────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20) // 1 MB limit
	return json.NewDecoder(r.Body).Decode(v)
}
