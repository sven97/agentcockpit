package server

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sven97/agentcockpit/internal/protocol"
	"github.com/sven97/agentcockpit/internal/store"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// handleHostWS upgrades a host agent connection.
// Authenticates via Authorization: Bearer <host-token>.
func (s *Server) handleHostWS(w http.ResponseWriter, r *http.Request) {
	token := extractBearerToken(r)
	if token == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}
	host, err := s.store.GetHostByTokenHash(r.Context(), hashToken(token))
	if err != nil || host == nil {
		http.Error(w, "invalid host token", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("host WS upgrade %s: %v", host.ID, err)
		return
	}

	s.store.UpdateHostStatus(context.Background(), host.ID, "online", time.Now())
	s.hub.BroadcastToUser(host.UserID, map[string]any{
		"type":   "host_status",
		"hostId": host.ID,
		"status": "online",
	})

	hc := s.hub.RegisterHost(host.ID, host.UserID, conn)
	go hc.RunWriter()

	// RunReader blocks until the connection drops; clean up after.
	hc.RunReader()

	s.store.UpdateHostStatus(context.Background(), host.ID, "offline", time.Now())
}

// handleBrowserWS upgrades a browser client connection.
func (s *Server) handleBrowserWS(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("browser WS upgrade: %v", err)
		return
	}
	connID := generateToken32()[:16]
	bc := s.hub.RegisterBrowser(connID, user.ID, conn)
	go bc.RunWriter()
	s.hub.PushTokenStates(bc)
	bc.RunReader()
}

// ApprovalPersistFn returns the hub callback that persists approval requests
// to the DB. Called during server setup to wire hub ↔ store without import cycle.
func (s *Server) ApprovalPersistFn() func(ctx context.Context, msg *protocol.ApprovalRequest, userID string) {
	return s.approvalPersistFn
}

func (s *Server) approvalPersistFn(ctx context.Context, msg *protocol.ApprovalRequest, userID string) {
	// Determine session for user_id validation.
	sess, err := s.store.GetSession(ctx, msg.SessionID)
	if err != nil || sess == nil {
		return
	}
	s.store.CreateApprovalRequest(ctx, &store.ApprovalRequest{
		ID:        msg.RequestID,
		SessionID: msg.SessionID,
		UserID:    userID,
		ToolName:  msg.ToolName,
		ToolInput: string(msg.ToolInput),
		RiskLevel: msg.RiskLevel,
	})
	s.store.UpdateSessionStatus(ctx, msg.SessionID, "awaiting_approval")
}
