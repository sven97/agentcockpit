package server

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/sven97/agentcockpit/internal/protocol"
	"github.com/sven97/agentcockpit/internal/store"
)

// ── Host invite / claim ────────────────────────────────────────────────────────

// handleHostInvite is called by the browser dashboard. It generates a
// short-lived token that the CLI can claim without any browser interaction.
func (s *Server) handleHostInvite(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	token := generateToken32()
	s.hostInvites.Store(token, hostInvite{
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(15 * time.Minute),
	})
	writeJSON(w, http.StatusCreated, map[string]string{"token": token})
}

// handleHostClaim is called by `agentcockpit connect --invite <token>`.
// It exchanges the invite token for a permanent host token.
func (s *Server) handleHostClaim(w http.ResponseWriter, r *http.Request) {
	var body struct {
		InviteToken string `json:"invite_token"`
		Name        string `json:"name"`
		Hostname    string `json:"hostname"`
		Platform    string `json:"platform"`
	}
	if err := decodeJSON(r, &body); err != nil || body.InviteToken == "" {
		writeError(w, http.StatusBadRequest, "invite_token required")
		return
	}

	val, ok := s.hostInvites.LoadAndDelete(body.InviteToken)
	if !ok {
		writeError(w, http.StatusNotFound, "invite token not found or already used")
		return
	}
	invite := val.(hostInvite)
	if time.Now().After(invite.ExpiresAt) {
		writeError(w, http.StatusGone, "invite token expired")
		return
	}

	name := body.Name
	if name == "" {
		name = body.Hostname
	}
	if name == "" {
		name = "unnamed-host"
	}

	hostToken := generateToken32()
	host := &store.Host{
		UserID:    invite.UserID,
		Name:      name,
		Hostname:  body.Hostname,
		Platform:  body.Platform,
		TokenHash: hashToken(hostToken),
	}
	if err := s.store.CreateHost(r.Context(), host); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create host")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"host_token": hostToken,
		"host_id":    host.ID,
	})
}

// ── Hosts ─────────────────────────────────────────────────────────────────────

func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	hosts, err := s.store.ListHostsByUser(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	if hosts == nil {
		hosts = []*store.Host{}
	}
	writeJSON(w, http.StatusOK, hosts)
}

func (s *Server) handleDeleteHost(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := r.PathValue("id")

	host, err := s.store.GetHostByID(r.Context(), id)
	if err != nil || host == nil || host.UserID != user.ID {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	// Notify the agent so it stops and removes its config before we delete.
	s.hub.SendToHost(id, map[string]string{"type": protocol.TypeHostRemoved})

	if err := s.store.DeleteSessionsByHost(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	if err := s.store.DeleteHost(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	// Notify browser clients so they refresh hosts and sessions immediately.
	s.hub.BroadcastToUser(user.ID, map[string]any{
		"type":   "host_deleted",
		"hostId": id,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ── Sessions ──────────────────────────────────────────────────────────────────

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	sessions, err := s.store.ListSessionsByUser(r.Context(), user.ID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	if sessions == nil {
		sessions = []*store.Session{}
	}
	writeJSON(w, http.StatusOK, sessions)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := r.PathValue("id")
	sess, err := s.store.GetSession(r.Context(), id)
	if err != nil || sess == nil || sess.UserID != user.ID {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

// handleCreateSession creates a new session record and instructs the host agent
// to spawn the PTY process.
func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var body struct {
		HostID     string `json:"host_id"`
		AgentType  string `json:"agent_type"`
		WorkingDir string `json:"working_dir"`
		Name       string `json:"name"`
		Command    string `json:"command"` // optional override; defaults to agent binary
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.HostID == "" || body.AgentType == "" || body.WorkingDir == "" {
		writeError(w, http.StatusBadRequest, "host_id, agent_type, working_dir required")
		return
	}

	host, err := s.store.GetHostByID(r.Context(), body.HostID)
	if err != nil || host == nil || host.UserID != user.ID {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}

	command := body.Command
	if command == "" && body.AgentType != "custom" {
		command = defaultCommand(body.AgentType)
	}
	if command == "" {
		writeError(w, http.StatusBadRequest, "command required for custom agent type")
		return
	}

	sess := &store.Session{
		UserID:     user.ID,
		HostID:     body.HostID,
		Name:       body.Name,
		AgentType:  body.AgentType,
		WorkingDir: body.WorkingDir,
		Command:    command,
	}
	if err := s.store.CreateSession(r.Context(), sess); err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	// Tell the host agent to spawn the session.
	s.hub.SendToHost(body.HostID, protocol.SessionCreate{
		Type:      protocol.TypeSessionCreate,
		SessionID: sess.ID,
		AgentType: body.AgentType,
		CWD:       body.WorkingDir,
		Command:   command,
	})

	writeJSON(w, http.StatusCreated, sess)
}

func (s *Server) handleKillSession(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := r.PathValue("id")

	sess, err := s.store.GetSession(r.Context(), id)
	if err != nil || sess == nil || sess.UserID != user.ID {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	s.hub.SendToHost(sess.HostID, protocol.SessionKill{
		Type:      protocol.TypeSessionKill,
		SessionID: id,
	})
	s.store.UpdateSessionStatus(r.Context(), id, "stopped")
	writeJSON(w, http.StatusOK, map[string]string{"status": "killed"})
}

func (s *Server) handleSessionEvents(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := r.PathValue("id")

	sess, err := s.store.GetSession(r.Context(), id)
	if err != nil || sess == nil || sess.UserID != user.ID {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	afterSeq := 0
	if a := r.URL.Query().Get("after_seq"); a != "" {
		afterSeq, _ = strconv.Atoi(a)
	}

	events, err := s.store.ListSessionEvents(r.Context(), id, afterSeq)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	if events == nil {
		events = []*store.SessionEvent{}
	}
	writeJSON(w, http.StatusOK, events)
}

// ── Approvals ─────────────────────────────────────────────────────────────────

func (s *Server) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	approvals, err := s.store.ListPendingApprovals(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	if approvals == nil {
		approvals = []*store.ApprovalRequest{}
	}
	writeJSON(w, http.StatusOK, approvals)
}

func (s *Server) handleResolveApproval(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := r.PathValue("id")

	var body struct {
		Decision string `json:"decision"` // "approved" | "rejected"
		Reason   string `json:"reason"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Decision != "approved" && body.Decision != "rejected" {
		writeError(w, http.StatusBadRequest, "decision must be 'approved' or 'rejected'")
		return
	}

	approval, err := s.store.GetApprovalRequest(r.Context(), id)
	if err != nil || approval == nil || approval.UserID != user.ID {
		writeError(w, http.StatusNotFound, "approval not found")
		return
	}

	if err := s.store.ResolveApproval(r.Context(), id, body.Decision, body.Reason, user.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	// Map UI decision ("approved"/"rejected") to protocol decision ("allow"/"deny").
	protoDecision := "allow"
	if body.Decision == "rejected" {
		protoDecision = "deny"
	}

	// Find which host owns the session, then deliver the response (WS + in-memory channel).
	sess, _ := s.store.GetSession(r.Context(), approval.SessionID)
	if sess != nil {
		s.hub.DeliverApprovalToHost(sess.HostID, &protocol.ApprovalResponse{
			Type:      protocol.TypeApprovalResponse,
			RequestID: id,
			Decision:  protoDecision,
			Reason:    body.Reason,
		})
	}

	// Push updated approval state to all browser clients of this user.
	s.hub.BroadcastToUser(user.ID, map[string]any{
		"type":     "approval_resolved",
		"id":       id,
		"decision": body.Decision,
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": body.Decision})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func defaultCommand(agentType string) string {
	switch agentType {
	case "claude-code":
		return "claude"
	case "codex":
		return "codex"
	case "opencode":
		return "opencode"
	default:
		return agentType
	}
}

// persistApprovalRequest is called by the hub's approval flow to save requests to the DB.
// Exported so the hub can call it without importing server (avoids import cycle).
func (s *Server) persistApprovalRequest(ctx context.Context, sessionID, userID, toolName, toolInput, riskLevel, requestID string) error {
	return s.store.CreateApprovalRequest(ctx, &store.ApprovalRequest{
		ID:        requestID,
		SessionID: sessionID,
		UserID:    userID,
		ToolName:  toolName,
		ToolInput: toolInput,
		RiskLevel: riskLevel,
	})
}
